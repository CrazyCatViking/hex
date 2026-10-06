package postgres

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	hex "github.com/crazycatviking/hex/server"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestCredentialVersionsAgainstPostgres(t *testing.T) {
	database, ctx := openTestDatabase(t)
	owner := "credential-version-" + rand.Text()
	now := time.Now().UTC().Truncate(time.Microsecond)
	if err := database.PutCredential(ctx, hex.CredentialRecord{
		Owner: owner, Connector: "docs", Sealed: []byte("original"), Account: "original", ConnectedAt: now, LastUsedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := database.DeleteCredential(cleanup, owner, "docs"); err != nil && !errors.Is(err, hex.ErrNotFound) {
			t.Error(err)
		}
	})
	original, err := database.GetCredential(ctx, owner, "docs")
	if err != nil || original.Generation == "" || original.Version == "" {
		t.Fatalf("missing generation/version: %+v %v", original, err)
	}
	updated := original
	updated.Sealed = []byte("rotated")
	updated.LastUsedAt = now.Add(-time.Hour)
	updated.Account = "must not replace metadata"
	if err := database.WithCredentialLock(ctx, owner, "docs", func(ctx context.Context) error {
		return database.UpdateCredential(ctx, updated)
	}); err != nil {
		t.Fatal(err)
	}
	current, err := database.GetCredential(ctx, owner, "docs")
	if err != nil || current.Generation != original.Generation || current.Version == original.Version || current.Account != original.Account || !current.LastUsedAt.Equal(now) {
		t.Fatalf("rotation changed generation/metadata or regressed usage: %+v %v", current, err)
	}
	assertStale := func(record hex.CredentialRecord) {
		t.Helper()
		if err := database.UpdateCredential(ctx, record); !errors.Is(err, hex.ErrCredentialChanged) {
			t.Fatalf("stale refresh update: %v", err)
		}
		if err := database.DeleteCredentialVersion(ctx, owner, "docs", record.Version); !errors.Is(err, hex.ErrCredentialChanged) {
			t.Fatalf("stale refresh deletion: %v", err)
		}
		if err := database.TouchCredential(ctx, owner, "docs", record.Version, now.Add(time.Hour)); !errors.Is(err, hex.ErrCredentialChanged) {
			t.Fatalf("stale usage update: %v", err)
		}
	}
	assertStale(original)
	if err := database.DeleteCredential(ctx, owner, "docs"); err != nil {
		t.Fatal(err)
	}
	assertStale(current)
	if err := database.PutCredential(ctx, current); err != nil {
		t.Fatal(err)
	}
	replacement, err := database.GetCredential(ctx, owner, "docs")
	if err != nil || replacement.Generation == current.Generation || replacement.Version == current.Version {
		t.Fatalf("reconnect reused generation/version: %+v %v", replacement, err)
	}
	assertStale(current)
	// Revocation deletes must persist even though the refresh callback reports
	// ErrNotConnected, and must release the advisory lock afterward.
	err = database.WithCredentialLock(ctx, owner, "docs", func(ctx context.Context) error {
		if err := database.DeleteCredentialVersion(ctx, owner, "docs", replacement.Version); err != nil {
			return err
		}
		return hex.ErrNotConnected
	})
	if !errors.Is(err, hex.ErrNotConnected) {
		t.Fatalf("callback error was lost: %v", err)
	}
	if _, err := database.GetCredential(ctx, owner, "docs"); !errors.Is(err, hex.ErrNotFound) {
		t.Fatalf("revocation deletion was rolled back: %v", err)
	}
	if err := database.WithCredentialLock(ctx, owner, "docs", func(context.Context) error { return nil }); err != nil {
		t.Fatalf("lock was not released after callback error: %v", err)
	}
}

func TestPendingWithdrawalAgainstPostgres(t *testing.T) {
	database, ctx := openTestDatabase(t)
	site := "withdraw-" + rand.Text()
	defer func() {
		if err := database.DeleteIntegrationApproval(context.Background(), site, "docs"); err != nil && !errors.Is(err, hex.ErrNotFound) {
			t.Error(err)
		}
	}()
	if err := database.PutIntegrationApproval(ctx, hex.IntegrationApproval{Site: site, Integration: "docs", Status: hex.ApprovalRequested}); err != nil {
		t.Fatal(err)
	}
	if err := database.WithdrawIntegrationApproval(ctx, site, "docs"); err != nil {
		t.Fatal(err)
	}
	if err := database.WithdrawIntegrationApproval(ctx, site, "docs"); err != nil {
		t.Fatal("missing withdrawal should be idempotent", err)
	}
	if err := database.PutIntegrationApproval(ctx, hex.IntegrationApproval{Site: site, Integration: "docs", Status: hex.ApprovalApproved}); err != nil {
		t.Fatal(err)
	}
	if err := database.WithdrawIntegrationApproval(ctx, site, "docs"); !errors.Is(err, hex.ErrForbidden) {
		t.Fatalf("approved withdrawal accepted: %v", err)
	}
	approval, err := database.GetIntegrationApproval(ctx, site, "docs")
	if err != nil || approval.Status != hex.ApprovalApproved {
		t.Fatalf("approved record changed: %+v %v", approval, err)
	}
}

func openSingleConnectionCredentialDatabase(t *testing.T) (*Database, context.Context) {
	t.Helper()
	connection := os.Getenv("HEX_TEST_POSTGRES_URL")
	if connection == "" {
		t.Skip("set HEX_TEST_POSTGRES_URL to run against PostgreSQL")
	}
	config, err := pgxpool.ParseConfig(connection)
	if err != nil {
		t.Fatal(err)
	}
	config.MaxConns = 1
	config.MinConns = 0
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	database := &Database{pool: pool}
	t.Cleanup(database.Close)
	if err := database.MigrateIntegrations(ctx); err != nil {
		t.Fatal(err)
	}
	return database, ctx
}

func TestCredentialRefreshUsesOnePostgresConnection(t *testing.T) {
	database, ctx := openSingleConnectionCredentialDatabase(t)
	owner := "credential-single-connection-" + rand.Text()
	now := time.Now().UTC().Truncate(time.Microsecond)
	if err := database.PutCredential(ctx, hex.CredentialRecord{
		Owner: owner, Connector: "docs", Sealed: []byte("original"), ConnectedAt: now, LastUsedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	// The lock holds the pool's only connection. Every callback operation must
	// use its transaction instead of waiting for a second pool connection.
	err := database.WithCredentialLock(ctx, owner, "docs", func(ctx context.Context) error {
		record, err := database.GetCredential(ctx, owner, "docs")
		if err != nil {
			return err
		}
		record.Sealed = []byte("rotated")
		if err := database.UpdateCredential(ctx, record); err != nil {
			return err
		}
		record, err = database.GetCredential(ctx, owner, "docs")
		if err != nil {
			return err
		}
		if err := database.TouchCredential(ctx, owner, "docs", record.Version, now.Add(time.Hour)); err != nil {
			return err
		}
		record, err = database.GetCredential(ctx, owner, "docs")
		if err != nil {
			return err
		}
		if err := database.DeleteCredentialVersion(ctx, owner, "docs", record.Version); err != nil {
			return err
		}
		return hex.ErrNotConnected
	})
	if !errors.Is(err, hex.ErrNotConnected) {
		t.Fatalf("credential callback failed or waited for its own connection: %v", err)
	}
	if _, err := database.GetCredential(ctx, owner, "docs"); !errors.Is(err, hex.ErrNotFound) {
		t.Fatalf("conditional delete was not committed: %v", err)
	}
}

func TestCredentialRefreshDisconnectRaceAgainstPostgres(t *testing.T) {
	first, ctx := openSingleConnectionCredentialDatabase(t)
	second, _ := openSingleConnectionCredentialDatabase(t)
	for _, revoked := range []bool{false, true} {
		for _, reconnect := range []bool{false, true} {
			t.Run(fmt.Sprintf("revoked=%t/reconnect=%t", revoked, reconnect), func(t *testing.T) {
				owner := "credential-disconnect-race-" + rand.Text()
				now := time.Now().UTC().Truncate(time.Microsecond)
				if err := first.PutCredential(ctx, hex.CredentialRecord{
					Owner: owner, Connector: "docs", Sealed: []byte("original"), ConnectedAt: now, LastUsedAt: now,
				}); err != nil {
					t.Fatal(err)
				}
				started := make(chan struct{})
				release := make(chan struct{}, 1)
				finished := make(chan error, 1)
				go func() {
					finished <- first.WithCredentialLock(ctx, owner, "docs", func(ctx context.Context) error {
						record, err := first.GetCredential(ctx, owner, "docs")
						if err != nil {
							return err
						}
						close(started)
						select {
						case <-release:
						case <-ctx.Done():
							return ctx.Err()
						}
						if revoked {
							return first.DeleteCredentialVersion(ctx, owner, "docs", record.Version)
						}
						record.Sealed = []byte("stale-refresh")
						return first.UpdateCredential(ctx, record)
					})
				}()
				// Unblock the callback on all exits, including a failing disconnect.
				defer close(release)
				select {
				case <-started:
				case err := <-finished:
					t.Fatalf("refresh did not start: %v", err)
				case <-ctx.Done():
					t.Fatal(ctx.Err())
				}
				if err := second.DeleteCredential(ctx, owner, "docs"); err != nil {
					t.Fatalf("disconnect was blocked by refresh: %v", err)
				}
				var replacement hex.CredentialRecord
				if reconnect {
					if err := second.PutCredential(ctx, hex.CredentialRecord{
						Owner: owner, Connector: "docs", Sealed: []byte("replacement"), ConnectedAt: now, LastUsedAt: now,
					}); err != nil {
						t.Fatalf("reconnect was blocked by refresh: %v", err)
					}
					var err error
					replacement, err = second.GetCredential(ctx, owner, "docs")
					if err != nil {
						t.Fatal(err)
					}
				}
				// The callback also watches cancellation; a buffered signal avoids
				// blocking this test if its deadline has just expired.
				select {
				case release <- struct{}{}:
				case <-ctx.Done():
					t.Fatal(ctx.Err())
				}
				if err := <-finished; !errors.Is(err, hex.ErrCredentialChanged) {
					t.Fatalf("stale callback affected a disconnected/replaced account: %v", err)
				}
				current, err := second.GetCredential(ctx, owner, "docs")
				if reconnect {
					if err != nil || current.Version != replacement.Version || current.Generation != replacement.Generation || string(current.Sealed) != "replacement" {
						t.Fatalf("stale callback affected replacement: %+v %v", current, err)
					}
					if err := second.DeleteCredential(ctx, owner, "docs"); err != nil {
						t.Fatal(err)
					}
				} else if !errors.Is(err, hex.ErrNotFound) {
					t.Fatalf("stale refresh recreated a deleted record: %+v %v", current, err)
				}
			})
		}
	}
}

func TestCredentialRefreshLockAcrossPostgresInstances(t *testing.T) {
	first, ctx := openTestDatabase(t)
	second, err := New(ctx, os.Getenv("HEX_TEST_POSTGRES_URL"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(second.Close)
	owner := "credential-lock-" + rand.Text()
	entered := make(chan struct{})
	release := make(chan struct{}, 1)
	finished := make(chan error, 1)
	defer close(release)
	go func() {
		finished <- first.WithCredentialLock(ctx, owner, "docs", func(context.Context) error {
			close(entered)
			select {
			case <-release:
			case <-ctx.Done():
				return ctx.Err()
			}
			return nil
		})
	}()
	select {
	case <-entered:
	case err := <-finished:
		t.Fatalf("first refresh did not acquire its lock: %v", err)
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	blocked, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
	defer cancel()
	if err := second.WithCredentialLock(blocked, owner, "docs", func(context.Context) error {
		t.Error("another database instance entered the same refresh lock")
		return nil
	}); err == nil {
		t.Fatal("second refresh did not block on the first instance")
	}
	// A lock for a different account remains independent.
	if err := second.WithCredentialLock(ctx, owner+"-other", "docs", func(context.Context) error { return nil }); err != nil {
		t.Fatalf("unrelated account was blocked: %v", err)
	}
	release <- struct{}{}
	if err := <-finished; err != nil {
		t.Fatal(err)
	}
	if err := second.WithCredentialLock(ctx, owner, "docs", func(context.Context) error { return nil }); err != nil {
		t.Fatalf("shared refresh lock was not released: %v", err)
	}
}

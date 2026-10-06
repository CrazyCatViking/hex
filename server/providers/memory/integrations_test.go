package memory

import (
	"context"
	"errors"
	"testing"
	"time"

	hex "github.com/crazycatviking/hex/server"
)

func TestCredentialConditionalOperations(t *testing.T) {
	store := NewIntegrationStore()
	ctx := context.Background()
	now := time.Now().UTC()
	record := hex.CredentialRecord{Owner: "person", Connector: "docs", Sealed: []byte("old"), Account: "original", ConnectedAt: now, LastUsedAt: now}
	if err := store.PutCredential(ctx, record); err != nil {
		t.Fatal(err)
	}
	original, err := store.GetCredential(ctx, "person", "docs")
	if err != nil || original.Generation == "" || original.Version == "" {
		t.Fatalf("missing credential generation/version: %+v %v", original, err)
	}
	updated := original
	updated.Sealed = []byte("refreshed")
	updated.Account = "must not replace metadata"
	updated.LastUsedAt = now.Add(-time.Hour)
	if err := store.UpdateCredential(ctx, updated); err != nil {
		t.Fatal(err)
	}
	current, err := store.GetCredential(ctx, "person", "docs")
	if err != nil || current.Generation != original.Generation || current.Version == original.Version || current.Account != original.Account || !current.LastUsedAt.Equal(now) {
		t.Fatalf("refresh did not preserve generation/metadata/monotonic usage: %+v %v", current, err)
	}
	assertStale := func(record hex.CredentialRecord) {
		t.Helper()
		if err := store.UpdateCredential(ctx, record); !errors.Is(err, hex.ErrCredentialChanged) {
			t.Fatalf("stale update: %v", err)
		}
		if err := store.TouchCredential(ctx, record.Owner, record.Connector, record.Version, now.Add(time.Hour)); !errors.Is(err, hex.ErrCredentialChanged) {
			t.Fatalf("stale touch: %v", err)
		}
		if err := store.DeleteCredentialVersion(ctx, record.Owner, record.Connector, record.Version); !errors.Is(err, hex.ErrCredentialChanged) {
			t.Fatalf("stale delete: %v", err)
		}
	}
	assertStale(original)
	if err := store.DeleteCredential(ctx, "person", "docs"); err != nil {
		t.Fatal(err)
	}
	assertStale(current)
	if err := store.PutCredential(ctx, current); err != nil {
		t.Fatal(err)
	}
	replacement, err := store.GetCredential(ctx, "person", "docs")
	if err != nil || replacement.Generation == current.Generation || replacement.Version == current.Version {
		t.Fatalf("reconnect reused generation/version: %+v %v", replacement, err)
	}
	assertStale(current)
}

func TestCredentialRefreshLockHonorsCancellationAndReleasesAfterError(t *testing.T) {
	store := NewIntegrationStore()
	ctx := context.Background()
	entered := make(chan struct{})
	release := make(chan struct{})
	finished := make(chan error, 1)
	expected := errors.New("callback failed")
	go func() {
		finished <- store.WithCredentialLock(ctx, "person", "docs", func(context.Context) error {
			close(entered)
			<-release
			return expected
		})
	}()
	<-entered
	blocked, cancel := context.WithTimeout(ctx, 20*time.Millisecond)
	defer cancel()
	err := store.WithCredentialLock(blocked, "person", "docs", func(context.Context) error {
		t.Error("callback ran while another refresh held the lock")
		return nil
	})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("lock acquisition ignored cancellation: %v", err)
	}
	close(release)
	if err := <-finished; !errors.Is(err, expected) {
		t.Fatalf("callback error was lost: %v", err)
	}
	if err := store.WithCredentialLock(blocked, "person", "docs", func(context.Context) error {
		t.Error("callback ran with an expired context")
		return nil
	}); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expired context was ignored: %v", err)
	}
	if err := store.WithCredentialLock(ctx, "person", "docs", func(context.Context) error { return nil }); err != nil {
		t.Fatalf("lock was not released after callback error: %v", err)
	}
}

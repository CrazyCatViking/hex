package postgres

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	hex "github.com/crazycatviking/hex/server"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

func (d *Database) migrateIntegrations(ctx context.Context) error {
	const statements = `
		CREATE TABLE IF NOT EXISTS hex_integration_approvals (
			site text NOT NULL,
			integration text NOT NULL,
			approval jsonb NOT NULL,
			PRIMARY KEY (site, integration)
		);
		CREATE TABLE IF NOT EXISTS hex_integration_credentials (
			owner text NOT NULL,
			connector text NOT NULL,
			sealed bytea NOT NULL,
			updated_at timestamptz NOT NULL DEFAULT now(),
			PRIMARY KEY (owner, connector)
		);
		ALTER TABLE hex_integration_credentials
			ADD COLUMN IF NOT EXISTS account text NOT NULL DEFAULT '',
			ADD COLUMN IF NOT EXISTS connected_at timestamptz NOT NULL DEFAULT now(),
			ADD COLUMN IF NOT EXISTS last_used_at timestamptz NOT NULL DEFAULT now();
		ALTER TABLE hex_integration_credentials
			ADD COLUMN IF NOT EXISTS generation text NOT NULL DEFAULT md5(random()::text || clock_timestamp()::text),
			ADD COLUMN IF NOT EXISTS version text NOT NULL DEFAULT md5(random()::text || clock_timestamp()::text);
		CREATE INDEX IF NOT EXISTS hex_integration_credentials_last_used
			ON hex_integration_credentials (last_used_at)`
	if _, err := d.pool.Exec(ctx, statements); err != nil {
		return fmt.Errorf("create integration tables: %w", err)
	}
	return nil
}

func (d *Database) GetIntegrationApproval(ctx context.Context, site, integration string) (hex.IntegrationApproval, error) {
	var data []byte
	err := d.pool.QueryRow(ctx,
		`SELECT approval FROM hex_integration_approvals WHERE site = $1 AND integration = $2`,
		site, integration).Scan(&data)
	if errors.Is(err, pgx.ErrNoRows) {
		return hex.IntegrationApproval{}, hex.ErrNotFound
	}
	if err != nil {
		return hex.IntegrationApproval{}, fmt.Errorf("read integration approval: %w", err)
	}
	var approval hex.IntegrationApproval
	if err := json.Unmarshal(data, &approval); err != nil {
		return hex.IntegrationApproval{}, fmt.Errorf("decode integration approval: %w", err)
	}
	return approval, nil
}

func (d *Database) PutIntegrationApproval(ctx context.Context, approval hex.IntegrationApproval) error {
	data, err := json.Marshal(approval)
	if err != nil {
		return err
	}
	const statement = `
		INSERT INTO hex_integration_approvals (site, integration, approval)
		VALUES ($1, $2, $3)
		ON CONFLICT (site, integration) DO UPDATE SET approval = EXCLUDED.approval`
	if _, err := d.pool.Exec(ctx, statement, approval.Site, approval.Integration, data); err != nil {
		return fmt.Errorf("save integration approval: %w", err)
	}
	return nil
}

func (d *Database) DeleteIntegrationApproval(ctx context.Context, site, integration string) error {
	result, err := d.pool.Exec(ctx,
		`DELETE FROM hex_integration_approvals WHERE site = $1 AND integration = $2`, site, integration)
	if err != nil {
		return fmt.Errorf("delete integration approval: %w", err)
	}
	if result.RowsAffected() == 0 {
		return hex.ErrNotFound
	}
	return nil
}

func (d *Database) ListIntegrationApprovals(ctx context.Context) ([]hex.IntegrationApproval, error) {
	rows, err := d.pool.Query(ctx, `SELECT approval FROM hex_integration_approvals ORDER BY site, integration`)
	if err != nil {
		return nil, fmt.Errorf("list integration approvals: %w", err)
	}
	defer rows.Close()

	approvals := make([]hex.IntegrationApproval, 0)
	for rows.Next() {
		var data []byte
		if err := rows.Scan(&data); err != nil {
			return nil, err
		}
		var approval hex.IntegrationApproval
		if err := json.Unmarshal(data, &approval); err != nil {
			return nil, fmt.Errorf("decode integration approval: %w", err)
		}
		approvals = append(approvals, approval)
	}
	return approvals, rows.Err()
}

const credentialColumns = `owner, connector, sealed, account, connected_at, last_used_at, generation, version`

func scanCredential(row pgx.Row) (hex.CredentialRecord, error) {
	var record hex.CredentialRecord
	err := row.Scan(&record.Owner, &record.Connector, &record.Sealed, &record.Account, &record.ConnectedAt, &record.LastUsedAt, &record.Generation, &record.Version)
	record.ConnectedAt = record.ConnectedAt.UTC()
	record.LastUsedAt = record.LastUsedAt.UTC()
	return record, err
}

func (d *Database) GetCredential(ctx context.Context, owner, connector string) (hex.CredentialRecord, error) {
	row := d.credentialExecutor(ctx).QueryRow(ctx,
		`SELECT `+credentialColumns+` FROM hex_integration_credentials WHERE owner = $1 AND connector = $2`,
		owner, connector)
	record, err := scanCredential(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return hex.CredentialRecord{}, hex.ErrNotFound
	}
	if err != nil {
		return hex.CredentialRecord{}, fmt.Errorf("read credential: %w", err)
	}
	return record, nil
}

func (d *Database) PutCredential(ctx context.Context, record hex.CredentialRecord) error {
	const statement = `
		INSERT INTO hex_integration_credentials (owner, connector, sealed, account, connected_at, last_used_at, generation, version, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, now())
		ON CONFLICT (owner, connector) DO UPDATE SET
			sealed = EXCLUDED.sealed, account = EXCLUDED.account, connected_at = EXCLUDED.connected_at,
			last_used_at = EXCLUDED.last_used_at, generation = EXCLUDED.generation,
			version = EXCLUDED.version, updated_at = now()`
	_, err := d.pool.Exec(ctx, statement, record.Owner, record.Connector, record.Sealed, record.Account,
		record.ConnectedAt, record.LastUsedAt, rand.Text(), rand.Text())
	if err != nil {
		return fmt.Errorf("save credential: %w", err)
	}
	return nil
}

func (d *Database) UpdateCredential(ctx context.Context, record hex.CredentialRecord) error {
	result, err := d.credentialExecutor(ctx).Exec(ctx, `
		UPDATE hex_integration_credentials SET sealed = $4, last_used_at = GREATEST(last_used_at, $5),
		version = $6, updated_at = now() WHERE owner = $1 AND connector = $2 AND version = $3`,
		record.Owner, record.Connector, record.Version, record.Sealed, record.LastUsedAt, rand.Text())
	if err != nil {
		return fmt.Errorf("update credential: %w", err)
	}
	if result.RowsAffected() == 0 {
		return hex.ErrCredentialChanged
	}
	return nil
}

func (d *Database) TouchCredential(ctx context.Context, owner, connector, version string, usedAt time.Time) error {
	result, err := d.credentialExecutor(ctx).Exec(ctx,
		`UPDATE hex_integration_credentials SET last_used_at = GREATEST(last_used_at, $4), version = $5 WHERE owner = $1 AND connector = $2 AND version = $3`,
		owner, connector, version, usedAt, rand.Text())
	if err != nil {
		return fmt.Errorf("record credential use: %w", err)
	}
	if result.RowsAffected() == 0 {
		return hex.ErrCredentialChanged
	}
	return nil
}

func (d *Database) DeleteCredentialVersion(ctx context.Context, owner, connector, version string) error {
	result, err := d.credentialExecutor(ctx).Exec(ctx,
		`DELETE FROM hex_integration_credentials WHERE owner = $1 AND connector = $2 AND version = $3`, owner, connector, version)
	if err != nil {
		return fmt.Errorf("conditionally delete credential: %w", err)
	}
	if result.RowsAffected() == 0 {
		return hex.ErrCredentialChanged
	}
	return nil
}

type credentialTransactionKey struct{}

type credentialExecutor interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
	QueryRow(context.Context, string, ...any) pgx.Row
}

type credentialTransaction struct {
	database *Database
	tx       pgx.Tx
}

func (d *Database) credentialExecutor(ctx context.Context) credentialExecutor {
	if transaction, ok := ctx.Value(credentialTransactionKey{}).(credentialTransaction); ok && transaction.database == d {
		return transaction.tx
	}
	return d.pool
}

func (d *Database) WithCredentialLock(ctx context.Context, owner, connector string, fn func(context.Context) error) error {
	tx, err := d.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin credential refresh: %w", err)
	}
	defer func() {
		cleanupContext, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		// pgx closes the connection if rollback fails, releasing its locks.
		if err := tx.Rollback(cleanupContext); err != nil && !errors.Is(err, pgx.ErrTxClosed) {
			slog.Error("rollback credential refresh", "error", err)
		}
	}()
	digest := sha256.Sum256([]byte("hex-credential-refresh\x00" + owner + "\x00" + connector))
	key := int64(binary.BigEndian.Uint64(digest[:8]))
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, key); err != nil {
		return fmt.Errorf("lock credential refresh: %w", err)
	}
	callbackContext := context.WithValue(ctx, credentialTransactionKey{}, credentialTransaction{database: d, tx: tx})
	callbackErr := fn(callbackContext)
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit credential refresh: %w", err)
	}
	return callbackErr
}

func (d *Database) DeleteCredential(ctx context.Context, owner, connector string) error {
	result, err := d.pool.Exec(ctx,
		`DELETE FROM hex_integration_credentials WHERE owner = $1 AND connector = $2`, owner, connector)
	if err != nil {
		return fmt.Errorf("delete credential: %w", err)
	}
	if result.RowsAffected() == 0 {
		return hex.ErrNotFound
	}
	return nil
}

// ListCredentials lists connections without their sealed tokens.
func (d *Database) ListCredentials(ctx context.Context, owner string) ([]hex.CredentialRecord, error) {
	rows, err := d.pool.Query(ctx, `
		SELECT owner, connector, ''::bytea, account, connected_at, last_used_at, generation, version
		FROM hex_integration_credentials
		WHERE $1 = '' OR owner = $1
		ORDER BY owner, connector`, owner)
	if err != nil {
		return nil, fmt.Errorf("list credentials: %w", err)
	}
	defer rows.Close()

	records := make([]hex.CredentialRecord, 0)
	for rows.Next() {
		record, err := scanCredential(rows)
		if err != nil {
			return nil, err
		}
		record.Sealed = nil
		records = append(records, record)
	}
	return records, rows.Err()
}

func (d *Database) DeleteCredentialsUnusedSince(ctx context.Context, cutoff time.Time) (int, error) {
	result, err := d.pool.Exec(ctx, `DELETE FROM hex_integration_credentials WHERE last_used_at < $1`, cutoff)
	if err != nil {
		return 0, fmt.Errorf("delete unused credentials: %w", err)
	}
	return int(result.RowsAffected()), nil
}

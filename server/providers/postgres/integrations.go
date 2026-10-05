package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	hex "github.com/crazycatviking/hex/server"
	"github.com/jackc/pgx/v5"
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

const credentialColumns = `owner, connector, sealed, account, connected_at, last_used_at`

func scanCredential(row pgx.Row) (hex.CredentialRecord, error) {
	var record hex.CredentialRecord
	err := row.Scan(&record.Owner, &record.Connector, &record.Sealed, &record.Account, &record.ConnectedAt, &record.LastUsedAt)
	record.ConnectedAt = record.ConnectedAt.UTC()
	record.LastUsedAt = record.LastUsedAt.UTC()
	return record, err
}

func (d *Database) GetCredential(ctx context.Context, owner, connector string) (hex.CredentialRecord, error) {
	row := d.pool.QueryRow(ctx,
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
		INSERT INTO hex_integration_credentials (owner, connector, sealed, account, connected_at, last_used_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, now())
		ON CONFLICT (owner, connector) DO UPDATE SET
			sealed = EXCLUDED.sealed, account = EXCLUDED.account, connected_at = EXCLUDED.connected_at,
			last_used_at = EXCLUDED.last_used_at, updated_at = now()`
	_, err := d.pool.Exec(ctx, statement, record.Owner, record.Connector, record.Sealed, record.Account,
		record.ConnectedAt, record.LastUsedAt)
	if err != nil {
		return fmt.Errorf("save credential: %w", err)
	}
	return nil
}

func (d *Database) TouchCredential(ctx context.Context, owner, connector string, usedAt time.Time) error {
	result, err := d.pool.Exec(ctx,
		`UPDATE hex_integration_credentials SET last_used_at = $3 WHERE owner = $1 AND connector = $2`,
		owner, connector, usedAt)
	if err != nil {
		return fmt.Errorf("record credential use: %w", err)
	}
	if result.RowsAffected() == 0 {
		return hex.ErrNotFound
	}
	return nil
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
		SELECT owner, connector, ''::bytea, account, connected_at, last_used_at
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

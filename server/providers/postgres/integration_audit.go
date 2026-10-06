package postgres

import (
	"context"
	"fmt"
	"strings"
	"time"

	hex "github.com/crazycatviking/hex/server"
)

func (d *Database) migrateIntegrationAudit(ctx context.Context) error {
	const statements = `
		CREATE TABLE IF NOT EXISTS hex_integration_audit (
			id text PRIMARY KEY,
			at timestamptz NOT NULL,
			site text NOT NULL,
			integration text NOT NULL,
			endpoint text NOT NULL,
			caller text NOT NULL,
			caller_name text NOT NULL DEFAULT '',
			input jsonb NOT NULL,
			records text[] NOT NULL DEFAULT '{}',
			cached boolean NOT NULL,
			failed boolean NOT NULL
		);
		CREATE INDEX IF NOT EXISTS hex_integration_audit_at ON hex_integration_audit (at);
		CREATE INDEX IF NOT EXISTS hex_integration_audit_site ON hex_integration_audit (site, at);
		CREATE INDEX IF NOT EXISTS hex_integration_audit_caller ON hex_integration_audit (caller, at);
		CREATE INDEX IF NOT EXISTS hex_integration_audit_endpoint ON hex_integration_audit (integration, endpoint, at);
		CREATE INDEX IF NOT EXISTS hex_integration_audit_records ON hex_integration_audit USING GIN (records)`
	if _, err := d.pool.Exec(ctx, statements); err != nil {
		return fmt.Errorf("create integration audit table: %w", err)
	}
	return nil
}

func (d *Database) RecordIntegrationAudit(ctx context.Context, record hex.IntegrationAuditRecord) error {
	const statement = `
		INSERT INTO hex_integration_audit (id, at, site, integration, endpoint, caller, caller_name, input, records, cached, failed)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)`
	records := record.Records
	if records == nil {
		records = []string{}
	}
	_, err := d.pool.Exec(ctx, statement, record.ID, record.At, record.Site, record.Integration, record.Endpoint,
		record.Caller, record.CallerName, []byte(record.Input), records, record.Cached, record.Failed)
	if err != nil {
		return fmt.Errorf("record integration audit: %w", err)
	}
	return nil
}

// auditConditions turns a filter into a WHERE clause and its arguments.
func auditConditions(filter hex.IntegrationAuditFilter) (string, []any) {
	var conditions []string
	var arguments []any
	add := func(condition string, value any) {
		arguments = append(arguments, value)
		conditions = append(conditions, fmt.Sprintf(condition, len(arguments)))
	}
	if !filter.Since.IsZero() {
		add("at >= $%d", filter.Since)
	}
	if !filter.Until.IsZero() {
		add("at < $%d", filter.Until)
	}
	if filter.Site != "" {
		add("site = $%d", filter.Site)
	}
	if filter.Caller != "" {
		add("caller = $%d", filter.Caller)
	}
	if filter.Endpoint != "" {
		integration, endpoint, _ := strings.Cut(filter.Endpoint, ".")
		add("integration = $%d", integration)
		if endpoint != "*" {
			add("endpoint = $%d", endpoint)
		}
	}
	if filter.Record != "" {
		add("records @> ARRAY[$%d::text]", filter.Record)
	}
	if len(conditions) == 0 {
		return "", nil
	}
	return " WHERE " + strings.Join(conditions, " AND "), arguments
}

func (d *Database) ListIntegrationAudit(ctx context.Context, filter hex.IntegrationAuditFilter) ([]hex.IntegrationAuditRecord, error) {
	where, arguments := auditConditions(filter)
	query := `SELECT id, at, site, integration, endpoint, caller, caller_name, input, records, cached, failed
		FROM hex_integration_audit` + where + ` ORDER BY at DESC, id DESC`
	if filter.Limit > 0 {
		arguments = append(arguments, filter.Limit)
		query += fmt.Sprintf(" LIMIT $%d", len(arguments))
	}
	rows, err := d.pool.Query(ctx, query, arguments...)
	if err != nil {
		return nil, fmt.Errorf("list integration audit: %w", err)
	}
	defer rows.Close()

	records := make([]hex.IntegrationAuditRecord, 0)
	for rows.Next() {
		var record hex.IntegrationAuditRecord
		var input []byte
		if err := rows.Scan(&record.ID, &record.At, &record.Site, &record.Integration, &record.Endpoint,
			&record.Caller, &record.CallerName, &input, &record.Records, &record.Cached, &record.Failed); err != nil {
			return nil, err
		}
		record.Input = input
		records = append(records, record)
	}
	return records, rows.Err()
}

func (d *Database) DeleteIntegrationAuditBefore(ctx context.Context, cutoff time.Time) (int, error) {
	result, err := d.pool.Exec(ctx, `DELETE FROM hex_integration_audit WHERE at < $1`, cutoff)
	if err != nil {
		return 0, fmt.Errorf("delete expired integration audit records: %w", err)
	}
	return int(result.RowsAffected()), nil
}

package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	hex "github.com/crazycatviking/hex/server"
)

func (d *Database) migrateIntegrations(ctx context.Context) error {
	const schema = `CREATE TABLE IF NOT EXISTS hex_tool_calls (
		id text PRIMARY KEY, user_id text NOT NULL, tool text NOT NULL, started_at timestamptz NOT NULL,
		lease_until timestamptz NOT NULL, counted boolean NOT NULL, pending boolean NOT NULL,
		output_bytes bigint NOT NULL, records bigint NOT NULL, audit jsonb NOT NULL);
		CREATE INDEX IF NOT EXISTS hex_tool_calls_user ON hex_tool_calls(user_id,started_at);
		CREATE INDEX IF NOT EXISTS hex_tool_calls_time ON hex_tool_calls(started_at);`
	if _, err := d.pool.Exec(ctx, schema); err != nil {
		return fmt.Errorf("migrate integration state: %w", err)
	}
	return nil
}

func (d *Database) ReserveToolCall(ctx context.Context, reservation hex.ToolCallReservation) error {
	tx, err := d.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer rollbackAnalytics(tx)
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, "hex-tools:"+reservation.Audit.UserID); err != nil {
		return err
	}
	var calls, bytes, records, active int64
	const usage = `SELECT count(*) FILTER (WHERE started_at >= $2),
		COALESCE(sum(output_bytes) FILTER (WHERE started_at >= $2),0)::bigint,
		COALESCE(sum(records) FILTER (WHERE started_at >= $2),0)::bigint,
		count(*) FILTER (WHERE pending AND lease_until > $3)
		FROM hex_tool_calls WHERE user_id=$1 AND counted AND (started_at >= $2 OR pending AND lease_until > $3)`
	if err := tx.QueryRow(ctx, usage, reservation.Audit.UserID, reservation.Audit.StartedAt.Add(-time.Hour), reservation.Audit.StartedAt).Scan(&calls, &bytes, &records, &active); err != nil {
		return err
	}
	budget := reservation.Budget
	if calls >= budget.CallsPerHour || bytes+reservation.OutputBytes > budget.OutputBytesPerHour || records+reservation.Records > budget.RecordsPerHour || active >= budget.ConcurrentCalls {
		return hex.ErrIntegrationBudget
	}
	audit, err := json.Marshal(reservation.Audit)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO hex_tool_calls (id,user_id,tool,started_at,lease_until,counted,pending,output_bytes,records,audit)
		VALUES ($1,$2,$3,$4,$5,true,true,$6,$7,$8)`, reservation.Audit.ID, reservation.Audit.UserID, reservation.Audit.Tool, reservation.Audit.StartedAt, reservation.Audit.LeaseUntil, reservation.OutputBytes, reservation.Records, audit)
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (d *Database) FinishToolCall(ctx context.Context, audit hex.ToolCallAudit) error {
	data, err := json.Marshal(audit)
	if err != nil {
		return err
	}
	result, err := d.pool.Exec(ctx, `UPDATE hex_tool_calls SET pending=false,output_bytes=$3,records=$4,audit=$5
		WHERE id=$1 AND user_id=$2 AND pending AND $3 >= 0 AND $3 <= output_bytes AND $4 >= 0 AND $4 <= records`, audit.ID, audit.UserID, audit.OutputBytes, audit.Records, data)
	if err != nil {
		return err
	}
	if result.RowsAffected() == 0 {
		return hex.ErrNotFound
	}
	return nil
}

func (d *Database) RecordToolAudit(ctx context.Context, audit hex.ToolCallAudit) error {
	data, err := json.Marshal(audit)
	if err != nil {
		return err
	}
	_, err = d.pool.Exec(ctx, `INSERT INTO hex_tool_calls (id,user_id,tool,started_at,lease_until,counted,pending,output_bytes,records,audit)
		VALUES ($1,$2,$3,$4,$4,false,false,0,0,$5) ON CONFLICT DO NOTHING`, audit.ID, audit.UserID, audit.Tool, audit.StartedAt, data)
	return err
}

func (d *Database) ListToolAudit(ctx context.Context, query hex.ToolAuditQuery) ([]hex.ToolCallAudit, error) {
	limit := query.Limit
	if limit < 1 || limit > 100 {
		limit = 50
	}
	var before *time.Time
	if !query.Before.IsZero() {
		before = &query.Before
	}
	rows, err := d.pool.Query(ctx, `SELECT audit FROM hex_tool_calls WHERE ($1='' OR user_id=$1) AND ($2='' OR tool=$2)
		AND ($3::timestamptz IS NULL OR started_at < $3) ORDER BY started_at DESC,id DESC LIMIT $4`, query.UserID, query.Tool, before, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []hex.ToolCallAudit{}
	for rows.Next() {
		var data []byte
		if err := rows.Scan(&data); err != nil {
			return nil, err
		}
		var audit hex.ToolCallAudit
		if err := json.Unmarshal(data, &audit); err != nil {
			return nil, err
		}
		result = append(result, audit)
	}
	return result, rows.Err()
}

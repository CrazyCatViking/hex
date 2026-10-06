package postgres

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	hex "github.com/crazycatviking/hex/server"
	"github.com/jackc/pgx/v5"
)

const maxRunsPerAutomation = 50

func (d *Database) migrateAutomations(ctx context.Context) error {
	const statements = `
		CREATE TABLE IF NOT EXISTS hex_automations (
			site text NOT NULL,
			name text NOT NULL,
			entry jsonb NOT NULL,
			next_run timestamptz,
			PRIMARY KEY (site, name)
		);
		CREATE INDEX IF NOT EXISTS hex_automations_due ON hex_automations (next_run) WHERE next_run IS NOT NULL;
		CREATE TABLE IF NOT EXISTS hex_automation_runs (
			id text PRIMARY KEY,
			site text NOT NULL,
			name text NOT NULL,
			started_at timestamptz NOT NULL,
			run jsonb NOT NULL
		);
		CREATE INDEX IF NOT EXISTS hex_automation_runs_history ON hex_automation_runs (site, name, started_at DESC)`
	if _, err := d.pool.Exec(ctx, statements); err != nil {
		return fmt.Errorf("create automation tables: %w", err)
	}
	return nil
}

func nullableTime(value time.Time) *time.Time {
	if value.IsZero() {
		return nil
	}
	return &value
}

// ReplaceSiteAutomations swaps the site's whole set while holding the same
// site lock as claims, preserving current run times for unchanged schedules.
func (d *Database) ReplaceSiteAutomations(ctx context.Context, site string, automations []hex.ScheduledAutomation) error {
	transaction, err := d.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return fmt.Errorf("begin automation update: %w", err)
	}
	defer func() {
		if err := transaction.Rollback(ctx); err != nil && !errors.Is(err, pgx.ErrTxClosed) {
			slog.Error("roll back automation update", "error", err)
		}
	}()

	if err := lockAutomationSite(ctx, transaction, site); err != nil {
		return err
	}
	rows, err := transaction.Query(ctx, `SELECT entry, next_run FROM hex_automations WHERE site = $1 FOR UPDATE`, site)
	if err != nil {
		return fmt.Errorf("read automations for replacement: %w", err)
	}
	stored, err := scanAutomations(rows)
	if err != nil {
		return err
	}
	current := make(map[string]hex.ScheduledAutomation, len(stored))
	for _, entry := range stored {
		current[entry.Automation.Name] = entry
	}
	if _, err := transaction.Exec(ctx, `DELETE FROM hex_automations WHERE site = $1`, site); err != nil {
		return fmt.Errorf("remove automations: %w", err)
	}
	for _, automation := range automations {
		automation.Site = site
		automation.Revision = rand.Text()
		if previous, exists := current[automation.Automation.Name]; exists && hex.SameAutomationSchedule(previous.Automation, automation.Automation) {
			automation.NextRun = previous.NextRun
		}
		entry, err := json.Marshal(automation)
		if err != nil {
			return err
		}
		const insert = `INSERT INTO hex_automations (site, name, entry, next_run) VALUES ($1, $2, $3, $4)`
		if _, err := transaction.Exec(ctx, insert, site, automation.Automation.Name, entry, nullableTime(automation.NextRun)); err != nil {
			return fmt.Errorf("save automation %s: %w", automation.Automation.Name, err)
		}
	}
	if err := transaction.Commit(ctx); err != nil {
		return fmt.Errorf("commit automation update: %w", err)
	}
	return nil
}

func (d *Database) queryAutomations(ctx context.Context, query string, arguments ...any) ([]hex.ScheduledAutomation, error) {
	rows, err := d.pool.Query(ctx, query, arguments...)
	if err != nil {
		return nil, fmt.Errorf("list automations: %w", err)
	}
	return scanAutomations(rows)
}

func scanAutomations(rows pgx.Rows) ([]hex.ScheduledAutomation, error) {
	defer rows.Close()

	automations := make([]hex.ScheduledAutomation, 0)
	for rows.Next() {
		var data []byte
		var next *time.Time
		if err := rows.Scan(&data, &next); err != nil {
			return nil, err
		}
		var automation hex.ScheduledAutomation
		if err := json.Unmarshal(data, &automation); err != nil {
			return nil, fmt.Errorf("decode automation: %w", err)
		}
		// The column is authoritative: claims update it without rewriting
		// the entry.
		automation.NextRun = time.Time{}
		if next != nil {
			automation.NextRun = next.UTC()
		}
		automations = append(automations, automation)
	}
	return automations, rows.Err()
}

func (d *Database) ListSiteAutomations(ctx context.Context, site string) ([]hex.ScheduledAutomation, error) {
	return d.queryAutomations(ctx, `SELECT entry, next_run FROM hex_automations WHERE site = $1 ORDER BY name`, site)
}

func (d *Database) DueAutomations(ctx context.Context, now time.Time, limit int) ([]hex.ScheduledAutomation, error) {
	return d.queryAutomations(ctx,
		`SELECT entry, next_run FROM hex_automations WHERE next_run <= $1 ORDER BY next_run LIMIT $2`, now, limit)
}

func (d *Database) ClaimAutomation(ctx context.Context, site, name, revision string, expected, next time.Time) (bool, error) {
	if expected.IsZero() {
		return false, nil
	}
	transaction, err := d.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return false, fmt.Errorf("begin automation claim: %w", err)
	}
	defer func() {
		if err := transaction.Rollback(ctx); err != nil && !errors.Is(err, pgx.ErrTxClosed) {
			slog.Error("roll back automation claim", "error", err)
		}
	}()
	if err := lockAutomationSite(ctx, transaction, site); err != nil {
		return false, err
	}
	result, err := transaction.Exec(ctx,
		`UPDATE hex_automations SET next_run = $5
		 WHERE site = $1 AND name = $2 AND next_run = $3 AND COALESCE(entry->>'revision', '') = $4`,
		site, name, expected, revision, nullableTime(next))
	if err != nil {
		return false, fmt.Errorf("claim automation: %w", err)
	}
	if err := transaction.Commit(ctx); err != nil {
		return false, fmt.Errorf("commit automation claim: %w", err)
	}
	return result.RowsAffected() == 1, nil
}

func lockAutomationSite(ctx context.Context, transaction pgx.Tx, site string) error {
	// The site lock covers whole-set replacement and absent names; row locks
	// alone cannot serialize deletion/recreation or an initially empty site.
	if _, err := transaction.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('hex-automations:' || $1, 0))`, site); err != nil {
		return fmt.Errorf("lock automations for %s: %w", site, err)
	}
	return nil
}

func (d *Database) RecordAutomationRun(ctx context.Context, run hex.AutomationRun) error {
	data, err := json.Marshal(run)
	if err != nil {
		return err
	}
	const upsert = `
		INSERT INTO hex_automation_runs (id, site, name, started_at, run) VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (id) DO UPDATE SET run = EXCLUDED.run`
	if _, err := d.pool.Exec(ctx, upsert, run.ID, run.Site, run.Automation, run.StartedAt, data); err != nil {
		return fmt.Errorf("record automation run: %w", err)
	}
	const prune = `
		DELETE FROM hex_automation_runs WHERE site = $1 AND name = $2 AND id NOT IN (
			SELECT id FROM hex_automation_runs WHERE site = $1 AND name = $2 ORDER BY started_at DESC LIMIT $3)`
	if _, err := d.pool.Exec(ctx, prune, run.Site, run.Automation, maxRunsPerAutomation); err != nil {
		return fmt.Errorf("prune automation runs: %w", err)
	}
	return nil
}

func (d *Database) ListAutomationRuns(ctx context.Context, site, name string, limit int) ([]hex.AutomationRun, error) {
	rows, err := d.pool.Query(ctx,
		`SELECT run FROM hex_automation_runs WHERE site = $1 AND name = $2 ORDER BY started_at DESC LIMIT $3`,
		site, name, limit)
	if err != nil {
		return nil, fmt.Errorf("list automation runs: %w", err)
	}
	defer rows.Close()

	runs := make([]hex.AutomationRun, 0)
	for rows.Next() {
		var data []byte
		if err := rows.Scan(&data); err != nil {
			return nil, err
		}
		var run hex.AutomationRun
		if err := json.Unmarshal(data, &run); err != nil {
			return nil, fmt.Errorf("decode automation run: %w", err)
		}
		runs = append(runs, run)
	}
	return runs, rows.Err()
}

func (d *Database) GetAutomationRun(ctx context.Context, site, id string) (hex.AutomationRun, error) {
	var data []byte
	err := d.pool.QueryRow(ctx, `SELECT run FROM hex_automation_runs WHERE site = $1 AND id = $2`, site, id).Scan(&data)
	if errors.Is(err, pgx.ErrNoRows) {
		return hex.AutomationRun{}, hex.ErrNotFound
	}
	if err != nil {
		return hex.AutomationRun{}, fmt.Errorf("read automation run: %w", err)
	}
	var run hex.AutomationRun
	if err := json.Unmarshal(data, &run); err != nil {
		return hex.AutomationRun{}, fmt.Errorf("decode automation run: %w", err)
	}
	return run, nil
}

package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	hex "github.com/crazycatviking/hex/server"
)

func (d *Database) migrateAIUsage(ctx context.Context) error {
	const statements = `
		CREATE TABLE IF NOT EXISTS hex_ai_usage (
			id text PRIMARY KEY,
			at timestamptz NOT NULL,
			site text NOT NULL,
			caller text NOT NULL,
			caller_name text NOT NULL DEFAULT '',
			model text NOT NULL,
			input_tokens bigint NOT NULL,
			cached_input_tokens bigint NOT NULL,
			cache_write_tokens bigint NOT NULL,
			output_tokens bigint NOT NULL,
			estimated boolean NOT NULL,
			cost_micros bigint NOT NULL,
			priced boolean NOT NULL
		);
		CREATE INDEX IF NOT EXISTS hex_ai_usage_at ON hex_ai_usage (at);
		CREATE INDEX IF NOT EXISTS hex_ai_usage_site ON hex_ai_usage (site, at);
		CREATE INDEX IF NOT EXISTS hex_ai_usage_caller ON hex_ai_usage (caller, at);
		CREATE TABLE IF NOT EXISTS hex_ai_budgets (
			scope text NOT NULL,
			subject text NOT NULL,
			budget jsonb NOT NULL,
			PRIMARY KEY (scope, subject)
		)`
	if _, err := d.pool.Exec(ctx, statements); err != nil {
		return fmt.Errorf("create AI usage tables: %w", err)
	}
	return nil
}

func (d *Database) RecordAIUsage(ctx context.Context, record hex.AIUsageRecord) error {
	const statement = `
		INSERT INTO hex_ai_usage (id, at, site, caller, caller_name, model, input_tokens, cached_input_tokens,
			cache_write_tokens, output_tokens, estimated, cost_micros, priced)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)`
	usage := record.Usage
	_, err := d.pool.Exec(ctx, statement, record.ID, record.At, record.Site, record.Caller, record.CallerName, record.Model,
		usage.InputTokens, usage.CachedInputTokens, usage.CacheWriteTokens, usage.OutputTokens, usage.Estimated,
		record.CostMicros, record.Priced)
	if err != nil {
		return fmt.Errorf("record AI usage: %w", err)
	}
	return nil
}

// usageConditions turns a filter into a WHERE clause and its arguments.
func usageConditions(filter hex.AIUsageFilter) (string, []any) {
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
	if len(conditions) == 0 {
		return "", nil
	}
	return " WHERE " + strings.Join(conditions, " AND "), arguments
}

func (d *Database) SumAICost(ctx context.Context, filter hex.AIUsageFilter) (int64, error) {
	where, arguments := usageConditions(filter)
	var total int64
	if err := d.pool.QueryRow(ctx, `SELECT COALESCE(SUM(cost_micros), 0) FROM hex_ai_usage`+where, arguments...).Scan(&total); err != nil {
		return 0, fmt.Errorf("sum AI cost: %w", err)
	}
	return total, nil
}

var usageGroupColumns = map[string]string{
	hex.GroupBySite:   "site",
	hex.GroupByCaller: "caller",
	hex.GroupByModel:  "model",
	hex.GroupByDay:    "to_char(at AT TIME ZONE 'UTC', 'YYYY-MM-DD')",
}

func (d *Database) AIUsageTotals(ctx context.Context, filter hex.AIUsageFilter, groupBy string) ([]hex.AIUsageTotal, error) {
	column, valid := usageGroupColumns[groupBy]
	if !valid {
		return nil, fmt.Errorf("unknown AI usage grouping %q", groupBy)
	}
	order := "SUM(cost_micros) DESC, key"
	if groupBy == hex.GroupByDay {
		order = "key"
	}
	where, arguments := usageConditions(filter)
	query := `SELECT ` + column + ` AS key, (array_agg(caller_name ORDER BY at DESC))[1], COUNT(*),
			SUM(input_tokens), SUM(cached_input_tokens), SUM(cache_write_tokens), SUM(output_tokens),
			SUM(cost_micros), COUNT(*) FILTER (WHERE estimated)
		FROM hex_ai_usage` + where + ` GROUP BY key ORDER BY ` + order
	rows, err := d.pool.Query(ctx, query, arguments...)
	if err != nil {
		return nil, fmt.Errorf("total AI usage: %w", err)
	}
	defer rows.Close()

	totals := make([]hex.AIUsageTotal, 0)
	for rows.Next() {
		var total hex.AIUsageTotal
		if err := rows.Scan(&total.Key, &total.Name, &total.Calls, &total.InputTokens, &total.CachedInputTokens,
			&total.CacheWriteTokens, &total.OutputTokens, &total.CostMicros, &total.Estimated); err != nil {
			return nil, err
		}
		if groupBy != hex.GroupByCaller {
			total.Name = ""
		}
		totals = append(totals, total)
	}
	return totals, rows.Err()
}

func (d *Database) ListAIBudgets(ctx context.Context) ([]hex.AIBudget, error) {
	rows, err := d.pool.Query(ctx, `SELECT budget FROM hex_ai_budgets ORDER BY scope, subject`)
	if err != nil {
		return nil, fmt.Errorf("list AI budgets: %w", err)
	}
	defer rows.Close()

	budgets := make([]hex.AIBudget, 0)
	for rows.Next() {
		var data []byte
		if err := rows.Scan(&data); err != nil {
			return nil, err
		}
		var budget hex.AIBudget
		if err := json.Unmarshal(data, &budget); err != nil {
			return nil, fmt.Errorf("decode AI budget: %w", err)
		}
		budgets = append(budgets, budget)
	}
	return budgets, rows.Err()
}

func (d *Database) PutAIBudget(ctx context.Context, budget hex.AIBudget) error {
	data, err := json.Marshal(budget)
	if err != nil {
		return err
	}
	const statement = `
		INSERT INTO hex_ai_budgets (scope, subject, budget) VALUES ($1, $2, $3)
		ON CONFLICT (scope, subject) DO UPDATE SET budget = EXCLUDED.budget`
	if _, err := d.pool.Exec(ctx, statement, budget.Scope, budget.Subject, data); err != nil {
		return fmt.Errorf("save AI budget: %w", err)
	}
	return nil
}

func (d *Database) DeleteAIBudget(ctx context.Context, scope, subject string) error {
	result, err := d.pool.Exec(ctx, `DELETE FROM hex_ai_budgets WHERE scope = $1 AND subject = $2`, scope, subject)
	if err != nil {
		return fmt.Errorf("delete AI budget: %w", err)
	}
	if result.RowsAffected() == 0 {
		return hex.ErrNotFound
	}
	return nil
}

package postgres

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestConcurrentColdCapabilityMigrations(t *testing.T) {
	platformTables := []string{"hex_people", "hex_site_policies", "hex_integration_approvals", "hex_integration_credentials", "hex_automations", "hex_automation_runs", "hex_ai_usage", "hex_ai_usage_lock", "hex_ai_reservations", "hex_ai_budgets", "hex_integration_audit"}
	analyticsTables := []string{"hex_analytics_people", "hex_analytics_events", "hex_analytics_traffic", "hex_analytics_receipts", "hex_analytics_sessions"}
	allTables := append(slices.Concat(platformTables, analyticsTables), "hex_documents")
	cases := []struct {
		name    string
		migrate func(*Database, context.Context) error
		tables  []string
	}{
		{"documents", (*Database).MigrateDocuments, []string{"hex_documents"}},
		{"people", (*Database).MigratePeople, []string{"hex_people"}},
		{"analytics", (*Database).MigrateAnalytics, analyticsTables},
		{"access", (*Database).MigrateAccess, []string{"hex_site_policies"}},
		{"integrations", (*Database).MigrateIntegrations, []string{"hex_integration_approvals", "hex_integration_credentials"}},
		{"automations", (*Database).MigrateAutomations, []string{"hex_automations", "hex_automation_runs"}},
		{"AI usage", (*Database).MigrateAIUsage, []string{"hex_ai_usage", "hex_ai_usage_lock", "hex_ai_reservations", "hex_ai_budgets"}},
		{"integration audit", (*Database).MigrateIntegrationAudit, []string{"hex_integration_audit"}},
		{"platform", (*Database).MigratePlatform, platformTables},
		{"full", (*Database).Migrate, allTables},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			database := isolatedAnalyticsDatabase(t)
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			config := database.pool.Config()
			// Exercise multiple independent instances and one-connection pools.
			// Holding the lock on a pooled connection would deadlock this case.
			config.MaxConns = 1
			stores := make([]*Database, 2)
			for i := range stores {
				pool, err := pgxpool.NewWithConfig(ctx, config.Copy())
				if err != nil {
					t.Fatal(err)
				}
				stores[i] = &Database{pool: pool}
				defer stores[i].Close()
			}
			start := make(chan struct{})
			failures := make(chan error, 8)
			var workers sync.WaitGroup
			for i := range 8 {
				workers.Go(func() {
					<-start
					if err := test.migrate(stores[i%len(stores)], ctx); err != nil {
						failures <- fmt.Errorf("worker %d: %w", i, err)
					}
				})
			}
			close(start)
			workers.Wait()
			close(failures)
			for err := range failures {
				t.Error(err)
			}
			if t.Failed() {
				return
			}
			actual := migrationTables(t, database)
			want := slices.Clone(test.tables)
			slices.Sort(want)
			if !reflect.DeepEqual(actual, want) {
				t.Fatalf("migration expanded its scope:\ngot %v\nwant %v", actual, want)
			}
		})
	}
}

func migrationTables(t *testing.T, database *Database) []string {
	t.Helper()
	rows, err := database.pool.Query(context.Background(), `SELECT tablename FROM pg_tables WHERE schemaname=current_schema() ORDER BY tablename`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var tables []string
	for rows.Next() {
		var table string
		if err := rows.Scan(&table); err != nil {
			t.Fatal(err)
		}
		tables = append(tables, table)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return tables
}

func TestMigrationLockCancellationAndFailureRelease(t *testing.T) {
	database := isolatedAnalyticsDatabase(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	lock, err := pgx.ConnectConfig(ctx, database.pool.Config().ConnConfig.Copy())
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close(context.Background())
	if _, err := lock.Exec(ctx, `SELECT pg_advisory_lock(hashtextextended('hex_schema_migration', 0))`); err != nil {
		t.Fatal(err)
	}
	wait, cancelWait := context.WithTimeout(ctx, 50*time.Millisecond)
	err = database.MigratePeople(wait)
	cancelWait()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("waiting migration must honor its deadline: %v", err)
	}
	if _, err := lock.Exec(ctx, `SELECT pg_advisory_unlock(hashtextextended('hex_schema_migration', 0))`); err != nil {
		t.Fatal(err)
	}
	failure := errors.New("migration failed")
	if err := database.migrate(ctx, func(context.Context) error { return failure }); !errors.Is(err, failure) {
		t.Fatalf("lost migration error: %v", err)
	}
	if err := database.MigratePeople(ctx); err != nil {
		t.Fatalf("failed/cancelled migration left the lock held: %v", err)
	}
}

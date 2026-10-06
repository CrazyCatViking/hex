package postgres

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	hex "github.com/crazycatviking/hex/server"
	"github.com/jackc/pgx/v5/pgxpool"
)

// These tests exercise the AI migration independently of other capabilities'
// schema setup, which may run concurrently in integration tests.
func openAIUsageDatabase(t *testing.T) (*Database, context.Context) {
	t.Helper()
	connection := os.Getenv("HEX_TEST_POSTGRES_URL")
	if connection == "" {
		t.Skip("set HEX_TEST_POSTGRES_URL to run against PostgreSQL")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	database, err := New(ctx, connection)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(database.Close)
	if err := database.migrateAIUsage(ctx); err != nil {
		t.Fatal(err)
	}
	return database, ctx
}

func TestAIReservationsAcrossPostgresPools(t *testing.T) {
	database, ctx := openAIUsageDatabase(t)
	replica, err := New(ctx, os.Getenv("HEX_TEST_POSTGRES_URL"))
	if err != nil {
		t.Fatal(err)
	}
	defer replica.Close()
	site := "ai-reservations-" + rand.Text()
	at := time.Now().UTC().Truncate(time.Microsecond)
	checks := []hex.AIBudgetCheck{{Filter: hex.AIUsageFilter{Site: site}, LimitMicros: 100}}
	var mu sync.Mutex
	var admitted []hex.AIUsageReservation
	var wait sync.WaitGroup
	for index := 0; index < 30; index++ {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			store := database
			if index%2 == 0 {
				store = replica
			}
			hold := hex.AIUsageReservation{ID: rand.Text(), At: at, Site: site, Caller: "user:a", CostMicros: 30}
			if err := store.ReserveAIUsage(ctx, hold, checks); err == nil {
				mu.Lock()
				admitted = append(admitted, hold)
				mu.Unlock()
			} else {
				var exceeded *hex.AIBudgetExceededError
				if !errors.As(err, &exceeded) {
					t.Errorf("unexpected admission error: %v", err)
				}
			}
		}(index)
	}
	wait.Wait()
	if len(admitted) != 4 {
		t.Fatalf("cross-pool admission overcommitted: %d calls", len(admitted))
	}
	// Future monthly filters still count unresolved holds from this month.
	future := []hex.AIBudgetCheck{{Filter: hex.AIUsageFilter{Site: site, Since: at.AddDate(0, 1, 0)}, LimitMicros: 100}}
	blocked := hex.AIUsageReservation{ID: rand.Text(), At: at, Site: site, CostMicros: 30}
	if err := replica.ReserveAIUsage(ctx, blocked, future); err == nil {
		t.Fatal("month rollover released outstanding spending")
	}
	hold := admitted[0]
	if err := replica.ReserveAIUsage(ctx, hold, checks); err != nil {
		t.Fatalf("retry of the same reservation failed: %v", err)
	}
	record := hex.AIUsageRecord{ID: hold.ID, At: hold.At, Site: hold.Site, Caller: hold.Caller, Model: "general", CostMicros: 15, Priced: true}
	// Force failure AFTER the reservation deletion, proving transaction
	// rollback retains it rather than losing already-spent funds.
	function := "hex_ai_fail_" + rand.Text()
	trigger := "hex_ai_trigger_" + rand.Text()
	statement := fmt.Sprintf(`CREATE FUNCTION %q() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN IF NEW.id = '%s' THEN RAISE EXCEPTION 'simulated accounting failure'; END IF; RETURN NEW; END $$;
		CREATE TRIGGER %q BEFORE INSERT ON hex_ai_usage FOR EACH ROW EXECUTE FUNCTION %q()`, function, hold.ID, trigger, function)
	if _, err := database.pool.Exec(ctx, statement); err != nil {
		t.Fatal(err)
	}
	drop := fmt.Sprintf(`DROP TRIGGER IF EXISTS %q ON hex_ai_usage; DROP FUNCTION IF EXISTS %q()`, trigger, function)
	t.Cleanup(func() {
		if _, err := database.pool.Exec(ctx, drop); err != nil {
			t.Error(err)
		}
	})
	if err := replica.SettleAIUsage(ctx, record); err == nil {
		t.Fatal("injected insertion failure did not fail settlement")
	}
	var count int
	if err := database.pool.QueryRow(ctx, `SELECT COUNT(*) FROM hex_ai_reservations WHERE id=$1`, hold.ID).Scan(&count); err != nil || count != 1 {
		t.Fatalf("failed settlement lost its durable reservation: %d %v", count, err)
	}
	if err := replica.ReserveAIUsage(ctx, blocked, checks); err == nil {
		t.Fatal("failed settlement admitted further spending")
	}
	if _, err := database.pool.Exec(ctx, drop); err != nil {
		t.Fatal(err)
	}
	// Simultaneous retries from distinct pools must charge exactly once.
	for index := 0; index < 10; index++ {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			store := database
			if index%2 == 0 {
				store = replica
			}
			if err := store.SettleAIUsage(ctx, record); err != nil {
				t.Error(err)
			}
		}(index)
	}
	wait.Wait()
	totals, err := database.AIUsageTotals(ctx, hex.AIUsageFilter{Site: site}, hex.GroupBySite)
	if err != nil || len(totals) != 1 || totals[0].Calls != 1 || totals[0].CostMicros != 15 {
		t.Fatalf("settlement was not idempotent: %+v %v", totals, err)
	}
	if err := database.pool.QueryRow(ctx, `SELECT COUNT(*) FROM hex_ai_reservations WHERE id=$1`, hold.ID).Scan(&count); err != nil || count != 0 {
		t.Fatalf("successful settlement retained its hold: %d %v", count, err)
	}
}

func TestAICallerNamePostgresTemporalTiesAndImports(t *testing.T) {
	database, ctx := openAIUsageDatabase(t)
	site := "ai-caller-name-" + rand.Text()
	at := time.Now().UTC().Truncate(time.Microsecond)
	prefix := rand.Text()
	for _, record := range []hex.AIUsageRecord{
		{ID: prefix + "z", At: at, Site: site, Caller: "user:a", CallerName: "Newest tie winner"},
		{ID: prefix + "a", At: at, Site: site, Caller: "user:a", CallerName: "Tie loser"},
		{ID: prefix + "zz", At: at.Add(-time.Hour), Site: site, Caller: "user:a", CallerName: "Late import of older name"},
	} {
		if err := database.RecordAIUsage(ctx, record); err != nil {
			t.Fatal(err)
		}
		if err := database.RecordAIUsage(ctx, record); err != nil {
			t.Fatal(err)
		}
	}
	totals, err := database.AIUsageTotals(ctx, hex.AIUsageFilter{Site: site}, hex.GroupByCaller)
	if err != nil || len(totals) != 1 || totals[0].Name != "Newest tie winner" || totals[0].Calls != 3 {
		t.Fatalf("incorrect timestamp/tie order or import idempotency: %+v %v", totals, err)
	}
}

func TestAIReservationMetadataAndLegacyHoldsRecoverableAfterReopen(t *testing.T) {
	database, ctx := openAIUsageDatabase(t)
	site := "recover-reservation-" + rand.Text()
	at := time.Now().UTC().AddDate(0, -1, 0).Truncate(time.Microsecond)
	held := hex.AIUsageReservation{
		ID: rand.Text(), At: at, Site: site, Caller: "user:person", CallerName: "Person", Model: "general", CostMicros: 50,
		Price: &hex.AIPrice{Input: 1, CachedInput: 0.1, Output: 2}, ContextTokens: 20, MaxOutputTokens: 10,
		EstimatedUsage: hex.AIUsage{InputTokens: 5, Estimated: true},
	}
	if err := database.ReserveAIUsage(ctx, held, nil); err != nil {
		t.Fatal(err)
	}
	legacyID := rand.Text()
	// Simulate a hold from the previous migration, without model metadata.
	if _, err := database.pool.Exec(ctx, `INSERT INTO hex_ai_reservations(id,at,site,caller,cost_micros) VALUES($1,$2,$3,$4,25)`,
		legacyID, at.Add(-time.Hour), site, "user:legacy"); err != nil {
		t.Fatal(err)
	}
	if err := database.migrateAIUsage(ctx); err != nil {
		t.Fatal(err)
	}
	reopened, err := New(ctx, os.Getenv("HEX_TEST_POSTGRES_URL"))
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	listed, err := reopened.ListAIReservations(ctx, hex.AIUsageFilter{Site: site})
	if err != nil || len(listed) != 2 || listed[0].ID != legacyID || listed[0].CostMicros != 25 || !listed[0].EstimatedUsage.Estimated {
		t.Fatalf("legacy hold disappeared or was represented as known zero: %+v %v", listed, err)
	}
	if listed[1].Model != held.Model || listed[1].CallerName != held.CallerName || listed[1].Price.Input != 1 ||
		listed[1].ContextTokens != 20 || listed[1].MaxOutputTokens != 10 || listed[1].EstimatedUsage.InputTokens != 5 {
		t.Fatalf("reservation metadata did not survive reopen: %+v", listed[1])
	}
	spend, err := reopened.AIBudgetSpend(ctx, hex.AIUsageFilter{Site: site, Since: time.Now().UTC()})
	if err != nil || spend.ReservedMicros != 75 || spend.Reservations != 2 || spend.SettledMicros != 0 {
		t.Fatalf("old pending holds are missing from current budget: %+v %v", spend, err)
	}
	record := hex.AIUsageRecord{ID: listed[1].ID, At: listed[1].At, Site: listed[1].Site, Caller: listed[1].Caller,
		CallerName: listed[1].CallerName, Model: listed[1].Model, Usage: listed[1].EstimatedUsage, CostMicros: listed[1].CostMicros, Priced: true}
	if err := reopened.SettleAIUsage(ctx, record); err != nil {
		t.Fatal(err)
	}
	if err := database.SettleAIUsage(ctx, record); err != nil {
		t.Fatal(err)
	}
	spend, err = reopened.AIBudgetSpend(ctx, hex.AIUsageFilter{Site: site})
	if err != nil || spend.ReservedMicros != 25 || spend.Reservations != 1 || spend.SettledMicros != 50 {
		t.Fatalf("reconciliation lost or double-counted spending: %+v %v", spend, err)
	}
}

func TestAIUsageMigrationConcurrentFirstBoot(t *testing.T) {
	database, ctx := openAIUsageDatabase(t)
	schema := "ai_migration_" + rand.Text()
	if _, err := database.pool.Exec(ctx, fmt.Sprintf(`CREATE SCHEMA %q`, schema)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := database.pool.Exec(ctx, fmt.Sprintf(`DROP SCHEMA %q CASCADE`, schema)); err != nil {
			t.Error(err)
		}
	})
	var stores []*Database
	for index := 0; index < 2; index++ {
		config, err := pgxpool.ParseConfig(os.Getenv("HEX_TEST_POSTGRES_URL"))
		if err != nil {
			t.Fatal(err)
		}
		config.ConnConfig.RuntimeParams["search_path"] = `"` + schema + `"`
		pool, err := pgxpool.NewWithConfig(ctx, config)
		if err != nil {
			t.Fatal(err)
		}
		defer pool.Close()
		stores = append(stores, &Database{pool: pool})
	}
	var wait sync.WaitGroup
	for index := 0; index < 10; index++ {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			if err := stores[index%2].migrateAIUsage(ctx); err != nil {
				t.Error(err)
			}
		}(index)
	}
	wait.Wait()
	hold := hex.AIUsageReservation{ID: rand.Text(), At: time.Now().UTC().Truncate(time.Microsecond), Site: "demo", Caller: "user:a", CostMicros: 10}
	if err := stores[0].ReserveAIUsage(ctx, hold, nil); err != nil {
		t.Fatal(err)
	}
	if err := stores[1].SettleAIUsage(ctx, hex.AIUsageRecord{ID: hold.ID, At: hold.At, Site: hold.Site, Caller: hold.Caller, CostMicros: 5}); err != nil {
		t.Fatal(err)
	}
	spend, err := stores[0].AIBudgetSpend(ctx, hex.AIUsageFilter{})
	if err != nil || spend.SettledMicros != 5 || spend.ReservedMicros != 0 || spend.Reservations != 0 {
		t.Fatalf("AI migration left incomplete accounting tables: %+v %v", spend, err)
	}
}

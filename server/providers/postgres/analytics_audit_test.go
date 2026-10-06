package postgres

import (
	"context"
	"crypto/rand"
	"fmt"
	"os"
	"reflect"
	"testing"
	"time"

	hex "github.com/crazycatviking/hex/server"
	"github.com/crazycatviking/hex/server/providers/memory"
	"github.com/jackc/pgx/v5/pgxpool"
)

func isolatedAnalyticsDatabase(t *testing.T) *Database {
	t.Helper()
	connection := os.Getenv("HEX_TEST_POSTGRES_URL")
	if connection == "" {
		t.Skip("set HEX_TEST_POSTGRES_URL")
	}
	ctx := context.Background()
	admin, err := New(ctx, connection)
	if err != nil {
		t.Fatal(err)
	}
	schema := "analytics_audit_" + rand.Text()
	if _, err := admin.pool.Exec(ctx, `CREATE SCHEMA "`+schema+`"`); err != nil {
		admin.Close()
		t.Fatal(err)
	}
	config, err := pgxpool.ParseConfig(connection)
	if err != nil {
		t.Fatal(err)
	}
	config.ConnConfig.RuntimeParams["search_path"] = `"` + schema + `"`
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	database := &Database{pool: pool}
	t.Cleanup(func() {
		database.Close()
		if _, err := admin.pool.Exec(context.Background(), `DROP SCHEMA "`+schema+`" CASCADE`); err != nil {
			t.Error(err)
		}
		admin.Close()
	})
	return database
}

func TestAnalyticsScopedMigration(t *testing.T) {
	database := isolatedAnalyticsDatabase(t)
	ctx := context.Background()
	for range 2 {
		if err := database.MigrateAnalytics(ctx); err != nil {
			t.Fatal(err)
		}
	}
	rows, err := database.pool.Query(ctx, `SELECT tablename FROM pg_tables WHERE schemaname=current_schema()`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	tables := map[string]bool{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		tables[name] = true
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if len(tables) != 5 {
		t.Fatalf("analytics migration created unrelated tables: %+v", tables)
	}
	for _, name := range []string{"hex_analytics_people", "hex_analytics_events", "hex_analytics_traffic", "hex_analytics_receipts", "hex_analytics_sessions"} {
		if !tables[name] {
			t.Fatalf("missing %s", name)
		}
	}
	if err := database.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	var documents *string
	if err := database.pool.QueryRow(ctx, `SELECT to_regclass('hex_documents')::text`).Scan(&documents); err != nil || documents == nil {
		t.Fatalf("full convenience migration did not prepare documents: %v", err)
	}
}

func TestAnalyticsProvidersHaveMatchingSummaryAndVisitorSemantics(t *testing.T) {
	database := isolatedAnalyticsDatabase(t)
	ctx := context.Background()
	if err := database.MigrateAnalytics(ctx); err != nil {
		t.Fatal(err)
	}
	inMemory := memory.NewAnalytics()
	stores := []hex.AnalyticsStore{database, inMemory}
	day := time.Now().UTC().Truncate(24*time.Hour).AddDate(0, 0, -2)
	at := day.Add(12 * time.Hour)
	for _, store := range stores {
		observations := []struct {
			person hex.Person
			at     time.Time
		}{
			{hex.Person{ID: "alice", Name: "Old name"}, at},
			{hex.Person{ID: "alice", Name: "Newest name", Email: "alice@example.test"}, at.Add(time.Hour)},
			{hex.Person{ID: "alice", Name: "Stale name"}, at.Add(-time.Hour)},
			{hex.Person{ID: "alice", Name: "Alice", Email: "alice@example.test"}, at.Add(time.Hour)},
			{hex.Person{ID: "bob", Name: "Bob", Email: "bob@example.test"}, at},
		}
		for _, observation := range observations {
			if err := store.ObservePerson(ctx, observation.person, observation.at); err != nil {
				t.Fatal(err)
			}
		}
		events := []hex.SiteEvent{}
		for i := range 60 {
			events = append(events, hex.SiteEvent{ID: fmt.Sprint(i), At: at, Site: "demo", Kind: "published", Actor: &hex.Person{ID: "alice"}})
		}
		if err := store.RecordSiteEvents(ctx, events); err != nil {
			t.Fatal(err)
		}
		traffic := []hex.TrafficEvent{}
		for i, user := range []string{"alice", "bob", "unknown", "", "api-only"} {
			for j := range i + 1 {
				traffic = append(traffic, hex.TrafficEvent{ID: fmt.Sprintf("%d-%d", i, j), At: at.Add(time.Duration(j) * time.Hour), Site: "demo", UserID: user, PageView: user != "api-only", Status: 200, Bytes: 10, DurationMillis: 5})
			}
		}
		traffic = append(traffic, hex.TrafficEvent{ID: "tomorrow", At: at.AddDate(0, 0, 1), Site: "demo", UserID: "alice", PageView: true, Status: 304})
		traffic = append(traffic, hex.TrafficEvent{ID: "other", At: at, Site: "other", UserID: "bob", PageView: true, Status: 200})
		if err := store.RecordTraffic(ctx, traffic); err != nil {
			t.Fatal(err)
		}
	}
	for _, query := range []hex.AnalyticsQuery{
		{From: day, Until: day.AddDate(0, 0, 2)},
		{From: day, Until: day.AddDate(0, 0, 2), Site: "demo"},
		{From: day, Until: day.AddDate(0, 0, 2), User: "alice"},
		{From: day, Until: day.AddDate(0, 0, 2), Site: "missing"},
	} {
		actual, err := database.QueryAnalytics(ctx, query)
		if err != nil {
			t.Fatal(err)
		}
		want, err := inMemory.QueryAnalytics(ctx, query)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(actual, want) {
			t.Fatalf("full reports differ for %+v:\npostgres %+v\nmemory %+v", query, actual, want)
		}
		actual, err = database.QueryAnalyticsSummary(ctx, query)
		if err != nil {
			t.Fatal(err)
		}
		want, err = inMemory.QueryAnalyticsSummary(ctx, query)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(actual, want) {
			t.Fatalf("summary reports differ for %+v:\npostgres %+v\nmemory %+v", query, actual, want)
		}
	}
	query := hex.AnalyticsQuery{From: day, Until: day.AddDate(0, 0, 2), Site: "demo"}
	for _, sort := range []string{"views", "visits", "recent", "name"} {
		for _, search := range []string{"", "ALICE", "unknown", "%"} {
			for _, number := range []int{1, 2, 99} {
				visitors := hex.AnalyticsVisitorsQuery{AnalyticsQuery: query, Search: search, Sort: sort, Page: number, Limit: 1}
				actual, err := database.QueryAnalyticsVisitors(ctx, visitors)
				if err != nil {
					t.Fatal(err)
				}
				want, err := inMemory.QueryAnalyticsVisitors(ctx, visitors)
				if err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(actual, want) {
					t.Fatalf("visitor pages differ for %+v:\npostgres %+v\nmemory %+v", visitors, actual, want)
				}
			}
		}
	}
	actual, err := database.SiteTraffic(ctx, []string{"demo", "missing"})
	if err != nil {
		t.Fatal(err)
	}
	want, err := inMemory.SiteTraffic(ctx, []string{"demo", "missing"})
	if err != nil || !reflect.DeepEqual(actual, want) {
		t.Fatalf("lifetime summaries differ: %+v %+v %v", actual, want, err)
	}
	// External writes become visible after the bounded cache lifetime expires.
	if _, err := database.pool.Exec(ctx, `UPDATE hex_analytics_traffic SET page_views=page_views+1 WHERE site='demo' AND user_id='alice' AND day=$1`, day); err != nil {
		t.Fatal(err)
	}
	cached, err := database.SiteTraffic(ctx, []string{"demo"})
	if err != nil || cached[0].PageViews != actual[0].PageViews {
		t.Fatal("lifetime cache was not reused")
	}
	entry := database.trafficCache["demo"]
	entry.expires = time.Now().Add(-time.Second)
	database.trafficCache["demo"] = entry
	fresh, err := database.SiteTraffic(ctx, []string{"demo"})
	if err != nil || fresh[0].PageViews != actual[0].PageViews+1 {
		t.Fatalf("expired lifetime cache did not refresh: %+v %v", fresh, err)
	}
}

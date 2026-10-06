package postgres

import (
	"context"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	hex "github.com/crazycatviking/hex/server"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type analyticsQueryKey struct{}

// Commit a separate writer immediately after the first report query finishes.
// This deterministically exercises traffic/identity changes between statements.
type analyticsMutationTracer struct {
	prefix string
	mutate func()
	once   sync.Once
}

func (tracer *analyticsMutationTracer) TraceQueryStart(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	return context.WithValue(ctx, analyticsQueryKey{}, data.SQL)
}

func (tracer *analyticsMutationTracer) TraceQueryEnd(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryEndData) {
	query, _ := ctx.Value(analyticsQueryKey{}).(string)
	if data.Err == nil && strings.HasPrefix(query, tracer.prefix) {
		tracer.once.Do(tracer.mutate)
	}
}

func TestAnalyticsReadsUseConsistentSnapshots(t *testing.T) {
	for _, kind := range []string{"full", "summary", "visitors"} {
		t.Run(kind, func(t *testing.T) {
			writer := isolatedAnalyticsDatabase(t)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			if err := writer.MigrateAnalytics(ctx); err != nil {
				t.Fatal(err)
			}
			day := time.Now().UTC().Truncate(24 * time.Hour)
			at := day.Add(time.Hour)
			if err := writer.ObservePerson(ctx, hex.Person{ID: "alice", Name: "Original label"}, at); err != nil {
				t.Fatal(err)
			}
			if err := writer.RecordTraffic(ctx, []hex.TrafficEvent{{ID: "initial", At: at, Site: "demo", UserID: "alice", PageView: true, Status: 200}}); err != nil {
				t.Fatal(err)
			}
			query := hex.AnalyticsQuery{From: day, Until: day.AddDate(0, 0, 1)}
			visitors := hex.AnalyticsVisitorsQuery{AnalyticsQuery: query, Search: "Original", Limit: 1, Page: 1}
			visitors.Site = "demo"
			var want any
			var err error
			switch kind {
			case "full":
				want, err = writer.QueryAnalytics(ctx, query)
			case "summary":
				want, err = writer.QueryAnalyticsSummary(ctx, query)
			case "visitors":
				want, err = writer.QueryAnalyticsVisitors(ctx, visitors)
			}
			if err != nil {
				t.Fatal(err)
			}
			mutated := false
			tracer := &analyticsMutationTracer{prefix: "SELECT COALESCE(sum(requests)"}
			if kind == "visitors" {
				tracer.prefix = "WITH visitors AS"
			}
			tracer.mutate = func() {
				mutated = true
				for _, person := range []hex.Person{{ID: "alice", Name: "New label"}, {ID: "bob", Name: "Original Bob"}} {
					if err := writer.ObservePerson(ctx, person, at.Add(time.Minute)); err != nil {
						t.Fatal(err)
					}
				}
				if err := writer.RecordTraffic(ctx, []hex.TrafficEvent{{ID: "concurrent", At: at.Add(time.Minute), Site: "demo", UserID: "bob", PageView: true, Status: 200}}); err != nil {
					t.Fatal(err)
				}
				if err := writer.RecordSiteEvents(ctx, []hex.SiteEvent{{ID: "concurrent-publication", At: at, Site: "demo", Kind: "published"}}); err != nil {
					t.Fatal(err)
				}
			}
			config := writer.pool.Config()
			config.ConnConfig.Tracer = tracer
			pool, err := pgxpool.NewWithConfig(ctx, config)
			if err != nil {
				t.Fatal(err)
			}
			reader := &Database{pool: pool}
			defer reader.Close()
			var actual any
			switch kind {
			case "full":
				actual, err = reader.QueryAnalytics(ctx, query)
			case "summary":
				actual, err = reader.QueryAnalyticsSummary(ctx, query)
			case "visitors":
				actual, err = reader.QueryAnalyticsVisitors(ctx, visitors)
			}
			if err != nil {
				t.Fatal(err)
			}
			if !mutated {
				t.Fatal("concurrent writer was not exercised")
			}
			if !reflect.DeepEqual(actual, want) {
				t.Fatalf("report mixed traffic/identity snapshots:\ngot %+v\nwant %+v", actual, want)
			}
			updated, err := writer.QueryAnalytics(ctx, query)
			if err != nil || updated.Traffic.PageViews != 2 || updated.KnownUsers != 2 || updated.Publications != 1 {
				t.Fatalf("writer's changes were not committed: %+v %v", updated, err)
			}
		})
	}
}

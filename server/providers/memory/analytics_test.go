package memory

import (
	"context"
	"testing"
	"time"

	hex "github.com/crazycatviking/hex/server"
)

func TestLifetimeSiteTrafficAndVisitorLabels(t *testing.T) {
	store := NewAnalytics()
	ctx := context.Background()
	day := time.Now().UTC().Truncate(24 * time.Hour)
	if err := store.ObservePerson(ctx, hex.Person{ID: "alice", Name: "Alice Visitor", Email: "alice@example.test"}, day); err != nil {
		t.Fatal(err)
	}
	// Daily aggregates are retained beyond the short request-receipt window.
	for _, entry := range []struct {
		site, user string
		day        time.Time
		views      int64
	}{
		{"demo", "alice", day.AddDate(0, 0, -500), 5},
		{"demo", "alice", day, 2},
		{"demo", "bob", day, 1},
		{"demo", "", day, 1},
		{"other", "private", day, 100},
	} {
		key := bucketKey{visitorKey{entry.site, entry.user}, entry.day}
		store.buckets[key] = hex.TrafficBucket{Day: entry.day, Site: entry.site, UserID: entry.user, TrafficTotals: hex.TrafficTotals{Requests: entry.views, PageViews: entry.views, LastVisited: entry.day}}
	}
	rows, err := store.SiteTraffic(ctx, []string{"demo"})
	if err != nil || len(rows) != 1 || rows[0].PageViews != 9 || rows[0].Visitors != 2 || rows[0].Person != nil {
		t.Fatalf("incorrect scoped lifetime totals: %+v %v", rows, err)
	}
	empty, err := store.SiteTraffic(ctx, nil)
	if err != nil || len(empty) != 0 {
		t.Fatal("empty site selection must not query all sites")
	}
	report, err := store.QueryAnalytics(ctx, hex.AnalyticsQuery{From: day, Until: day.AddDate(0, 0, 1), Site: "demo"})
	if err != nil {
		t.Fatal(err)
	}
	if report.Traffic.PageViews != 4 || len(report.People) != 0 || len(report.Users) != 2 || report.Users[0].Person == nil || report.Users[0].Person.Name != "Alice Visitor" {
		t.Fatalf("date/site scoped visitor labels incorrect: %+v", report)
	}
}

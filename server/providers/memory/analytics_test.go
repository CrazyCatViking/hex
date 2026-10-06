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
		if store.lifetime[entry.site] == nil {
			store.lifetime[entry.site] = make(map[string]hex.TrafficBucket)
		}
		total := store.lifetime[entry.site][entry.user]
		total.Site, total.UserID = entry.site, entry.user
		total.Requests += entry.views
		total.PageViews += entry.views
		if entry.day.After(total.LastVisited) {
			total.LastVisited = entry.day
		}
		store.lifetime[entry.site][entry.user] = total
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

func TestNarrowAnalyticsAndNewestIdentityLabels(t *testing.T) {
	ctx := context.Background()
	store := NewAnalytics()
	day := time.Now().UTC().Truncate(24*time.Hour).AddDate(0, 0, -1)
	at := day.Add(12 * time.Hour)
	for _, observation := range []struct {
		person hex.Person
		at     time.Time
	}{
		{hex.Person{ID: "alice", Name: "Old Alice"}, at},
		{hex.Person{ID: "alice", Name: "Newest Alice"}, at.Add(time.Hour)},
		{hex.Person{ID: "alice", Name: "Stale Alice"}, at},
		{hex.Person{ID: "alice", Name: "Alice", Email: "alice@example.test"}, at.Add(time.Hour)},
		{hex.Person{ID: "bob", Name: "Bob"}, at},
	} {
		if err := store.ObservePerson(ctx, observation.person, observation.at); err != nil {
			t.Fatal(err)
		}
	}
	events := []hex.TrafficEvent{
		{ID: "alice", At: at, Site: "demo", UserID: "alice", PageView: true, Status: 200},
		{ID: "alice-2", At: at.AddDate(0, 0, 1), Site: "demo", UserID: "alice", PageView: true, Status: 304},
		{ID: "bob", At: at, Site: "demo", UserID: "bob", PageView: true, Status: 200},
		{ID: "anonymous", At: at, Site: "demo", PageView: true, Status: 200},
		{ID: "api-only", At: at, Site: "demo", UserID: "api-only", Status: 200},
	}
	for range 2 {
		if err := store.RecordTraffic(ctx, events); err != nil {
			t.Fatal(err)
		}
	}
	query := hex.AnalyticsQuery{From: day, Until: day.AddDate(0, 0, 2), Site: "demo"}
	summary, err := store.QueryAnalyticsSummary(ctx, query)
	if err != nil {
		t.Fatal(err)
	}
	if summary.Traffic.Requests != 5 || summary.Traffic.PageViews != 4 || summary.Traffic.Visitors != 2 || summary.Traffic.Visits != 3 || len(summary.Days) != 2 || len(summary.Users) != 0 || len(summary.People) != 0 || len(summary.Events) != 0 || len(summary.Sites) != 0 {
		t.Fatalf("incorrect narrow summary: %+v", summary)
	}
	page, err := store.QueryAnalyticsVisitors(ctx, hex.AnalyticsVisitorsQuery{AnalyticsQuery: query, Limit: 1, Page: 99, Sort: "name"})
	if err != nil {
		t.Fatal(err)
	}
	if page.Total != 2 || page.Page != 2 || len(page.Rows) != 1 || page.Rows[0].Key != "bob" || page.AnonymousPageViews != 1 || page.PeakPageViews != 2 {
		t.Fatalf("incorrect bounded page: %+v", page)
	}
	page, err = store.QueryAnalyticsVisitors(ctx, hex.AnalyticsVisitorsQuery{AnalyticsQuery: query, Limit: 10, Search: "ALICE@"})
	if err != nil {
		t.Fatal(err)
	}
	if page.Total != 1 || page.Rows[0].Person.Name != "Alice" || page.Rows[0].PageViews != 2 || page.AnonymousPageViews != 1 {
		t.Fatalf("search/newest identity incorrect: %+v", page)
	}
	lifetime, err := store.SiteTraffic(ctx, []string{"demo"})
	if err != nil || len(lifetime) != 1 || lifetime[0].PageViews != 4 || lifetime[0].Visitors != 2 {
		t.Fatalf("persistent lifetime aggregate incorrect: %+v %v", lifetime, err)
	}
}

package hex_test

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	hex "github.com/crazycatviking/hex/server"
)

func TestPublishersSeeNamedVisitorsAndSiteScopedVisits(t *testing.T) {
	server, _, analytics := analyticsFixture(t)
	owner := principalHeaders("owner")
	viewer := principalHeaders("viewer")
	publishSite(t, server, owner, "demo", map[string]string{"index.html": "app"}, "")
	publishSite(t, server, viewer, "other", map[string]string{"index.html": "other"}, "")
	day := time.Now().UTC().Truncate(24*time.Hour).AddDate(0, 0, -1)
	ctx := context.Background()
	for _, person := range []hex.Person{
		{ID: "alice", Name: "Alice <script>bad()</script>", Email: "alice@example.test"},
		{ID: "bob", Name: "Bob Visitor", Email: "bob@example.test"},
		{ID: "elsewhere", Name: "Other Site Only", Email: "elsewhere@example.test"},
		{ID: "asset", Name: "Asset Only", Email: "asset@example.test"},
	} {
		if err := analytics.ObservePerson(ctx, person, day); err != nil {
			t.Fatal(err)
		}
	}
	events := []hex.TrafficEvent{
		{ID: "alice-1", At: day.Add(time.Hour), Site: "demo", UserID: "alice", PageView: true, Status: 200},
		{ID: "alice-2", At: day.Add(time.Hour + 10*time.Minute), Site: "demo", UserID: "alice", PageView: true, Status: 200},
		{ID: "alice-3", At: day.Add(2 * time.Hour), Site: "demo", UserID: "alice", PageView: true, Status: 200},
		{ID: "bob-1", At: day.Add(3 * time.Hour), Site: "demo", UserID: "bob", PageView: true, Status: 200},
		{ID: "bob-2", At: day.Add(24 * time.Hour), Site: "demo", UserID: "bob", PageView: true, Status: 200},
		{ID: "anonymous", At: day.Add(4 * time.Hour), Site: "demo", PageView: true, Status: 200},
		{ID: "asset", At: day.Add(5 * time.Hour), Site: "demo", UserID: "asset", Status: 200},
		{ID: "elsewhere", At: day.Add(6 * time.Hour), Site: "other", UserID: "elsewhere", PageView: true, Status: 200},
	}
	if err := analytics.RecordTraffic(ctx, events); err != nil {
		t.Fatal(err)
	}
	base := fmt.Sprintf("from=%s&until=%s", day.Format(time.DateOnly), day.AddDate(0, 0, 1).Format(time.DateOnly))
	for _, path := range []string{"/manage/demo?tab=analytics&", "/api/hex/manage/sites/demo/analytics?"} {
		body := requestAs(t, server, owner, "GET", path+base+"&site=other&user=elsewhere", nil, 200).Body.String()
		for _, text := range []string{"Who visited", "Alice &lt;script&gt;", "alice@example.test", "Bob Visitor", "bob@example.test", "all time", "could not be"} {
			if !strings.Contains(body, text) {
				t.Fatalf("publisher report missing %q: %s", text, body)
			}
		}
		for _, text := range []string{"Other Site Only", "elsewhere@example.test", "Asset Only", "<script>bad()</script>"} {
			if strings.Contains(body, text) {
				t.Fatalf("publisher report leaked %q", text)
			}
		}
		requestAs(t, server, viewer, "GET", path+base, nil, 403)
		requestAs(t, server, nil, "GET", path+base, nil, 401)
	}
	filtered := requestAs(t, server, owner, "GET", "/api/hex/manage/sites/demo/analytics?"+base+"&visitor-q=bob@example.test&visitor-sort=recent", nil, 200).Body.String()
	if !strings.Contains(filtered, "Bob Visitor") || strings.Contains(filtered, "alice@example.test") {
		t.Fatal("visitor search did not filter the table")
	}
	oneDay := requestAs(t, server, owner, "GET", fmt.Sprintf("/api/hex/manage/sites/demo/analytics?from=%s&until=%s", day.Format(time.DateOnly), day.Format(time.DateOnly)), nil, 200).Body.String()
	if strings.Contains(oneDay, `datetime="`+day.AddDate(0, 0, 1).Format(time.RFC3339)+`"`) {
		t.Fatal("visitor last visit was taken outside the selected range")
	}
	requestAs(t, server, owner, "GET", "/api/hex/manage/sites/demo/analytics?visitor-page=bad", nil, 400)
	requestAs(t, server, owner, "GET", "/manage/demo?tab=analytics&from=bad", nil, 400)
	requestAs(t, server, owner, "GET", "http://demo.example.com/api/hex/manage/sites/demo/analytics", nil, 403)
}

func TestPublisherVisitorPaginationPreservesFilters(t *testing.T) {
	server, _, analytics := analyticsFixture(t)
	owner := principalHeaders("owner")
	publishSite(t, server, owner, "demo", map[string]string{"index.html": "app"}, "")
	at := time.Now().UTC().Add(-time.Hour)
	events := []hex.TrafficEvent{}
	for index := range 28 {
		id := fmt.Sprintf("person-%02d", index)
		if err := analytics.ObservePerson(context.Background(), hex.Person{ID: id, Name: fmt.Sprintf("Visitor %02d", index)}, at); err != nil {
			t.Fatal(err)
		}
		events = append(events, hex.TrafficEvent{ID: id, At: at, Site: "demo", UserID: id, PageView: true, Status: 200})
	}
	if err := analytics.RecordTraffic(context.Background(), events); err != nil {
		t.Fatal(err)
	}
	first := requestAs(t, server, owner, "GET", "/manage/demo?tab=analytics&visitor-sort=name&visitor-q=Visitor", nil, 200).Body.String()
	if !strings.Contains(first, "Visitor 24") || strings.Contains(first, "Visitor 25") || !strings.Contains(first, "Next visitors") {
		t.Fatal("first visitor page is incorrect")
	}
	second := requestAs(t, server, owner, "GET", "/manage/demo?tab=analytics&visitor-sort=name&visitor-q=Visitor&visitor-page=2", nil, 200).Body.String()
	if !strings.Contains(second, "Visitor 25") || strings.Contains(second, "Visitor 24") || !strings.Contains(second, "Previous visitors") || !strings.Contains(second, "visitor-sort=name") || !strings.Contains(second, "visitor-q=Visitor") {
		t.Fatal("visitor pagination lost filtering or ordering")
	}
}

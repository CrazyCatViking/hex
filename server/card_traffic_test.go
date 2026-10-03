package hex_test

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	hex "github.com/crazycatviking/hex/server"
)

func TestCardsShowTrafficAndSortFilterPopularity(t *testing.T) {
	server, _, analytics := analyticsFixture(t)
	owner := principalHeaders("owner")
	viewer := principalHeaders("viewer")
	for _, name := range []string{"alpha", "beta", "empty"} {
		publishSite(t, server, owner, name, map[string]string{"index.html": "app"}, "")
	}
	publishSite(t, server, owner, "secret", map[string]string{"index.html": "app"}, `{"viewers":["user:owner"]}`)
	day := time.Now().UTC().Truncate(24*time.Hour).AddDate(0, 0, -1)
	events := []hex.TrafficEvent{
		{ID: "a1", At: day.Add(time.Hour), Site: "alpha", UserID: "repeat-visitor", PageView: true, Status: 200},
		{ID: "a2", At: day.Add(2 * time.Hour), Site: "alpha", UserID: "repeat-visitor", PageView: true, Status: 200},
		{ID: "a3", At: day.Add(3 * time.Hour), Site: "alpha", UserID: "repeat-visitor", PageView: true, Status: 200},
		{ID: "b1", At: day.Add(4 * time.Hour), Site: "beta", UserID: "visitor-one", PageView: true, Status: 200},
		{ID: "b2", At: day.Add(5 * time.Hour), Site: "beta", UserID: "visitor-two", PageView: true, Status: 200},
		{ID: "asset", At: day.Add(6 * time.Hour), Site: "empty", UserID: "asset-only", Status: 200},
		{ID: "private", At: day.Add(7 * time.Hour), Site: "secret", UserID: "private-person", PageView: true, Status: 200},
	}
	if err := analytics.RecordTraffic(context.Background(), events); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		query  string
		first  string
		absent string
	}{
		{"sort=popular", "alpha", "secret"},
		{"sort=visitors", "beta", "secret"},
		{"sort=visited", "beta", "secret"},
		{"sort=name", "alpha", "secret"},
		{"sort=popular&traffic=visited", "alpha", "empty"},
		{"sort=popular&traffic=unvisited", "empty", "alpha"},
		{"sort=popular&search=beta", "beta", "alpha"},
	} {
		t.Run(test.query, func(t *testing.T) {
			body := requestAs(t, server, viewer, "GET", "/api/hex/catalog?"+test.query, nil, 200).Body.String()
			want := fmt.Sprintf(`href="http://%s.example.com/"`, test.first)
			if !strings.Contains(body, want) || strings.Contains(body, fmt.Sprintf(`href="http://%s.example.com/"`, test.absent)) {
				t.Fatalf("wrong traffic filter: %s", body)
			}
			first := strings.Index(body, `class="site-link"`)
			if first < 0 || !strings.HasPrefix(body[first:], `class="site-link" `+want) {
				t.Fatalf("wrong popularity order: %s", body)
			}
			for _, id := range []string{"repeat-visitor", "visitor-one", "visitor-two", "asset-only", "private-person"} {
				if strings.Contains(body, id) {
					t.Fatalf("public cards leaked visitor %s", id)
				}
			}
			if !strings.Contains(body, "all time") {
				t.Fatal("card counts need a clear time scope")
			}
		})
	}
	root := requestAs(t, server, viewer, "GET", "/?sort=popular", nil, 200).Body.String()
	if !strings.Contains(root, "Most popular (page views)") || !strings.Contains(root, `name="traffic"`) || !strings.Contains(root, "3 page views") {
		t.Fatal("browsing controls or counts missing")
	}
	mine := requestAs(t, server, owner, "GET", "/manage?sort=visitors&traffic=visited", nil, 200).Body.String()
	if !strings.Contains(mine, `href="/manage/beta?tab=analytics"`) || strings.Contains(mine, `href="/manage/empty"`) {
		t.Fatal("publisher listing lacks filtered traffic/analytics access")
	}
}

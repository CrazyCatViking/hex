package hex_test

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	hex "github.com/crazycatviking/hex/server"
	"github.com/crazycatviking/hex/server/providers/easyauth"
	"github.com/crazycatviking/hex/server/providers/local"
	"github.com/crazycatviking/hex/server/providers/memory"
)

func analyticsFixture(t *testing.T) (*hex.Server, *local.Store, *memory.Analytics) {
	t.Helper()
	store, err := local.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Error(err)
		}
	})
	analytics := memory.NewAnalytics()
	return hex.New(hex.Config{
		Sites: store, Publisher: store, Identity: easyauth.Resolver{}, Access: memory.NewAccessStore(),
		People: memory.NewPeopleStore(), Analytics: analytics, AdminGroups: []string{"admins"}, SiteBaseURL: "http://example.com",
	}), store, analytics
}

func TestAdminAnalyticsInventoryHistoryAndAuthorization(t *testing.T) {
	server, store, analytics := analyticsFixture(t)
	alex := signedIn(t, "alex", "Same Name", "alex@example.test")
	bea := signedIn(t, "bea", "Same Name", "bea@example.test")
	admin := principalHeaders("admin", "admins")
	publishSite(t, server, alex, "hidden", map[string]string{"index.html": "app"}, "")
	publishSite(t, server, alex, "hidden", map[string]string{"index.html": "updated"}, "")
	var metadata hex.SiteMetadata
	reader, err := store.ReadSiteFile(context.Background(), "hidden", ".hex-site.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.NewDecoder(reader).Decode(&metadata); err != nil {
		t.Fatal(err)
	}
	reader.Close()
	hidden := false
	metadata.Discoverable = &hidden
	encoded, err := json.Marshal(metadata)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.WriteSiteFile(context.Background(), "hidden", ".hex-site.json", int64(len(encoded)), strings.NewReader(string(encoded))); err != nil {
		t.Fatal(err)
	}
	artifact := requestAs(t, server, bea, "POST", "/api/hex/artifacts", []byte(`{"title":"Shared file"}`), 201)
	var created struct{ Name string }
	if err := json.Unmarshal(artifact.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	publishSite(t, server, bea, created.Name, map[string]string{"index.html": "file"}, "")
	for _, path := range []string{"/admin", "/admin/sites", "/admin/users", "/api/hex/admin/analytics"} {
		requestAs(t, server, nil, "GET", path, nil, http.StatusUnauthorized)
		requestAs(t, server, alex, "GET", path, nil, http.StatusForbidden)
		requestAs(t, server, admin, "GET", path, nil, http.StatusOK)
	}
	result := requestAs(t, server, admin, "GET", "/api/hex/admin/analytics", nil, 200)
	var response struct {
		hex.AnalyticsReport
		Inventory struct{ Sites, Apps, Artifacts, Creators int }
	}
	if err := json.Unmarshal(result.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.Inventory.Sites != 2 || response.Inventory.Apps != 1 || response.Inventory.Artifacts != 1 || response.Inventory.Creators != 2 || response.Created != 2 || response.Publications != 3 {
		t.Fatalf("wrong inventory/history: %+v", response)
	}
	csvResponse := requestAs(t, server, admin, "GET", "/api/hex/admin/analytics.csv", nil, 200)
	rows, err := csv.NewReader(strings.NewReader(csvResponse.Body.String())).ReadAll()
	if err != nil || len(rows) != 31 || rows[0][0] != "date_utc" || !strings.Contains(csvResponse.Header().Get("Content-Disposition"), "attachment") {
		t.Fatalf("bad daily CSV: %v %v", rows, err)
	}
	requestAs(t, server, alex, "GET", "/api/hex/admin/analytics.csv", nil, 403)
	for _, path := range []string{"/admin", "/admin/sites", "/admin/users", "/admin/sites/hidden", "/admin/users?user=alex"} {
		page := requestAs(t, server, admin, "GET", path, nil, 200)
		if !strings.Contains(page.Body.String(), "Page views") {
			t.Fatalf("analytics missing from %s: %s", path, page.Body.String())
		}
	}
	requestAs(t, server, admin, "GET", "/admin?from=bad", nil, 400)
	requestAs(t, server, admin, "GET", "/admin?from=2020-01-01", nil, 400)
	requestAs(t, server, admin, "GET", "/admin/sites?page=bad", nil, 400)
	r := httptest.NewRequest("GET", "/admin", nil)
	r.Host = "hidden.example.com"
	r.Header = admin.Clone()
	w := httptest.NewRecorder()
	server.ServeHTTP(w, r)
	if w.Code != 404 {
		t.Fatalf("admin page served on a site origin: %d", w.Code)
	}
	requestAs(t, server, alex, "GET", "/api/hex/admin/analytics?site=hidden", nil, 403)
	formRequest(t, server, alex, "POST", "/api/hex/manage/sites/hidden/unpublish", url.Values{"confirm": {"hidden"}}, 204)
	requestAs(t, server, bea, "DELETE", "/api/hex/sites/"+created.Name, nil, 204)
	query := hex.AnalyticsQuery{From: time.Now().UTC().Truncate(24 * time.Hour), Until: time.Now().UTC().Truncate(24*time.Hour).AddDate(0, 0, 1)}
	report, err := analytics.QueryAnalytics(context.Background(), query)
	if err != nil {
		t.Fatal(err)
	}
	if report.Created != 2 || report.Publications != 3 || report.Unpublished != 2 || len(report.Events) != 7 {
		t.Fatalf("unpublishing lost history: %+v", report)
	}
	requestAs(t, server, admin, "GET", "/admin/sites/hidden", nil, 200)
}

type retryAnalytics struct {
	*memory.Analytics
	attempts atomic.Int32
}

func (s *retryAnalytics) RecordTraffic(ctx context.Context, events []hex.TrafficEvent) error {
	if s.attempts.Add(1) <= 2 {
		return errors.New("temporary analytics storage failure")
	}
	return s.Analytics.RecordTraffic(ctx, events)
}

func TestTrafficCollectorRetriesDeduplicatesAndCloses(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	store := &retryAnalytics{Analytics: memory.NewAnalytics()}
	if _, err := hex.StartTrafficCollector(ctx, "0.0.0.0:0", store); err == nil {
		t.Fatal("collector must not listen on public interfaces")
	}
	collector, err := hex.StartTrafficCollector(ctx, "127.0.0.1:0", store)
	if err != nil {
		t.Fatal(err)
	}
	defer collector.Close()
	conn, err := net.Dial("udp", collector.Address())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	packet := fmt.Sprintf(`{"version":1,"id":"%s","time":"%.3f","site":"demo","user":"dmlzaXRvcg","method":"GET","status":"200","bytes":"5","duration":"0.001","destination":"document","contentType":"text/html","api":"0"}`, strings.Repeat("a", 32), float64(time.Now().UnixMilli())/1000)
	for _, data := range []string{packet, packet, "malformed"} {
		if _, err := conn.Write([]byte(data)); err != nil {
			t.Fatal(err)
		}
	}
	for collector.Status().Recorded != 2 {
		select {
		case <-ctx.Done():
			t.Fatalf("collector did not recover: %+v", collector.Status())
		case <-time.After(20 * time.Millisecond):
		}
	}
	status := collector.Status()
	if status.WriteFailures != 2 || status.Rejected != 1 || status.Received != 3 || status.Dropped != 0 {
		t.Fatalf("wrong collection status: %+v", status)
	}
	day := time.Now().UTC().Truncate(24 * time.Hour)
	report, err := store.QueryAnalytics(ctx, hex.AnalyticsQuery{From: day, Until: day.AddDate(0, 0, 1)})
	if err != nil || report.Traffic.Requests != 1 || report.Traffic.PageViews != 1 {
		t.Fatalf("retried request counted twice: %+v %v", report, err)
	}
	if err := collector.Close(); err != nil {
		t.Fatal(err)
	}
	if collector.Status().Running {
		t.Fatal("collector still running after close")
	}
}

func TestAnalyticsTrafficDistinctVisitorsSessionsAndOwnerScope(t *testing.T) {
	server, _, store := analyticsFixture(t)
	alex := principalHeaders("alex")
	admin := principalHeaders("admin", "admins")
	publishSite(t, server, alex, "demo", map[string]string{"index.html": "app"}, "")
	day := time.Now().UTC().Truncate(24*time.Hour).AddDate(0, 0, -1)
	query := hex.AnalyticsQuery{From: day, Until: day.AddDate(0, 0, 2)}
	events := []hex.TrafficEvent{
		{ID: "one", At: day.Add(23*time.Hour + 50*time.Minute), Site: "demo", UserID: "visitor-a", PageView: true, Status: 200, Bytes: 10},
		{ID: "two", At: day.Add(24*time.Hour + 5*time.Minute), Site: "demo", UserID: "visitor-a", PageView: true, Status: 304},
		{ID: "three", At: day.Add(24*time.Hour + 40*time.Minute), Site: "demo", UserID: "visitor-a", PageView: true, Status: 200, Bytes: 20},
		{ID: "four", At: day.Add(24*time.Hour + 45*time.Minute), Site: "demo", UserID: "visitor-b", PageView: true, Status: 200},
		{ID: "five", At: day.Add(24*time.Hour + 46*time.Minute), Site: "other", UserID: "visitor-a", PageView: true, Status: 200},
		{ID: "six", At: day.Add(24*time.Hour + 47*time.Minute), Site: "demo", Status: 404, Bytes: 5},
	}
	if err := store.RecordTraffic(context.Background(), events); err != nil {
		t.Fatal(err)
	}
	if err := store.RecordTraffic(context.Background(), events); err != nil {
		t.Fatal(err)
	}
	report, err := store.QueryAnalytics(context.Background(), query)
	if err != nil {
		t.Fatal(err)
	}
	if report.Traffic.Requests != 6 || report.Traffic.PageViews != 5 || report.Traffic.Visitors != 2 || report.Traffic.Visits != 4 || report.Traffic.Errors != 1 || report.Traffic.Bytes != 35 {
		t.Fatalf("incorrect traffic aggregation: %+v", report.Traffic)
	}
	query.Site = "demo"
	report, err = store.QueryAnalytics(context.Background(), query)
	if err != nil || report.Traffic.Visits != 3 || report.Traffic.PageViews != 4 {
		t.Fatalf("site filtering/session boundary failed: %+v %v", report, err)
	}
	query.User = "visitor-b"
	report, err = store.QueryAnalytics(context.Background(), query)
	if err != nil || report.Traffic.Requests != 1 || report.Traffic.Visitors != 1 {
		t.Fatalf("user filtering failed: %+v %v", report, err)
	}
	path := fmt.Sprintf("/api/hex/manage/sites/demo/analytics?from=%s&until=%s&user=visitor-a&site=other", day.Format(time.DateOnly), day.AddDate(0, 0, 1).Format(time.DateOnly))
	body := requestAs(t, server, alex, "GET", path, nil, 200).Body.String()
	if !strings.Contains(body, "visitor-a") || !strings.Contains(body, "visitor-b") || strings.Contains(body, "/manage/other") {
		t.Fatalf("owner analytics must show visitors for their site only: %s", body)
	}
	requestAs(t, server, principalHeaders("visitor-a"), "GET", path, nil, 403)
	requestAs(t, server, alex, "GET", "/manage/demo?tab=analytics", nil, 200)
	requestAs(t, server, admin, "GET", "/admin/sites/demo", nil, 200)
}

package postgres

import (
	"context"
	"fmt"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	hex "github.com/crazycatviking/hex/server"
	"github.com/crazycatviking/hex/server/providers/memory"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestPostgresAdminGrantsAreDurableFreshAndAuditedAcrossPools(t *testing.T) {
	database := isolatedAnalyticsDatabase(t)
	ctx := context.Background()
	if err := database.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	config := database.pool.Config()
	config.MaxConns = 1
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	replica := &Database{pool: pool}
	defer replica.Close()
	policy := hex.SiteAccess{Version: 2, Owners: []string{"user:owner"}, Admission: hex.Subjects{Owners: true}}
	if err := database.PutSiteAccess(ctx, "demo", policy); err != nil {
		t.Fatal(err)
	}
	serverConfig := hex.Config{Sites: portalSitesFixture{}, SiteBaseURL: "http://example.com", Identity: hex.StaticIdentity{Identity: hex.Identity{Provider: "test", ID: "operator", Groups: []string{"admins"}}}, AdminGroups: []string{"admins"}, Access: database, Database: database}
	first := hex.New(serverConfig)
	serverConfig.Access, serverConfig.Database = replica, replica
	second := hex.New(serverConfig)
	call := func(server *hex.Server, method, path, body string, status int) string {
		t.Helper()
		r := httptest.NewRequest(method, "http://example.com"+path, strings.NewReader(body))
		r.Header.Set("X-Hex-Request", "1")
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		w := httptest.NewRecorder()
		server.ServeHTTP(w, r)
		if w.Code != status {
			t.Fatalf("%s %s: %d %s", method, path, w.Code, w.Body.String())
		}
		return w.Body.String()
	}
	call(first, "GET", "/api/sites/demo/db/tasks", "", 403)
	form := url.Values{"scope": {"app-read"}, "reason": {"Investigating a live issue"}, "minutes": {"60"}}.Encode()
	call(first, "POST", "/api/hex/admin/sites/demo/access", form, 303)
	call(second, "GET", "/api/sites/demo/db/tasks", "", 200)
	leases, err := replica.ListAdminAccess(ctx, hex.AdminAccessQuery{Site: "demo", ActiveAt: time.Now(), Limit: 25})
	if err != nil || len(leases) != 1 {
		t.Fatalf("lease was not persisted: %v %v", leases, err)
	}
	call(second, "POST", "/api/hex/admin/sites/demo/access/"+leases[0].ID+"/revoke", "", 303)
	call(first, "GET", "/api/sites/demo/db/tasks", "", 403)
	audit, err := database.ListAdminAccess(ctx, hex.AdminAccessQuery{Site: "demo", Limit: 25})
	if err != nil || len(audit) != 1 || audit[0].RevokedAt.IsZero() || audit[0].RevokedBy.ID != "operator" {
		t.Fatalf("revocation audit was lost: %v %v", audit, err)
	}
	call(first, "POST", "/api/hex/admin/sites/demo/access", form, 303)
	if _, err := database.pool.Exec(ctx, `UPDATE hex_admin_access SET expires_at=now()-interval '1 second' WHERE revoked_at IS NULL`); err != nil {
		t.Fatal(err)
	}
	call(second, "GET", "/api/sites/demo/db/tasks", "", 403)
	stored, err := database.GetSiteAccess(ctx, "demo")
	if err != nil || !reflect.DeepEqual(stored, policy) {
		t.Fatal("lease altered the saved policy")
	}
	// Independent pools serialize same-scope renewals and retain every record.
	var workers sync.WaitGroup
	for index := range 12 {
		workers.Go(func() {
			store := database
			if index%2 == 0 {
				store = replica
			}
			now := time.Now().UTC()
			grant := hex.AdminAccessGrant{ID: fmt.Sprintf("renew-%02d", index), Site: "demo", Subject: "subject", Actor: hex.Person{ID: "operator"}, Scope: "management", Reason: "renewal", StartedAt: now, ExpiresAt: now.Add(time.Hour)}
			if err := store.GrantAdminAccess(ctx, grant); err != nil {
				t.Error(err)
			}
		})
	}
	workers.Wait()
	active, err := database.ListAdminAccess(ctx, hex.AdminAccessQuery{Site: "demo", Subject: "subject", ActiveAt: time.Now(), Limit: 25})
	if err != nil || len(active) != 1 {
		t.Fatalf("concurrent renewals left multiple grants: %v %v", active, err)
	}
	all, err := replica.ListAdminAccess(ctx, hex.AdminAccessQuery{Site: "demo", Subject: "subject", Limit: 25})
	if err != nil || len(all) != 12 {
		t.Fatalf("concurrent audit lost records: %d %v", len(all), err)
	}
}

func TestPostgresAdminReportsBoundRowsAndMatchMemoryPaging(t *testing.T) {
	database := isolatedAnalyticsDatabase(t)
	ctx := context.Background()
	if err := database.MigrateAnalytics(ctx); err != nil {
		t.Fatal(err)
	}
	inMemory := memory.NewAnalytics()
	at := time.Now().UTC().Truncate(time.Second)
	for index := range 61 {
		person := hex.Person{ID: fmt.Sprintf("person-%03d", index), Name: fmt.Sprintf("Person %03d", index), Email: fmt.Sprintf("person-%03d@example.test", index)}
		for _, store := range []hex.AnalyticsStore{database, inMemory} {
			if err := store.ObservePerson(ctx, person, at.Add(-time.Duration(index)*time.Second)); err != nil {
				t.Fatal(err)
			}
			if err := store.RecordTraffic(ctx, []hex.TrafficEvent{{ID: fmt.Sprintf("traffic-%03d", index), Site: fmt.Sprintf("app-%03d", index), UserID: person.ID, At: at, PageView: true, Status: 200}}); err != nil {
				t.Fatal(err)
			}
		}
	}
	query := hex.AdminAnalyticsQuery{AnalyticsQuery: hex.AnalyticsQuery{From: at.Truncate(24 * time.Hour), Until: at.Truncate(24*time.Hour).AddDate(0, 0, 1)}, Section: "users", Limit: 25, Page: 2, Sort: "name"}
	for _, test := range []struct {
		sort, search string
		page         int
	}{{"name", "", 2}, {"views", "", 2}, {"recent", "", 2}, {"name", "Person 060", 1}, {"name", "", 9999}} {
		query.Sort, query.Search, query.Page = test.sort, test.search, test.page
		actual, err := database.QueryAdminAnalytics(ctx, query)
		if err != nil {
			t.Fatal(err)
		}
		want, err := inMemory.QueryAdminAnalytics(ctx, query)
		if err != nil {
			t.Fatal(err)
		}
		if len(actual.People) > 25 || len(actual.Users) > 25 || actual.Total != want.Total || actual.Page != want.Page {
			t.Fatalf("wrong page shape: %+v", actual)
		}
		for index := range actual.People {
			if actual.People[index].ID != want.People[index].ID || actual.Users[index].PageViews != want.Users[index].PageViews {
				t.Fatalf("paging mismatch: %v vs %v", actual.People, want.People)
			}
		}
	}
	query.Section, query.Search, query.Page = "overview", "", 1
	overview, err := database.QueryAdminAnalytics(ctx, query)
	if err != nil || len(overview.Sites) > 5 || len(overview.Users) > 5 || len(overview.People) > 5 || len(overview.Events) > 50 || overview.Traffic.PageViews != 61 {
		t.Fatalf("overview is not bounded/accurate: %+v %v", overview, err)
	}
}

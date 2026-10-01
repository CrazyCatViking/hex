package hex_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"testing"

	hex "github.com/crazycatviking/hex/server"
	"github.com/crazycatviking/hex/server/providers/easyauth"
	"github.com/crazycatviking/hex/server/providers/local"
	"github.com/crazycatviking/hex/server/providers/memory"
)

func setupPortal(t *testing.T) (*hex.Server, *memory.Database) {
	t.Helper()
	store, err := local.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	database := memory.NewDatabase()
	server := hex.New(hex.Config{
		Sites:       store,
		Publisher:   store,
		Files:       memory.NewStore(),
		Database:    database,
		Identity:    easyauth.Resolver{},
		Access:      memory.NewAccessStore(),
		People:      memory.NewPeopleStore(),
		Groups:      hex.ParseGroups("Sales=sales-id"),
		AdminGroups: []string{"admin-group"},
		SiteBaseURL: "http://example.com",
	})
	return server, database
}

func formRequest(t *testing.T, handler http.Handler, headers http.Header, method, path string, values url.Values, want int) string {
	t.Helper()
	r := httptest.NewRequest(method, path, strings.NewReader(values.Encode()))
	r.Header.Set("X-Hex-Request", "1")
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	for name, header := range headers {
		r.Header[name] = header
	}
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	if w.Code != want {
		t.Fatalf("%s %s: got %d, want %d: %s", method, path, w.Code, want, w.Body.String())
	}
	return w.Body.String()
}

func TestPortalListsAndProtectsOwnedSites(t *testing.T) {
	server, _ := setupPortal(t)
	alex := signedIn(t, "alex-id", "Alex Andersen", "alex@example.test")
	bea := signedIn(t, "bea-id", "Bea Berg", "bea@example.test")
	admin := principalHeaders("admin-id", "admin-group")

	publishSite(t, server, alex, "dashboard", map[string]string{"index.html": "app"}, "")
	publishSite(t, server, bea, "beas-app", map[string]string{"index.html": "app"}, "")

	page := requestAs(t, server, alex, "GET", "/manage", nil, 200)
	if body := page.Body.String(); !strings.Contains(body, `href="/manage/dashboard"`) || strings.Contains(body, "beas-app") {
		t.Fatalf("listing should show only Alex's sites: %s", body)
	}
	// HTMX changes carry the request marker the API requires.
	if !strings.Contains(page.Body.String(), `hx-headers='{"X-Hex-Request":"1"}'`) {
		t.Fatal("portal pages must send the X-Hex-Request marker with HTMX requests")
	}
	if csp := page.Header().Get("Content-Security-Policy"); !strings.Contains(csp, "default-src 'self'") {
		t.Fatalf("portal pages need the content security policy: %q", csp)
	}
	if body := requestAs(t, server, admin, "GET", "/manage?all=1", nil, 200).Body.String(); !strings.Contains(body, "beas-app") || !strings.Contains(body, "dashboard") {
		t.Fatalf("admins can list every site: %s", body)
	}
	if body := requestAs(t, server, alex, "GET", "/manage?all=1", nil, 200).Body.String(); strings.Contains(body, "beas-app") {
		t.Fatal("only admins can list every site")
	}

	site := requestAs(t, server, alex, "GET", "/manage/dashboard", nil, 200).Body.String()
	for _, expected := range []string{"Demo", "Alex Andersen", "Who can open it", `href="/manage/dashboard?tab=sharing"`} {
		if !strings.Contains(site, expected) {
			t.Fatalf("site page lacks %q", expected)
		}
	}
	for _, tab := range []string{"sharing", "data", "history", "settings"} {
		requestAs(t, server, alex, "GET", "/manage/dashboard?tab="+tab, nil, 200)
	}
	if settings := requestAs(t, server, alex, "GET", "/manage/dashboard?tab=settings", nil, 200).Body.String(); !strings.Contains(settings, "Take it down") {
		t.Fatal("settings tab lacks unpublishing")
	}
	requestAs(t, server, bea, "GET", "/manage/dashboard", nil, 403)
	requestAs(t, server, admin, "GET", "/manage/dashboard", nil, 200)

	// The portal only answers on the platform's own host.
	r := httptest.NewRequest("GET", "/manage", nil)
	r.Host = "dashboard.example.com"
	for name, values := range alex {
		r.Header[name] = values
	}
	w := httptest.NewRecorder()
	server.ServeHTTP(w, r)
	if w.Code != http.StatusNotFound {
		t.Fatalf("portal served on a site host: %d", w.Code)
	}
}

func TestPortalSharing(t *testing.T) {
	server, _ := setupPortal(t)
	alex := signedIn(t, "alex-id", "Alex Andersen", "alex@example.test")
	bea := signedIn(t, "bea-id", "<img src=x onerror=alert(1)>", "bea@example.test")
	publishSite(t, server, alex, "dashboard", map[string]string{"index.html": "app"}, "")
	requestAs(t, server, bea, "GET", "/api/hex/me", nil, 200)

	path := "/api/hex/manage/sites/dashboard/sharing"
	saved := formRequest(t, server, alex, "PUT", path, url.Values{
		"principal": {"user:alex-id", "group:sales-id", "user:bea-id", "user:new.person@example.test"},
		"role":      {"owner", "viewer", "editor", "viewer"},
		"general":   {"restricted"},
		"rules":     {`{"paths":[{"prefix":"/admin/","viewers":"owners"}]}`},
	}, 200)
	if !strings.Contains(saved, "Saved") || !strings.Contains(saved, "Sales") || !strings.Contains(saved, "/admin/") {
		t.Fatalf("sharing form did not save: %s", saved)
	}
	// Names from the directory are escaped.
	if strings.Contains(saved, "<img src=x") || !strings.Contains(saved, "&lt;img src=x") {
		t.Fatalf("a person's name was not escaped: %s", saved)
	}

	var policy hex.SiteAccess
	if err := json.Unmarshal(requestAs(t, server, alex, "GET", "/api/hex/sites/dashboard/access", nil, 200).Body.Bytes(), &policy); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(policy.Owners, []string{"user:alex-id"}) || !slices.Equal(policy.Editors, []string{"user:bea-id"}) ||
		!slices.Equal(policy.Viewers, []string{"group:sales-id", "user:new.person@example.test"}) || len(policy.Paths) != 1 {
		t.Fatalf("unexpected policy: %+v", policy)
	}

	// Opening it to everyone keeps the people who may edit.
	formRequest(t, server, alex, "PUT", path, url.Values{
		"principal": {"user:alex-id", "user:bea-id"},
		"role":      {"owner", "editor"},
		"general":   {"view"},
	}, 200)
	policy = hex.SiteAccess{}
	if err := json.Unmarshal(requestAs(t, server, alex, "GET", "/api/hex/sites/dashboard/access", nil, 200).Body.Bytes(), &policy); err != nil {
		t.Fatal(err)
	}
	if len(policy.Viewers) != 0 || !slices.Equal(policy.Editors, []string{"user:bea-id"}) || len(policy.Paths) != 0 {
		t.Fatalf("everyone-can-view policy: %+v", policy)
	}

	// Mistakes are shown in the form, not applied.
	for _, values := range []url.Values{
		{"principal": {"user:bea-id"}, "role": {"viewer"}, "general": {"restricted"}},
		{"principal": {"user:someone-else"}, "role": {"owner"}, "general": {"restricted"}},
		{"principal": {"user:alex-id"}, "role": {"owner"}, "general": {"restricted"}, "rules": {"{not json"}},
		{"principal": {"user:alex-id"}, "role": {"owner"}, "general": {"restricted"}, "rules": {`{"owners":["user:x"]}`}},
		{"principal": {"user:alex-id", "sales"}, "role": {"owner", "viewer"}, "general": {"restricted"}},
		{"principal": {"user:alex-id"}, "role": {"owner", "viewer"}, "general": {"restricted"}},
	} {
		if body := formRequest(t, server, alex, "PUT", path, values, 200); !strings.Contains(body, "banner-error") {
			t.Fatalf("invalid sharing form was accepted: %v: %s", values, body)
		}
	}
	formRequest(t, server, bea, "PUT", path, url.Values{"principal": {"user:bea-id"}, "role": {"owner"}, "general": {"edit"}}, 403)

	// Changes need the request marker, like every API change.
	r := httptest.NewRequest("PUT", path, strings.NewReader("principal=user%3Aalex-id&role=owner&general=edit"))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	for name, values := range alex {
		r.Header[name] = values
	}
	w := httptest.NewRecorder()
	server.ServeHTTP(w, r)
	if w.Code != http.StatusForbidden {
		t.Fatalf("a cross-site form post was accepted: %d", w.Code)
	}
}

func TestHomeShowsSharedAndOwnedSites(t *testing.T) {
	server, _ := setupPortal(t)
	alex := signedIn(t, "alex-id", "Alex Andersen", "alex@example.test")
	bea := signedIn(t, "bea-id", "Bea Berg", "bea@example.test")
	publishSite(t, server, alex, "report", map[string]string{"index.html": "report"}, `{"viewers":["user:alex-id","user:bea@example.test"]}`)
	publishSite(t, server, alex, "private", map[string]string{"index.html": "private"}, `{"viewers":["user:alex-id"]}`)

	home := requestAs(t, server, bea, "GET", "/", nil, 200).Body.String()
	shared := home[strings.Index(home, "Shared with you"):strings.Index(home, `id="mine-heading"`)]
	if !strings.Contains(shared, `href="http://report.example.com/"`) || strings.Contains(shared, "private.example.com") {
		t.Fatalf("shared section is wrong: %s", shared)
	}
	if !strings.Contains(home, "Welcome back, Bea") || !strings.Contains(home, "Nothing published yet") {
		t.Fatal("home is not personal")
	}

	own := requestAs(t, server, alex, "GET", "/", nil, 200).Body.String()
	if strings.Contains(own, "Shared with you") || !strings.Contains(own, `href="/manage/report"`) || !strings.Contains(own, "Only owners") {
		t.Fatalf("owner's home is wrong")
	}
}

func TestPortalDataBrowserAndUnpublish(t *testing.T) {
	server, database := setupPortal(t)
	alex := signedIn(t, "alex-id", "Alex Andersen", "alex@example.test")
	bea := signedIn(t, "bea-id", "Bea Berg", "bea@example.test")
	publishSite(t, server, alex, "dashboard", map[string]string{"index.html": "app"}, "")
	requestAs(t, server, bea, "PUT", "/api/sites/dashboard/db/tasks/first", []byte(`{"title":"<b>ship</b>"}`), 200)
	requestAs(t, server, alex, "PUT", "/api/sites/dashboard/files/exports/report.csv", []byte("a,b"), 200)

	collections := requestAs(t, server, alex, "GET", "/api/hex/manage/sites/dashboard/collections", nil, 200).Body.String()
	if !strings.Contains(collections, "tasks") {
		t.Fatalf("collections not listed: %s", collections)
	}
	documents := requestAs(t, server, alex, "GET", "/api/hex/manage/sites/dashboard/collections/tasks", nil, 200).Body.String()
	if !strings.Contains(documents, "first") || !strings.Contains(documents, "Bea Berg") || strings.Contains(documents, "<b>ship") {
		t.Fatalf("documents not listed with their creator, or not escaped: %s", documents)
	}
	document := requestAs(t, server, alex, "GET", "/api/hex/manage/sites/dashboard/collections/tasks/first", nil, 200).Body.String()
	if !strings.Contains(document, "&lt;b&gt;ship&lt;/b&gt;") {
		t.Fatalf("document view: %s", document)
	}
	requestAs(t, server, bea, "GET", "/api/hex/manage/sites/dashboard/collections/tasks", nil, 403)
	requestAs(t, server, alex, "DELETE", "/api/hex/manage/sites/dashboard/collections/tasks/first", nil, 200)
	if _, err := database.Get(context.Background(), "dashboard", "tasks", "first"); err == nil {
		t.Fatal("document was not deleted")
	}

	files := requestAs(t, server, alex, "GET", "/api/hex/manage/sites/dashboard/files", nil, 200).Body.String()
	if !strings.Contains(files, `href="/api/sites/dashboard/files/exports/report.csv"`) {
		t.Fatalf("files not listed: %s", files)
	}
	requestAs(t, server, alex, "DELETE", "/api/hex/manage/sites/dashboard/files/exports/report.csv", nil, 200)
	requestAs(t, server, alex, "GET", "/api/sites/dashboard/files/exports/report.csv", nil, 404)

	// Unpublishing needs the site's name typed as confirmation.
	formRequest(t, server, alex, "POST", "/api/hex/manage/sites/dashboard/unpublish", url.Values{"confirm": {"wrong"}}, 400)
	formRequest(t, server, bea, "POST", "/api/hex/manage/sites/dashboard/unpublish", url.Values{"confirm": {"dashboard"}}, 403)
	formRequest(t, server, alex, "POST", "/api/hex/manage/sites/dashboard/unpublish", url.Values{"confirm": {"dashboard"}}, 204)
	if body := requestAs(t, server, alex, "GET", "/manage", nil, 200).Body.String(); strings.Contains(body, `href="/manage/dashboard"`) {
		t.Fatal("unpublished site is still listed")
	}
}

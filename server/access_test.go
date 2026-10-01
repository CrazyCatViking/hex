package hex_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	hex "github.com/crazycatviking/hex/server"
	"github.com/crazycatviking/hex/server/providers/easyauth"
	"github.com/crazycatviking/hex/server/providers/local"
	"github.com/crazycatviking/hex/server/providers/memory"
)

func setupWithAccess(t *testing.T) (*hex.Server, *local.Store) {
	t.Helper()
	store, err := local.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Errorf("close test store: %v", err)
		}
	})

	server := hex.New(hex.Config{
		Files:          store,
		Sites:          store,
		Publisher:      store,
		Database:       memory.NewDatabase(),
		Realtime:       memory.NewRealtime(),
		Identity:       easyauth.Resolver{},
		Access:         memory.NewAccessStore(),
		AdminGroups:    []string{"admin-group"},
		MaxUploadBytes: 4096,
	})
	return server, store
}

func principalHeaders(id string, groups ...string) http.Header {
	claims := make([]map[string]string, 0, len(groups))
	for _, group := range groups {
		claims = append(claims, map[string]string{"typ": "groups", "val": group})
	}
	document, err := json.Marshal(map[string]any{
		"auth_typ": "aad",
		"claims":   claims,
		"name_typ": "name",
		"role_typ": "roles",
	})
	if err != nil {
		panic(err)
	}

	headers := http.Header{}
	headers.Set("X-Ms-Client-Principal", base64.StdEncoding.EncodeToString(document))
	headers.Set("X-Ms-Client-Principal-Id", id)
	headers.Set("X-Ms-Client-Principal-Name", id+"@example.test")
	return headers
}

func requestAs(t *testing.T, handler http.Handler, headers http.Header, method, path string, data []byte, want int) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(method, path, bytes.NewReader(data))
	r.Header.Set("X-Hex-Request", "1")
	for name, values := range headers {
		r.Header[name] = values
	}
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	if w.Code != want {
		t.Fatalf("%s %s: got %d, want %d: %s", method, path, w.Code, want, w.Body.String())
	}
	return w
}

func putPolicy(t *testing.T, server http.Handler, headers http.Header, site, policy string) {
	t.Helper()
	requestAs(t, server, headers, "PUT", "/api/hex/sites/"+site+"/access", []byte(policy), 200)
}

func TestSiteAccessLifecycle(t *testing.T) {
	server, store := setupWithAccess(t)
	owner := principalHeaders("owner-id")
	member := principalHeaders("member-id", "sales")
	outsider := principalHeaders("outsider-id", "unrelated")
	admin := principalHeaders("admin-id", "admin-group")

	if err := store.Put(context.Background(), "public/sites/demo/index.html", strings.NewReader("app")); err != nil {
		t.Fatal(err)
	}

	// Sites without a policy stay open to every authenticated user.
	requestAs(t, server, outsider, "PUT", "/api/sites/demo/db/tasks/a", []byte(`{"open":true}`), 200)

	putPolicy(t, server, owner, "demo", `{"owners":["user:owner-id"],"viewers":["group:sales"]}`)

	// Viewers and owners use the namespace; outsiders and anonymous callers do not.
	requestAs(t, server, member, "GET", "/api/sites/demo/db/tasks/a", nil, 200)
	requestAs(t, server, owner, "GET", "/api/sites/demo/db/tasks/a", nil, 200)
	requestAs(t, server, admin, "GET", "/api/sites/demo/db/tasks/a", nil, 200)
	requestAs(t, server, outsider, "GET", "/api/sites/demo/db/tasks/a", nil, 403)
	requestAs(t, server, outsider, "PUT", "/api/sites/demo/files/report.txt", []byte("data"), 403)
	requestAs(t, server, nil, "GET", "/api/sites/demo/db/tasks/a", nil, 401)

	// Only owners and admins manage the policy; a takeover attempt fails.
	requestAs(t, server, member, "GET", "/api/hex/sites/demo/access", nil, 403)
	takeover := []byte(`{"owners":["user:outsider-id"],"viewers":["group:unrelated"]}`)
	requestAs(t, server, outsider, "PUT", "/api/hex/sites/demo/access", takeover, 403)
	requestAs(t, server, owner, "GET", "/api/hex/sites/demo/access", nil, 200)

	// An owner cannot lock themselves out; admins may hand the policy over.
	lockout := []byte(`{"owners":["user:someone-else"],"viewers":["group:sales"]}`)
	requestAs(t, server, owner, "PUT", "/api/hex/sites/demo/access", lockout, 400)
	putPolicy(t, server, admin, "demo", `{"owners":["user:owner-id","group:ops"],"viewers":["group:sales","group:ops"]}`)

	// Omitting owners keeps the current ones.
	putPolicy(t, server, owner, "demo", `{"viewers":["group:sales"]}`)
	policy := requestAs(t, server, owner, "GET", "/api/hex/sites/demo/access", nil, 200).Body.String()
	if !strings.Contains(policy, `"owners":["user:owner-id","group:ops"]`) {
		t.Fatalf("owners were not kept: %s", policy)
	}

	// Restricted sites disappear from discovery for non-viewers.
	listing := requestAs(t, server, outsider, "GET", "/api/sites", nil, 200).Body.String()
	if strings.Contains(listing, "demo") {
		t.Fatalf("restricted site leaked into discovery: %s", listing)
	}
	listing = requestAs(t, server, member, "GET", "/api/sites", nil, 200).Body.String()
	if !strings.Contains(listing, "demo") {
		t.Fatalf("viewer cannot discover the site: %s", listing)
	}

	// Clearing the policy opens the site again.
	requestAs(t, server, owner, "DELETE", "/api/hex/sites/demo/access", nil, 204)
	requestAs(t, server, outsider, "GET", "/api/sites/demo/db/tasks/a", nil, 200)
	requestAs(t, server, owner, "DELETE", "/api/hex/sites/demo/access", nil, 404)
}

func TestUsersMatchByEmail(t *testing.T) {
	server, _ := setupWithAccess(t)
	owner := principalHeaders("owner-id")
	putPolicy(t, server, owner, "demo", `{"viewers":["user:Alex@Example.test"]}`)

	document, err := json.Marshal(map[string]any{
		"auth_typ": "aad", "name_typ": "name",
		"claims": []map[string]string{{"typ": "preferred_username", "val": "alex@example.test"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	alex := http.Header{}
	alex.Set("X-Ms-Client-Principal", base64.StdEncoding.EncodeToString(document))
	alex.Set("X-Ms-Client-Principal-Id", "alex-object-id")
	alex.Set("X-Ms-Client-Principal-Name", "Alex Example")

	requestAs(t, server, alex, "GET", "/api/sites/demo/db/tasks", nil, 200)
	requestAs(t, server, principalHeaders("someone-else"), "GET", "/api/sites/demo/db/tasks", nil, 403)
}

func TestViewersAndEditors(t *testing.T) {
	server, _ := setupWithAccess(t)
	owner := principalHeaders("owner-id")
	viewer := principalHeaders("viewer-id", "sales")
	editor := principalHeaders("editor-id", "editors")

	// Without editors, every viewer may edit.
	putPolicy(t, server, owner, "demo", `{"owners":["user:owner-id"]}`)
	requestAs(t, server, viewer, "PUT", "/api/sites/demo/db/tasks/a", []byte(`{"v":1}`), 200)

	putPolicy(t, server, owner, "demo", `{"editors":["group:editors"]}`)
	requestAs(t, server, viewer, "GET", "/api/sites/demo/db/tasks/a", nil, 200)
	requestAs(t, server, viewer, "PUT", "/api/sites/demo/db/tasks/a", []byte(`{"v":2}`), 403)
	requestAs(t, server, viewer, "POST", "/api/sites/demo/db/tasks", []byte(`{"v":2}`), 403)
	requestAs(t, server, viewer, "DELETE", "/api/sites/demo/db/tasks/a", nil, 403)
	requestAs(t, server, viewer, "PUT", "/api/sites/demo/files/a.txt", []byte("x"), 403)
	requestAs(t, server, editor, "PUT", "/api/sites/demo/db/tasks/a", []byte(`{"v":3}`), 200)
	requestAs(t, server, editor, "PUT", "/api/sites/demo/files/a.txt", []byte("x"), 200)
	requestAs(t, server, viewer, "GET", "/api/sites/demo/files/a.txt", nil, 200)

	// Explicit editors can edit even when viewers are restricted to others.
	putPolicy(t, server, owner, "demo", `{"viewers":["group:sales"],"editors":["group:editors"]}`)
	requestAs(t, server, editor, "GET", "/api/sites/demo/db/tasks/a", nil, 200)
	requestAs(t, server, principalHeaders("stranger"), "GET", "/api/sites/demo/db/tasks/a", nil, 403)
}

func TestCollectionRules(t *testing.T) {
	server, _ := setupWithAccess(t)
	owner := principalHeaders("owner-id")
	viewer := principalHeaders("viewer-id")
	auditor := principalHeaders("auditor-id", "auditors")

	putPolicy(t, server, owner, "demo", `{
		"owners": ["user:owner-id"],
		"collections": {
			"settings": {"read": "viewers", "write": "owners"},
			"audit": {"read": ["group:auditors"], "write": "owners"},
			"*": {"read": "viewers", "write": "editors"}
		}
	}`)

	requestAs(t, server, owner, "PUT", "/api/sites/demo/db/settings/theme", []byte(`{"dark":true}`), 200)
	requestAs(t, server, viewer, "GET", "/api/sites/demo/db/settings/theme", nil, 200)
	requestAs(t, server, viewer, "PUT", "/api/sites/demo/db/settings/theme", []byte(`{"dark":false}`), 403)

	requestAs(t, server, owner, "POST", "/api/sites/demo/db/audit", []byte(`{"event":"login"}`), 201)
	requestAs(t, server, viewer, "GET", "/api/sites/demo/db/audit", nil, 403)
	requestAs(t, server, auditor, "GET", "/api/sites/demo/db/audit", nil, 200)
	requestAs(t, server, auditor, "POST", "/api/sites/demo/db/audit", []byte(`{"event":"forged"}`), 403)

	// Unlisted collections use the "*" rule.
	requestAs(t, server, viewer, "PUT", "/api/sites/demo/db/tasks/a", []byte(`{"v":1}`), 200)
}

func TestCreatorOnlyDocuments(t *testing.T) {
	server, _ := setupWithAccess(t)
	owner := principalHeaders("owner-id")
	alice := principalHeaders("alice")
	bob := principalHeaders("bob")

	putPolicy(t, server, owner, "demo", `{"collections":{"drafts":{"read":"creator","write":"creator"}}}`)

	created := requestAs(t, server, alice, "POST", "/api/sites/demo/db/drafts", []byte(`{"text":"secret"}`), 201)
	var document hex.Document
	if err := json.Unmarshal(created.Body.Bytes(), &document); err != nil {
		t.Fatal(err)
	}
	if document.CreatedBy != "alice" {
		t.Fatalf("creator was not recorded: %+v", document)
	}
	path := "/api/sites/demo/db/drafts/" + document.ID
	requestAs(t, server, bob, "PUT", "/api/sites/demo/db/drafts/bobs", []byte(`{"text":"mine"}`), 200)

	// Bob neither sees nor changes Alice's draft; it looks missing to him.
	requestAs(t, server, bob, "GET", path, nil, 404)
	requestAs(t, server, bob, "PUT", path, []byte(`{"text":"hijacked"}`), 403)
	requestAs(t, server, bob, "DELETE", path, nil, 403)
	listing := requestAs(t, server, bob, "GET", "/api/sites/demo/db/drafts", nil, 200).Body.String()
	if strings.Contains(listing, "secret") || !strings.Contains(listing, "mine") {
		t.Fatalf("creator filter failed: %s", listing)
	}

	requestAs(t, server, alice, "PUT", path, []byte(`{"text":"edited"}`), 200)
	requestAs(t, server, alice, "GET", path, nil, 200)

	// Owners see and manage everything.
	everything := requestAs(t, server, owner, "GET", "/api/sites/demo/db/drafts", nil, 200).Body.String()
	if !strings.Contains(everything, "edited") || !strings.Contains(everything, "mine") {
		t.Fatalf("owner listing incomplete: %s", everything)
	}
	requestAs(t, server, alice, "DELETE", path, nil, 204)
}

func TestFileAndChannelRules(t *testing.T) {
	server, _ := setupWithAccess(t)
	owner := principalHeaders("owner-id")
	viewer := principalHeaders("viewer-id")

	putPolicy(t, server, owner, "demo", `{
		"files": {"exports/": {"read": "owners", "write": "owners"}},
		"channels": {"announcements": {"read": "viewers", "write": "owners"}, "staff": {"read": "owners"}}
	}`)

	requestAs(t, server, owner, "PUT", "/api/sites/demo/files/exports/report.csv", []byte("a,b"), 200)
	requestAs(t, server, owner, "PUT", "/api/sites/demo/files/public.txt", []byte("hi"), 200)
	requestAs(t, server, viewer, "GET", "/api/sites/demo/files/exports/report.csv", nil, 403)
	requestAs(t, server, viewer, "PUT", "/api/sites/demo/files/exports/new.csv", []byte("x"), 403)
	requestAs(t, server, viewer, "GET", "/api/sites/demo/files/public.txt", nil, 200)
	listing := requestAs(t, server, viewer, "GET", "/api/sites/demo/files", nil, 200).Body.String()
	if strings.Contains(listing, "exports") || !strings.Contains(listing, "public.txt") {
		t.Fatalf("file listing ignores rules: %s", listing)
	}

	requestAs(t, server, viewer, "GET", "/api/sites/demo/realtime/staff", nil, 403)

	httpServer := httptest.NewServer(server)
	defer httpServer.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	url := "ws" + strings.TrimPrefix(httpServer.URL, "http") + "/api/sites/demo/realtime/announcements"
	listener, _, err := websocket.Dial(ctx, url, &websocket.DialOptions{HTTPHeader: viewer})
	if err != nil {
		t.Fatal(err)
	}
	defer listener.CloseNow()
	if err := listener.Write(ctx, websocket.MessageText, []byte(`{"spoofed":true}`)); err != nil {
		t.Fatal(err)
	}
	if _, _, err := listener.Read(ctx); websocket.CloseStatus(err) != websocket.StatusPolicyViolation {
		t.Fatalf("read-only subscriber could send: %v", err)
	}
}

func TestStaticAssetAuthorizationEndpoint(t *testing.T) {
	server, _ := setupWithAccess(t)
	owner := principalHeaders("owner-id")
	member := principalHeaders("member-id", "sales")
	admin := principalHeaders("admin-id", "admins")
	outsider := principalHeaders("outsider-id")

	putPolicy(t, server, owner, "demo", `{
		"owners": ["user:owner-id"],
		"viewers": ["group:sales", "group:admins"],
		"paths": [
			{"prefix": "/admin/", "viewers": ["group:admins"]},
			{"prefix": "/admin/help/", "viewers": "viewers"}
		]
	}`)

	authz := func(t *testing.T, headers http.Header, site, path string, want int) {
		t.Helper()
		r := httptest.NewRequest("GET", "/api/hex/authz", nil)
		for name, values := range headers {
			r.Header[name] = values
		}
		if site != "" {
			r.Header.Set("X-Hex-Site", site)
		}
		if path != "" {
			r.Header.Set("X-Hex-Path", path)
		}
		// auth_request subrequests inherit the visitor's headers, including
		// cross-site Fetch Metadata from external navigations.
		r.Header.Set("Sec-Fetch-Site", "cross-site")
		w := httptest.NewRecorder()
		server.ServeHTTP(w, r)
		if w.Code != want {
			t.Fatalf("authz for %q %q: got %d, want %d: %s", site, path, w.Code, want, w.Body.String())
		}
	}

	authz(t, member, "demo", "/", 204)
	authz(t, owner, "demo", "/index.html", 204)
	authz(t, outsider, "demo", "/", 403)
	authz(t, nil, "demo", "/", 403)
	authz(t, outsider, "unregistered", "/", 204)
	authz(t, nil, "", "/", 204)

	// Path rules: the longest prefix wins, the bare directory is covered, and
	// matching ignores case like the Azure Files share does.
	authz(t, member, "demo", "/admin/index.html", 403)
	authz(t, member, "demo", "/admin", 403)
	authz(t, member, "demo", "/ADMIN/index.html", 403)
	authz(t, member, "demo", "/administration.html", 204)
	authz(t, member, "demo", "/admin/help/faq.html", 204)
	authz(t, admin, "demo", "/admin/index.html", 204)
	authz(t, owner, "demo", "/admin/index.html", 204)
}

func TestPermissionsDescribeTheCaller(t *testing.T) {
	server, _ := setupWithAccess(t)
	owner := principalHeaders("owner-id")
	viewer := principalHeaders("viewer-id")

	putPolicy(t, server, owner, "demo", `{
		"editors": ["group:editors"],
		"paths": [{"prefix": "/admin/", "viewers": "owners"}],
		"collections": {"drafts": {"read": "creator", "write": "creator"}}
	}`)

	var permissions struct {
		Role    string `json:"role"`
		Publish bool   `json:"publish"`
		Paths   []struct {
			Prefix  string `json:"prefix"`
			Allowed bool   `json:"allowed"`
		} `json:"paths"`
		Collections map[string]struct {
			Read  string `json:"read"`
			Write string `json:"write"`
		} `json:"collections"`
	}
	response := requestAs(t, server, viewer, "GET", "/api/hex/sites/demo/permissions", nil, 200)
	if err := json.Unmarshal(response.Body.Bytes(), &permissions); err != nil {
		t.Fatal(err)
	}
	if permissions.Role != "viewer" || permissions.Publish || len(permissions.Paths) != 1 || permissions.Paths[0].Allowed {
		t.Fatalf("unexpected viewer permissions: %+v", permissions)
	}
	if permissions.Collections["drafts"].Read != "own" || permissions.Collections["*"].Write != "none" {
		t.Fatalf("unexpected collection permissions: %+v", permissions.Collections)
	}

	putPolicy(t, server, owner, "demo", `{
		"viewers": ["group:staff"],
		"paths": [{"prefix": "/admin/", "viewers": "owners"}]
	}`)
	hidden := requestAs(t, server, viewer, "GET", "/api/hex/sites/demo/permissions", nil, 200).Body.String()
	if strings.Contains(hidden, "/admin/") || !strings.Contains(hidden, `"role":"none"`) {
		t.Fatalf("rules leaked to a caller who cannot view the site: %s", hidden)
	}

	response = requestAs(t, server, owner, "GET", "/api/hex/sites/demo/permissions", nil, 200)
	if err := json.Unmarshal(response.Body.Bytes(), &permissions); err != nil {
		t.Fatal(err)
	}
	if permissions.Role != "owner" || !permissions.Publish || !permissions.Paths[0].Allowed {
		t.Fatalf("unexpected owner permissions: %+v", permissions)
	}
}

func TestIdentityEndpointAndCapabilities(t *testing.T) {
	server, _ := setupWithAccess(t)

	response := requestAs(t, server, principalHeaders("user-id", "sales", "ops"), "GET", "/api/hex/me", nil, 200)
	var identity hex.Identity
	if err := json.Unmarshal(response.Body.Bytes(), &identity); err != nil {
		t.Fatal(err)
	}
	if identity.ID != "user-id" || identity.Provider != "aad" || len(identity.Groups) != 2 {
		t.Fatalf("unexpected identity: %+v", identity)
	}

	requestAs(t, server, nil, "GET", "/api/hex/me", nil, 401)
	requestAs(t, server, nil, "PUT", "/api/hex/sites/demo/access", []byte(`{"owners":["user:x"]}`), 401)

	capabilities := requestAs(t, server, nil, "GET", "/api/hex/capabilities", nil, 200).Body.String()
	for _, expected := range []string{`"identity":true`, `"accessControl":true`, `"publishing":true`} {
		if !strings.Contains(capabilities, expected) {
			t.Fatalf("capabilities missing %s: %s", expected, capabilities)
		}
	}

	// Without identity and access configuration the routes stay disabled and
	// the authorization endpoint allows everything.
	open, _ := setup(t)
	requestAs(t, open, nil, "GET", "/api/hex/me", nil, 404)
	requestAs(t, open, nil, "GET", "/api/hex/sites/demo/access", nil, 404)
	r := httptest.NewRequest("GET", "/api/hex/authz", nil)
	r.Header.Set("X-Hex-Site", "demo")
	w := httptest.NewRecorder()
	open.ServeHTTP(w, r)
	if w.Code != 204 {
		t.Fatalf("authz without access control: %d", w.Code)
	}
}

func TestAccessPolicyValidation(t *testing.T) {
	server, _ := setupWithAccess(t)
	owner := principalHeaders("owner-id")

	invalid := []string{
		`[]`,
		`{"viewers":["sales"]}`,
		`{"viewers":["group: spaced"]}`,
		fmt.Sprintf(`{"viewers":[%s"group:last"]}`, strings.Repeat(`"group:g",`, 64)),
		`{"paths":[{"prefix":"admin/","viewers":"owners"}]}`,
		`{"paths":[{"prefix":"/../x","viewers":"owners"}]}`,
		`{"paths":[{"prefix":"/admin/"}]}`,
		`{"paths":[{"prefix":"/admin/","viewers":"creator"}]}`,
		`{"collections":{"bad name":{"read":"viewers"}}}`,
		`{"collections":{"tasks":{"read":"everyone"}}}`,
		`{"files":{"exports/":{"read":"creator"}}}`,
		`{"files":{"../":{"read":"owners"}}}`,
		`{"channels":{"chat":{"write":"creator"}}}`,
	}
	for _, body := range invalid {
		requestAs(t, server, owner, "PUT", "/api/hex/sites/demo/access", []byte(body), 400)
	}

	// Owners without viewers reserve a name without restricting viewers.
	putPolicy(t, server, owner, "demo", `{"owners":["user:owner-id"]}`)
	requestAs(t, server, principalHeaders("anyone"), "GET", "/api/sites/demo/files", nil, 200)
}

func TestPublisherGroupsLimitNewSites(t *testing.T) {
	server := hex.New(hex.Config{
		Database:        memory.NewDatabase(),
		Identity:        easyauth.Resolver{},
		Access:          memory.NewAccessStore(),
		PublisherGroups: []string{"publishers"},
	})
	requestAs(t, server, principalHeaders("someone"), "PUT", "/api/hex/sites/demo/access", []byte(`{}`), 403)
	requestAs(t, server, principalHeaders("publisher", "publishers"), "PUT", "/api/hex/sites/demo/access", []byte(`{}`), 200)
}

type unresolvedSiteIdentity struct {
	err error
}

func (s unresolvedSiteIdentity) ResolveIdentity(*http.Request) (*hex.Identity, error) {
	return nil, s.err
}

func TestSiteEndpointsRejectUnresolvedIdentity(t *testing.T) {
	_, store := setup(t)
	if err := store.Put(context.Background(), "public/sites/demo/index.html", strings.NewReader("app")); err != nil {
		t.Fatal(err)
	}
	registry := new(hex.ActionRegistry)
	if err := registry.Register("demo", taskAction()); err != nil {
		t.Fatal(err)
	}
	for _, resolver := range []struct {
		name string
		err  error
	}{
		{name: "failure", err: errors.New("resolver unavailable")},
		{name: "anonymous"},
	} {
		for _, policy := range []string{"disabled", "missing", "open", "restricted"} {
			t.Run(resolver.name+"/"+policy, func(t *testing.T) {
				var access hex.AccessStore
				if policy != "disabled" {
					access = memory.NewAccessStore()
				}
				if policy == "open" || policy == "restricted" {
					value := hex.SiteAccess{}
					if policy == "restricted" {
						value.Viewers = []string{"user:alice"}
					}
					if err := access.PutSiteAccess(context.Background(), "demo", value); err != nil {
						t.Fatal(err)
					}
				}
				server := hex.New(hex.Config{
					Files: store, Sites: store, Database: memory.NewDatabase(), Realtime: memory.NewRealtime(),
					Actions: registry, Access: access, Identity: unresolvedSiteIdentity{err: resolver.err},
				})
				for _, endpoint := range []struct {
					method string
					path   string
					body   string
				}{
					{"GET", "/actions", ""},
					{"GET", "/actions/create-task", ""},
					{"POST", "/actions/create-task", `{"title":"Task"}`},
					{"GET", "/db/tasks", ""},
					{"POST", "/db/tasks", `{}`},
					{"GET", "/db/tasks/a", ""},
					{"PUT", "/db/tasks/a", `{}`},
					{"DELETE", "/db/tasks/a", ""},
					{"GET", "/files", ""},
					{"GET", "/files/a.txt", ""},
					{"PUT", "/files/a.txt", "data"},
					{"DELETE", "/files/a.txt", ""},
					{"GET", "/realtime/room", ""},
				} {
					response := request(t, server, endpoint.method, "/api/sites/demo"+endpoint.path, []byte(endpoint.body), 401)
					if !strings.Contains(response.Body.String(), "authentication required") {
						t.Fatal(response.Body.String())
					}
				}
				requestAs(t, server, http.Header{"X-Hex-Site": {"demo"}}, "GET", "/api/hex/authz", nil, 403)
				request(t, server, "GET", "/api/hex/authz", nil, 204)
				if body := request(t, server, "GET", "/api/sites", nil, 200).Body.String(); body != "[]\n" {
					t.Fatalf("unresolved caller discovered site: %s", body)
				}
				permissions := request(t, server, "GET", "/api/hex/sites/demo/permissions", nil, 200).Body.String()
				if !strings.Contains(permissions, `"role":"none"`) {
					t.Fatalf("unresolved caller granted role: %s", permissions)
				}
			})
		}
	}
}

func TestSiteEndpointsStayOpenWithoutIdentityResolver(t *testing.T) {
	_, store := setup(t)
	if err := store.Put(context.Background(), "public/sites/demo/index.html", strings.NewReader("app")); err != nil {
		t.Fatal(err)
	}
	registry := new(hex.ActionRegistry)
	if err := registry.Register("demo", taskAction()); err != nil {
		t.Fatal(err)
	}
	for _, policy := range []string{"disabled", "missing", "open"} {
		t.Run(policy, func(t *testing.T) {
			var access hex.AccessStore
			if policy != "disabled" {
				access = memory.NewAccessStore()
			}
			if policy == "open" {
				if err := access.PutSiteAccess(context.Background(), "demo", hex.SiteAccess{}); err != nil {
					t.Fatal(err)
				}
			}
			server := hex.New(hex.Config{
				Files: store, Sites: store, Database: memory.NewDatabase(), Realtime: memory.NewRealtime(),
				Actions: registry, Access: access,
			})
			request(t, server, "GET", "/api/sites/demo/actions", nil, 200)
			request(t, server, "GET", "/api/sites/demo/actions/create-task", nil, 200)
			request(t, server, "POST", "/api/sites/demo/actions/create-task", []byte(`{"title":"Task"}`), 200)
			created := request(t, server, "POST", "/api/sites/demo/db/tasks", []byte(`{}`), 201)
			var document hex.Document
			if err := json.Unmarshal(created.Body.Bytes(), &document); err != nil {
				t.Fatal(err)
			}
			if document.CreatedBy != "" {
				t.Fatalf("anonymous creator: %q", document.CreatedBy)
			}
			request(t, server, "PUT", "/api/sites/demo/db/tasks/a", []byte(`{}`), 200)
			request(t, server, "GET", "/api/sites/demo/db/tasks/a", nil, 200)
			request(t, server, "GET", "/api/sites/demo/db/tasks", nil, 200)
			request(t, server, "DELETE", "/api/sites/demo/db/tasks/a", nil, 204)
			request(t, server, "PUT", "/api/sites/demo/files/a.txt", []byte("data"), 200)
			request(t, server, "GET", "/api/sites/demo/files/a.txt", nil, 200)
			request(t, server, "GET", "/api/sites/demo/files", nil, 200)
			request(t, server, "DELETE", "/api/sites/demo/files/a.txt", nil, 204)
			requestAs(t, server, http.Header{"X-Hex-Site": {"demo"}}, "GET", "/api/hex/authz", nil, 204)
			if body := request(t, server, "GET", "/api/sites", nil, 200).Body.String(); !strings.Contains(body, `"name":"demo"`) {
				t.Fatalf("open site missing from discovery: %s", body)
			}
			httpServer := httptest.NewServer(server)
			defer httpServer.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			url := "ws" + strings.TrimPrefix(httpServer.URL, "http") + "/api/sites/demo/realtime/room"
			connection, _, err := websocket.Dial(ctx, url, nil)
			if err != nil {
				t.Fatal(err)
			}
			if err := connection.CloseNow(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

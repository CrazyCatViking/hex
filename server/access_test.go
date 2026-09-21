package hex_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

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

func TestSiteAccessLifecycle(t *testing.T) {
	server, store := setupWithAccess(t)
	owner := principalHeaders("owner-id")
	member := principalHeaders("member-id", "sales")
	outsider := principalHeaders("outsider-id", "unrelated")
	admin := principalHeaders("admin-id", "admin-group")

	if err := store.Put(context.Background(), "public/sites/demo/index.html", strings.NewReader("app")); err != nil {
		t.Fatal(err)
	}

	// Unregistered sites stay open to every authenticated user.
	requestAs(t, server, outsider, "PUT", "/api/sites/demo/db/tasks/a", []byte(`{"open":true}`), 200)

	entry := []byte(`{"owners":["owner-id"],"groups":["sales"]}`)
	requestAs(t, server, owner, "PUT", "/api/hex/sites/demo/access", entry, 200)

	// Members and owners use the namespace; outsiders and anonymous callers do not.
	requestAs(t, server, member, "GET", "/api/sites/demo/db/tasks/a", nil, 200)
	requestAs(t, server, owner, "GET", "/api/sites/demo/db/tasks/a", nil, 200)
	requestAs(t, server, admin, "GET", "/api/sites/demo/db/tasks/a", nil, 200)
	requestAs(t, server, outsider, "GET", "/api/sites/demo/db/tasks/a", nil, 403)
	requestAs(t, server, outsider, "PUT", "/api/sites/demo/files/report.txt", []byte("data"), 403)
	requestAs(t, server, nil, "GET", "/api/sites/demo/db/tasks/a", nil, 403)

	// Only owners and admins manage the entry; a takeover attempt fails.
	requestAs(t, server, member, "GET", "/api/hex/sites/demo/access", nil, 403)
	takeover := []byte(`{"owners":["outsider-id"],"groups":["unrelated"]}`)
	requestAs(t, server, outsider, "PUT", "/api/hex/sites/demo/access", takeover, 403)
	requestAs(t, server, owner, "GET", "/api/hex/sites/demo/access", nil, 200)

	// An owner cannot lock themselves out; admins may hand the entry over.
	lockout := []byte(`{"owners":["someone-else"],"groups":["sales"]}`)
	requestAs(t, server, owner, "PUT", "/api/hex/sites/demo/access", lockout, 400)
	handover := []byte(`{"owners":["owner-id","ops"],"groups":["sales","ops"]}`)
	requestAs(t, server, admin, "PUT", "/api/hex/sites/demo/access", handover, 200)

	// Restricted sites disappear from discovery for non-members.
	listing := requestAs(t, server, outsider, "GET", "/api/sites", nil, 200).Body.String()
	if strings.Contains(listing, "demo") {
		t.Fatalf("restricted site leaked into discovery: %s", listing)
	}
	listing = requestAs(t, server, member, "GET", "/api/sites", nil, 200).Body.String()
	if !strings.Contains(listing, "demo") {
		t.Fatalf("member cannot discover the site: %s", listing)
	}

	// Clearing the entry opens the site again.
	requestAs(t, server, owner, "DELETE", "/api/hex/sites/demo/access", nil, 204)
	requestAs(t, server, outsider, "GET", "/api/sites/demo/db/tasks/a", nil, 200)
	requestAs(t, server, owner, "DELETE", "/api/hex/sites/demo/access", nil, 404)
}

func TestStaticAssetAuthorizationEndpoint(t *testing.T) {
	server, _ := setupWithAccess(t)
	owner := principalHeaders("owner-id")
	member := principalHeaders("member-id", "sales")
	outsider := principalHeaders("outsider-id")

	entry := []byte(`{"owners":["owner-id"],"groups":["sales"]}`)
	requestAs(t, server, owner, "PUT", "/api/hex/sites/demo/access", entry, 200)

	authz := func(t *testing.T, headers http.Header, site string, want int) {
		t.Helper()
		r := httptest.NewRequest("GET", "/api/hex/authz", nil)
		for name, values := range headers {
			r.Header[name] = values
		}
		if site != "" {
			r.Header.Set("X-Hex-Site", site)
		}
		// auth_request subrequests inherit the visitor's headers, including
		// cross-site Fetch Metadata from external navigations.
		r.Header.Set("Sec-Fetch-Site", "cross-site")
		w := httptest.NewRecorder()
		server.ServeHTTP(w, r)
		if w.Code != want {
			t.Fatalf("authz for %q: got %d, want %d: %s", site, w.Code, want, w.Body.String())
		}
	}

	authz(t, member, "demo", 204)
	authz(t, owner, "demo", 204)
	authz(t, outsider, "demo", 403)
	authz(t, nil, "demo", 403)
	authz(t, outsider, "unregistered", 204)
	authz(t, nil, "", 204)
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
	requestAs(t, server, nil, "PUT", "/api/hex/sites/demo/access", []byte(`{"owners":["x"]}`), 401)

	capabilities := requestAs(t, server, nil, "GET", "/api/hex/capabilities", nil, 200).Body.String()
	for _, expected := range []string{`"identity":true`, `"accessControl":true`} {
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

func TestAccessEntryValidation(t *testing.T) {
	server, _ := setupWithAccess(t)
	owner := principalHeaders("owner-id")

	invalid := [][]byte{
		[]byte(`[]`),
		[]byte(`{"owners":["owner-id"],"groups":[" spaced value"]}`),
		[]byte(fmt.Appendf(nil, `{"owners":["owner-id"],"groups":[%s"last"]}`, strings.Repeat(`"g",`, 64))),
	}
	for _, body := range invalid {
		requestAs(t, server, owner, "PUT", "/api/hex/sites/demo/access", body, 400)
	}

	// Owners without groups reserve a name without restricting viewers.
	reserved := []byte(`{"owners":["owner-id"]}`)
	requestAs(t, server, owner, "PUT", "/api/hex/sites/demo/access", reserved, 200)
	requestAs(t, server, principalHeaders("anyone"), "GET", "/api/sites/demo/files", nil, 200)
}

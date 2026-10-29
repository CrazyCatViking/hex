package hex_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	hex "github.com/crazycatviking/hex/server"
	"github.com/crazycatviking/hex/server/providers/easyauth"
	"github.com/crazycatviking/hex/server/providers/local"
	"github.com/crazycatviking/hex/server/providers/memory"
)

const privateAdminTestPolicy = `{"version":2,"admission":{"owners":true},"grants":{"pages":{"kind":"path","resource":"*","operations":["read"],"subjects":{"owners":true}}}}`

func grantTemporaryAccess(t *testing.T, server http.Handler, admin http.Header, site, scope string) {
	t.Helper()
	formRequest(t, server, admin, "POST", "/api/hex/admin/sites/"+site+"/access", url.Values{"scope": {scope}, "reason": {"Investigating a reported incident"}}, 303)
}

func TestAdminAccessRequiresExplicitIndependentTemporaryGrants(t *testing.T) {
	server, database := setupPortal(t)
	owner := principalHeaders("owner")
	admin := principalHeaders("operator", "admin-group")
	publishSite(t, server, owner, "private-app", map[string]string{
		"index.html":  "secret app",
		"favicon.svg": `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 1 1"><path d="M0 0h1v1H0z"/></svg>`,
	}, privateAdminTestPolicy)
	staticHeaders := admin.Clone()
	staticHeaders.Set("X-Hex-Site", "private-app")
	staticHeaders.Set("X-Hex-Path", "/index.html")
	requestAs(t, server, staticHeaders, "GET", "/api/hex/authz", nil, 403)
	requestAs(t, server, admin, "GET", "/api/hex/sites/private-app/icon", nil, 403)
	if _, err := database.Put(context.Background(), "private-app", "records", "secret", json.RawMessage(`{"private":"canary"}`), hex.WriteOptions{}); err != nil {
		t.Fatal(err)
	}
	before := showAccess(t, server, owner, "private-app")
	for _, path := range []string{"/", "/library", "/api/sites"} {
		if body := requestAs(t, server, admin, "GET", path, nil, 200).Body.String(); strings.Contains(body, "http://private-app.example.com/") {
			t.Fatalf("private app leaked through %s", path)
		}
	}
	for _, path := range []string{"/api/hex/sites/private-app/access", "/api/hex/sites/private-app/history", "/api/sites/private-app/db/records/secret", "/api/hex/manage/sites/private-app/collections"} {
		requestAs(t, server, admin, "GET", path, nil, 403)
	}
	locked := requestAs(t, server, admin, "GET", "/manage/private-app?tab=data", nil, 200).Body.String()
	if !strings.Contains(locked, "do not currently have management access") || strings.Contains(locked, "canary") {
		t.Fatal("restricted admin entry exposed app content")
	}
	requestAs(t, server, owner, "POST", "/api/hex/admin/sites/private-app/access", nil, 403)
	grantTemporaryAccess(t, server, admin, "private-app", "management")
	page := requestAs(t, server, admin, "GET", "/manage/private-app?tab=settings", nil, 200).Body.String()
	if !strings.Contains(page, "Temporary admin access is active") || !strings.Contains(page, "End access") {
		t.Fatal("active grant missing from management page")
	}
	requestAs(t, server, admin, "GET", "/api/hex/sites/private-app/access", nil, 200)
	requestAs(t, server, admin, "PUT", "/api/hex/sites/private-app/access", encode(t, before), 200)
	publishPublication(t, server, admin, "private-app", map[string]string{"index.html": "updated by an explicitly elevated administrator"}, publication{
		Files: manifest(map[string]string{"index.html": "updated by an explicitly elevated administrator"}),
	})
	requestAs(t, server, admin, "GET", "/api/hex/sites/private-app/history", nil, 200)
	requestAs(t, server, admin, "GET", "/api/sites/private-app/db/records/secret", nil, 403)
	requestAs(t, server, staticHeaders, "GET", "/api/hex/authz", nil, 403)
	requestAs(t, server, admin, "GET", "/api/hex/sites/private-app/icon", nil, 403)
	grantTemporaryAccess(t, server, admin, "private-app", "app-read")
	requestAs(t, server, staticHeaders, "GET", "/api/hex/authz", nil, 204)
	requestAs(t, server, admin, "GET", "/api/hex/sites/private-app/icon", nil, 200)
	requestAs(t, server, admin, "GET", "/api/sites/private-app/db/records/secret", nil, 200)
	requestAs(t, server, admin, "PUT", "/api/sites/private-app/db/records/secret", []byte(`{"changed":true}`), 403)
	requestAs(t, server, admin, "DELETE", "/api/sites/private-app/db/records/secret", nil, 403)
	requestAs(t, server, admin, "PUT", "/api/sites/private-app/files/report.txt", []byte("must not upload"), 403)
	permissions := requestAs(t, server, admin, "GET", "/api/hex/sites/private-app/permissions", nil, 200).Body.String()
	if !strings.Contains(permissions, `"read":"all"`) || strings.Contains(permissions, `"role":"owner"`) {
		t.Fatalf("temporary access was misdescribed: %s", permissions)
	}
	grants, err := database.ListAdminAccess(context.Background(), hex.AdminAccessQuery{Site: "private-app", ActiveAt: time.Now(), Limit: 25})
	if err != nil || len(grants) != 2 {
		t.Fatalf("missing active grants: %v %v", grants, err)
	}
	for _, grant := range grants {
		if grant.ExpiresAt.Sub(grant.StartedAt) != time.Hour {
			t.Fatal("unexpected default grant duration")
		}
		if grant.Scope == "management" {
			formRequest(t, server, owner, "POST", "/api/hex/admin/sites/private-app/access/"+grant.ID+"/revoke", nil, 303)
		}
	}
	requestAs(t, server, admin, "GET", "/api/hex/sites/private-app/access", nil, 403)
	requestAs(t, server, admin, "GET", "/api/sites/private-app/db/records/secret", nil, 200)
	grantTemporaryAccess(t, server, admin, "private-app", "app-write")
	requestAs(t, server, admin, "PUT", "/api/sites/private-app/db/records/secret", []byte(`{"changed":true}`), 200)
	requestAs(t, server, admin, "GET", "/api/hex/sites/private-app/access", nil, 403)
	after := showAccess(t, server, owner, "private-app")
	if !reflect.DeepEqual(before, after) {
		t.Fatal("temporary privileges altered the saved site policy")
	}
	audit := requestAs(t, server, owner, "GET", "/manage/private-app?tab=admin-access", nil, 200).Body.String()
	if !strings.Contains(audit, "Investigating a reported incident") || !strings.Contains(audit, "ended") {
		t.Fatal("owner cannot see the grant/revocation audit")
	}
	for _, grant := range grants {
		if grant.Scope == "app-read" {
			formRequest(t, server, admin, "POST", "/api/hex/admin/sites/private-app/access/"+grant.ID+"/revoke", nil, 303)
		}
	}
	active, err := database.ListAdminAccess(context.Background(), hex.AdminAccessQuery{Site: "private-app", ActiveAt: time.Now(), Limit: 25})
	if err != nil {
		t.Fatal(err)
	}
	for _, grant := range active {
		formRequest(t, server, admin, "POST", "/api/hex/admin/sites/private-app/access/"+grant.ID+"/revoke", nil, 303)
	}
	requestAs(t, server, admin, "GET", "/api/sites/private-app/db/records/secret", nil, 403)
	requestAs(t, server, staticHeaders, "GET", "/api/hex/authz", nil, 403)
	requestAs(t, server, admin, "GET", "/api/hex/sites/private-app/icon", nil, 403)
}

func TestTemporaryManagementRecoversPolicylessPublishedSitesWithoutClaimingOwnership(t *testing.T) {
	store, err := local.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Error(err)
		}
	})
	server := hex.New(hex.Config{
		Sites: store, Publisher: store, Access: memory.NewAccessStore(), Database: memory.NewDatabase(),
		Identity: easyauth.Resolver{}, AdminGroups: []string{"admin-group"}, SiteBaseURL: "http://example.com",
	})
	admin := principalHeaders("operator", "admin-group")
	if err := store.Put(t.Context(), "public/sites/legacy/index.html", strings.NewReader("legacy app")); err != nil {
		t.Fatal(err)
	}
	requestAs(t, server, admin, "GET", "/api/hex/sites/legacy/access", nil, 404)
	page := requestAs(t, server, admin, "GET", "/manage/legacy", nil, 200).Body.String()
	if !strings.Contains(page, "Grant yourself temporary access") {
		t.Fatal("policyless publication has no recovery entry")
	}
	formRequest(t, server, admin, "POST", "/api/hex/admin/sites/missing/access", url.Values{
		"scope": {"management"}, "reason": {"Recover an older publication"},
	}, 404)
	grantTemporaryAccess(t, server, admin, "legacy", "management")
	requestAs(t, server, admin, "GET", "/manage/legacy?tab=settings", nil, 200)
	files := map[string]string{"index.html": "updated legacy app"}
	publishPublication(t, server, admin, "legacy", files, publication{Files: manifest(files)})
	requestAs(t, server, admin, "GET", "/api/hex/sites/legacy/access", nil, 404)
	requestAs(t, server, admin, "DELETE", "/api/hex/sites/legacy", nil, 204)
}

type unavailableAdminAccess struct {
	*memory.Database
}

func (unavailableAdminAccess) ListAdminAccess(context.Context, hex.AdminAccessQuery) ([]hex.AdminAccessGrant, error) {
	return nil, errors.New("temporary-access store is unavailable")
}

func TestTemporaryAdminAccessFailsClosedWithoutReliableStorage(t *testing.T) {
	for _, mode := range []string{"unsupported", "outage", "automation"} {
		t.Run(mode, func(t *testing.T) {
			config, _, policies := discoveryFixture(t, 1)
			if err := policies.PutSiteAccess(t.Context(), "app-0000", hex.SiteAccess{
				Version: 2, Owners: []string{"user:owner"}, Admission: hex.Subjects{Owners: true},
			}); err != nil {
				t.Fatal(err)
			}
			config.AdminGroups = []string{"admins"}
			config.Database = nil
			config.Files = memory.NewStore()
			wantGrant, wantRead := 501, 403
			if mode == "outage" {
				config.AdminAccess = unavailableAdminAccess{Database: memory.NewDatabase()}
				wantRead = 500
			}
			if mode == "automation" {
				config.Identity = hex.StaticIdentity{Identity: hex.Identity{Provider: "automation", ID: "runner", Groups: []string{"admins"}}}
				config.AdminAccess = memory.NewDatabase()
				wantGrant = 403
			}
			server := hex.New(config)
			admin := principalHeaders("operator", "admins")
			requestAs(t, server, admin, "GET", "/api/sites/app-0000/files/private.txt", nil, wantRead)
			if mode != "outage" {
				formRequest(t, server, admin, "POST", "/api/hex/admin/sites/app-0000/access", url.Values{
					"scope": {"management"}, "reason": {"Investigating a reported issue"},
				}, wantGrant)
			}
		})
	}
}

func TestTemporaryAdminAccessRevokesExistingRealtimeConnections(t *testing.T) {
	for _, direction := range []string{"receive", "send"} {
		t.Run(direction, func(t *testing.T) {
			config, _, policies := discoveryFixture(t, 1)
			if err := policies.PutSiteAccess(t.Context(), "app-0000", hex.SiteAccess{Version: 2, Owners: []string{"user:owner"}, Admission: hex.Subjects{Owners: true}}); err != nil {
				t.Fatal(err)
			}
			config.AdminGroups = []string{"admins"}
			realtime := memory.NewRealtime()
			config.Realtime = realtime
			server := hex.New(config)
			admin := principalHeaders("operator", "admins")
			grantTemporaryAccess(t, server, admin, "app-0000", "app-write")
			transport := httptest.NewServer(server)
			defer transport.Close()
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			endpoint := "ws" + strings.TrimPrefix(transport.URL, "http") + "/api/sites/app-0000/realtime/updates"
			connection, _, err := websocket.Dial(ctx, endpoint, &websocket.DialOptions{HTTPHeader: admin})
			if err != nil {
				t.Fatal(err)
			}
			defer connection.CloseNow()
			grants, err := config.Database.(*memory.Database).ListAdminAccess(ctx, hex.AdminAccessQuery{Site: "app-0000", ActiveAt: time.Now(), Limit: 25})
			if err != nil || len(grants) != 1 {
				t.Fatalf("missing realtime grant: %v %v", grants, err)
			}
			formRequest(t, server, admin, "POST", "/api/hex/admin/sites/app-0000/access/"+grants[0].ID+"/revoke", nil, 303)
			payload := []byte(`{"secret":true}`)
			if direction == "send" {
				err = connection.Write(ctx, websocket.MessageText, payload)
			} else {
				err = realtime.Publish(ctx, "app-0000/updates", payload)
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, _, err := connection.Read(ctx); websocket.CloseStatus(err) != websocket.StatusPolicyViolation {
				t.Fatalf("revoked connection retained %s authority: %v", direction, err)
			}
		})
	}
}

func TestAdminGrantValidationRevocationIsolationAndExpiry(t *testing.T) {
	server, database := setupPortal(t)
	owner, admin := principalHeaders("owner"), principalHeaders("operator", "admin-group")
	publishSite(t, server, owner, "private-app", map[string]string{"index.html": "app"}, privateAdminTestPolicy)
	publishSite(t, server, owner, "other-app", map[string]string{"index.html": "app"}, privateAdminTestPolicy)
	for _, values := range []url.Values{
		{"scope": {"owner"}, "reason": {"reason"}},
		{"scope": {"management"}, "reason": {"reason"}, "minutes": {"241"}},
		{"scope": {"management"}, "reason": {"reason"}, "minutes": {"0"}},
		{"scope": {"management"}, "reason": {"  "}},
	} {
		formRequest(t, server, admin, "POST", "/api/hex/admin/sites/private-app/access", values, 400)
	}
	grantTemporaryAccess(t, server, admin, "private-app", "app-read")
	requestAs(t, server, admin, "GET", "/api/sites/other-app/db/records/secret", nil, 403)
	grants, err := database.ListAdminAccess(context.Background(), hex.AdminAccessQuery{Site: "private-app", ActiveAt: time.Now(), Limit: 25})
	if err != nil || len(grants) != 1 {
		t.Fatalf("missing grant: %v %v", grants, err)
	}
	grant := grants[0]
	formRequest(t, server, principalHeaders("another", "admin-group"), "POST", "/api/hex/admin/sites/private-app/access/"+grant.ID+"/revoke", nil, 403)
	formRequest(t, server, principalHeaders("stranger"), "POST", "/api/hex/admin/sites/private-app/access/"+grant.ID+"/revoke", nil, 403)
	requestAs(t, server, principalHeaders("operator"), "GET", "/api/sites/private-app/db/records/secret", nil, 403)
	formRequest(t, server, admin, "POST", "/api/hex/admin/sites/private-app/access/"+grant.ID+"/revoke", nil, 303)
	grant.ID = "expired-grant"
	grant.StartedAt = time.Now().Add(-2 * time.Hour)
	grant.ExpiresAt = time.Now().Add(-time.Hour)
	if err := database.GrantAdminAccess(context.Background(), grant); err != nil {
		t.Fatal(err)
	}
	requestAs(t, server, admin, "GET", "/api/sites/private-app/db/records/secret", nil, 403)
	if page := requestAs(t, server, admin, "GET", "/library", nil, 200).Body.String(); strings.Contains(page, "http://private-app.example.com/") {
		t.Fatal("expired grant remained in warm discovery")
	}
	// Re-granting replaces only the same scope, retaining the complete audit.
	var workers sync.WaitGroup
	for range 5 {
		workers.Go(func() { grantTemporaryAccess(t, server, admin, "private-app", "management") })
	}
	workers.Wait()
	active, err := database.ListAdminAccess(context.Background(), hex.AdminAccessQuery{Site: "private-app", ActiveAt: time.Now(), Limit: 25})
	if err != nil {
		t.Fatal(err)
	}
	if len(active) != 1 {
		t.Fatalf("concurrent renewals produced %d active leases", len(active))
	}
	all, err := database.ListAdminAccess(context.Background(), hex.AdminAccessQuery{Site: "private-app", Limit: 25})
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 7 {
		t.Fatalf("lost audit history: %d", len(all))
	}
}

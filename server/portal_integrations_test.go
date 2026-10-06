package hex_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	hex "github.com/crazycatviking/hex/server"
	"github.com/crazycatviking/hex/server/providers/easyauth"
	"github.com/crazycatviking/hex/server/providers/memory"
	"golang.org/x/oauth2"
)

func setupPortalIntegrations(t *testing.T, withStore bool, configure ...func(*hex.Config)) (*hex.Server, *memory.IntegrationStore) {
	t.Helper()
	registry := new(hex.IntegrationRegistry)
	if err := registry.RegisterConnector(hex.Connector{
		Name: "archive-account", Title: "Archive account", OAuth2: oauth2.Config{ClientID: "client", Endpoint: oauth2.Endpoint{
			AuthURL: "https://archive.example.test/authorize", TokenURL: "https://archive.example.test/token",
		}},
	}); err != nil {
		t.Fatal(err)
	}
	handler := func(context.Context, hex.IntegrationCall, json.RawMessage) (any, error) {
		t.Error("the portal invoked an integration handler")
		return nil, errors.New("discovery must not invoke endpoints")
	}
	for _, integration := range []hex.Integration{
		{Name: "ledger", Title: "Team ledger", Description: "Registered financial operations.", RequiresApproval: true, Endpoints: []hex.IntegrationEndpoint{
			{Name: "balances", Description: "Read balances.", InputSchema: objectSchema, OutputSchema: objectSchema, Handler: handler},
			{Name: "adjust", Description: "Change a balance.", Permission: "finance.write", Write: true, InputSchema: objectSchema, OutputSchema: objectSchema, Handler: handler},
		}},
		{Name: "chat", Title: "Team chat", Description: "Registered messaging operations.", Endpoints: []hex.IntegrationEndpoint{
			{Name: "channels", Description: "Read channels.", InputSchema: objectSchema, OutputSchema: objectSchema, Handler: handler},
		}},
		{Name: "archive", Title: "Team archive", Connector: "archive-account", Endpoints: []hex.IntegrationEndpoint{
			{Name: "pages", Description: "Read archive pages.", InputSchema: objectSchema, OutputSchema: objectSchema, Handler: handler},
		}},
	} {
		if err := registry.Register(integration); err != nil {
			t.Fatal(err)
		}
	}
	access := memory.NewAccessStore()
	if err := access.PutSiteAccess(context.Background(), "demo", hex.SiteAccess{
		Owners: []string{"user:owner"}, Editors: []string{"user:editor"}, Viewers: []string{"user:viewer"},
	}); err != nil {
		t.Fatal(err)
	}
	config := hex.Config{
		Identity: easyauth.Resolver{}, Access: access, AdminGroups: []string{"admin-group"}, SiteBaseURL: "http://example.com",
		Integrations: registry, IntegrationGrants: []hex.IntegrationGrant{{Principal: "user:owner", Permissions: []string{"ledger.balances", "chat.channels"}}},
	}
	var store *memory.IntegrationStore
	if withStore {
		store = memory.NewIntegrationStore()
		config.IntegrationStore = store
	}
	for _, update := range configure {
		update(&config)
	}
	return hex.New(config), store
}

func integrationFormHeaders(identity string, admin bool) http.Header {
	headers := roleHeaders(identity)
	if admin {
		headers = principalHeaders(identity, "admin-group")
	}
	headers.Set("HX-Request", "true")
	return headers
}

const portalLedgerApproval = "/api/hex/manage/sites/demo/integrations/ledger/approval"

func containsIntegrationMarkup(markup, expected string) bool {
	markup = strings.Join(strings.Fields(markup), " ")
	markup = strings.NewReplacer("> ", ">", " <", "<").Replace(markup)
	return strings.Contains(markup, expected)
}

func TestPortalIntegrationApprovalWorkflowWithoutSitesStore(t *testing.T) {
	server, store := setupPortalIntegrations(t, true)
	owner := integrationFormHeaders("owner", false)
	admin := integrationFormHeaders("admin", true)
	page := requestAs(t, server, owner, "GET", "/integrations?site=demo", nil, 200).Body.String()
	for _, expected := range []string{"Integrations for demo", "Team ledger", "finance.write", "Request approval", `hx-post="` + portalLedgerApproval + `/request"`, `hx-target="#site-integrations"`} {
		if !containsIntegrationMarkup(page, expected) {
			t.Fatalf("site integration page lacks %q: %s", expected, page)
		}
	}
	if _, err := store.GetIntegrationApproval(context.Background(), "demo", "ledger"); !errors.Is(err, hex.ErrNotFound) {
		t.Fatal("GET created an approval")
	}
	reason := "Pipeline <script>alert(1)</script>"
	fragment := formRequest(t, server, owner, "POST", portalLedgerApproval+"/request", url.Values{"reason": {reason}}, 200)
	if !containsIntegrationMarkup(fragment, "Awaiting approval") || !containsIntegrationMarkup(fragment, "Withdraw request") || strings.Contains(fragment, "<script>alert(1)</script>") {
		t.Fatalf("request was not safely rendered: %s", fragment)
	}
	approval, err := store.GetIntegrationApproval(context.Background(), "demo", "ledger")
	if err != nil || approval.Status != hex.ApprovalRequested || approval.Reason != reason || approval.RequestedBy == nil || approval.RequestedBy.ID != "owner" {
		t.Fatalf("request was not persisted: %+v %v", approval, err)
	}
	page = requestAs(t, server, admin, "GET", "/integrations", nil, 200).Body.String()
	for _, expected := range []string{"Integration approval review", "Awaiting approval", `hx-post="` + portalLedgerApproval + `/approve?view=catalog"`, "Not granted"} {
		if !containsIntegrationMarkup(page, expected) {
			t.Fatalf("admin catalog lacks %q: %s", expected, page)
		}
	}
	approved := formRequest(t, server, admin, "POST", portalLedgerApproval+"/approve?view=catalog", nil, 200)
	if !containsIntegrationMarkup(approved, "Approved") || !containsIntegrationMarkup(approved, "Revoke approval") || strings.Contains(approved, "<!doctype html>") {
		t.Fatalf("approval did not update its review fragment: %s", approved)
	}
	approval, err = store.GetIntegrationApproval(context.Background(), "demo", "ledger")
	if err != nil || approval.Status != hex.ApprovalApproved || approval.DecidedBy == nil || approval.DecidedBy.ID != "admin" || approval.Reason != reason {
		t.Fatalf("approval was not persisted: %+v %v", approval, err)
	}
	fragment = requestAs(t, server, owner, "GET", "/api/hex/manage/sites/demo/integrations", nil, 200).Body.String()
	if !containsIntegrationMarkup(fragment, "Approved") || !containsIntegrationMarkup(fragment, ">Allowed<") || strings.Contains(fragment, "<!doctype html>") {
		t.Fatalf("site fragment did not reflect approval and caller grant: %s", fragment)
	}
	formRequest(t, server, owner, "POST", portalLedgerApproval+"/revoke", nil, http.StatusForbidden)
	// The JSON API and portal share the same idempotent request mutation.
	requestAs(t, server, owner, "POST", "/api/hex/sites/demo/integrations/ledger/approval", []byte(`{"reason":"must not downgrade approval"}`), 200)
	approval, err = store.GetIntegrationApproval(context.Background(), "demo", "ledger")
	if err != nil || approval.Status != hex.ApprovalApproved || approval.Reason != reason {
		t.Fatalf("request downgraded an approved integration: %+v %v", approval, err)
	}
	removed := formRequest(t, server, admin, "POST", portalLedgerApproval+"/revoke?view=catalog", nil, 200)
	if !strings.Contains(removed, "There are no integration approvals to review.") {
		t.Fatalf("revocation did not update its review fragment: %s", removed)
	}
	if _, err := store.GetIntegrationApproval(context.Background(), "demo", "ledger"); !errors.Is(err, hex.ErrNotFound) {
		t.Fatalf("revocation did not remove approval: %v", err)
	}
	formRequest(t, server, owner, "POST", portalLedgerApproval+"/request", url.Values{"reason": {"new request"}}, 200)
	withdrawn := formRequest(t, server, owner, "POST", portalLedgerApproval+"/revoke", nil, 200)
	if !containsIntegrationMarkup(withdrawn, "Not requested") || !containsIntegrationMarkup(withdrawn, "Request approval") {
		t.Fatalf("owner could not withdraw a pending request: %s", withdrawn)
	}
}

func TestPortalIntegrationCatalogUsesRegisteredMetadata(t *testing.T) {
	server, store := setupPortalIntegrations(t, true)
	owner := integrationFormHeaders("owner", false)
	page := requestAs(t, server, owner, "GET", "/integrations", nil, 200)
	for _, expected := range []string{"Team ledger", "Registered financial operations.", "Change a balance.", "finance.write", "Team chat", "Archive account", "Site approval required", "No site approval required", "Not granted", "Granted"} {
		if !containsIntegrationMarkup(page.Body.String(), expected) {
			t.Fatalf("registered metadata %q was not discovered: %s", expected, page.Body.String())
		}
	}
	if strings.Contains(page.Body.String(), "Integration approval review") || strings.Contains(page.Body.String(), `hx-post=`) {
		t.Fatal("a non-admin catalog exposes approval review or mutations without a site")
	}
	if page.Header().Get("Cache-Control") != "no-store" || page.Header().Get("Content-Security-Policy") == "" {
		t.Fatal("catalog lacks portal response protections")
	}
	// A persisted approval for an integration removed from the registry has no
	// current capability to approve/revoke and must not produce active controls.
	if err := store.PutIntegrationApproval(context.Background(), hex.IntegrationApproval{Site: "demo", Integration: "removed", Status: hex.ApprovalRequested}); err != nil {
		t.Fatal(err)
	}
	admin := integrationFormHeaders("admin", true)
	review := requestAs(t, server, admin, "GET", "/api/hex/manage/integration-approvals", nil, 200).Body.String()
	if strings.Contains(review, "/integrations/removed/") || !strings.Contains(review, "There are no integration approvals to review.") {
		t.Fatalf("review contains unavailable integration controls: %s", review)
	}
}

func TestPortalIntegrationPermissionsAndCapabilities(t *testing.T) {
	server, store := setupPortalIntegrations(t, true)
	owner := integrationFormHeaders("owner", false)
	admin := integrationFormHeaders("admin", true)
	requestAs(t, server, nil, "GET", "/integrations", nil, 401)
	requestAs(t, server, nil, "GET", "/api/hex/manage/sites/demo/integrations", nil, 401)
	for _, person := range []string{"editor", "viewer", "outsider"} {
		headers := integrationFormHeaders(person, false)
		requestAs(t, server, headers, "GET", "/integrations?site=demo", nil, 403)
		requestAs(t, server, headers, "GET", "/api/hex/manage/sites/demo/integrations", nil, 403)
		for _, operation := range []string{"request", "approve", "revoke"} {
			formRequest(t, server, headers, "POST", portalLedgerApproval+"/"+operation, nil, 403)
		}
	}
	for _, path := range []string{"/integrations?site=Bad_Name", "/integrations?site=", "/api/hex/manage/sites/Bad_Name/integrations"} {
		requestAs(t, server, owner, "GET", path, nil, 400)
	}
	requestAs(t, server, owner, "GET", "/integrations?site=unowned", nil, 403)
	requestAs(t, server, owner, "GET", "/api/hex/manage/integration-approvals", nil, 403)
	formRequest(t, server, owner, "POST", portalLedgerApproval+"/approve", nil, 403)
	formRequest(t, server, owner, "POST", portalLedgerApproval+"/request?view=catalog", nil, 403)
	formRequest(t, server, owner, "POST", portalLedgerApproval+"/request?view=other", nil, 400)
	formRequest(t, server, owner, "POST", "/api/hex/manage/sites/demo/integrations/missing/approval/request", nil, 404)
	formRequest(t, server, admin, "POST", "/api/hex/manage/sites/demo/integrations/chat/approval/approve", nil, 400)
	formRequest(t, server, owner, "POST", "/api/hex/manage/sites/Bad_Name/integrations/ledger/approval/request", nil, 400)
	formRequest(t, server, owner, "POST", portalLedgerApproval+"/request", url.Values{"reason": {strings.Repeat("x", 9<<10)}}, 400)
	for _, operation := range []string{"request", "approve", "revoke"} {
		requestAs(t, server, admin, "GET", portalLedgerApproval+"/"+operation, nil, 405)
	}
	if approvals, err := store.ListIntegrationApprovals(context.Background()); err != nil || len(approvals) != 0 {
		t.Fatalf("refused requests mutated approvals: %+v %v", approvals, err)
	}
	r := httptest.NewRequest("GET", "/integrations", nil)
	r.Host = "demo.example.com"
	r.Header = owner
	w := httptest.NewRecorder()
	server.ServeHTTP(w, r)
	if w.Code != 404 {
		t.Fatalf("catalog served on a site host: %d", w.Code)
	}
}

func TestPortalIntegrationDiscoveryWithoutApprovalStore(t *testing.T) {
	server, _ := setupPortalIntegrations(t, false)
	owner := integrationFormHeaders("owner", false)
	page := requestAs(t, server, owner, "GET", "/integrations?site=demo", nil, 200).Body.String()
	if !strings.Contains(page, "Team ledger") || !strings.Contains(page, "approval management is not configured") || strings.Contains(page, "hx-post=") {
		t.Fatalf("missing capability was not reflected in the page: %s", page)
	}
	for _, operation := range []string{"request", "approve", "revoke"} {
		formRequest(t, server, owner, "POST", portalLedgerApproval+"/"+operation, nil, 404)
	}
	requestAs(t, server, integrationFormHeaders("admin", true), "GET", "/api/hex/manage/integration-approvals", nil, 404)
}

func TestPortalIntegrationAdminRequestIsImmediatelyApproved(t *testing.T) {
	server, store := setupPortalIntegrations(t, true)
	admin := principalHeaders("admin", "admin-group")
	formRequest(t, server, admin, "POST", portalLedgerApproval+"/request", url.Values{"reason": {"Admin setup"}}, http.StatusOK)
	approval, err := store.GetIntegrationApproval(context.Background(), "demo", "ledger")
	if err != nil || approval.Status != hex.ApprovalApproved || approval.RequestedBy == nil || approval.RequestedBy.ID != "admin" || approval.DecidedBy == nil || approval.DecidedBy.ID != "admin" {
		t.Fatalf("admin request was not immediately approved: %+v %v", approval, err)
	}
	page := requestAs(t, server, admin, "GET", "/integrations?site=demo", nil, 200).Body.String()
	if !containsIntegrationMarkup(page, "Approved") || !containsIntegrationMarkup(page, "Revoke approval") || containsIntegrationMarkup(page, ">Request approval<") {
		t.Fatalf("approved site controls are incorrect: %s", page)
	}
}

func TestPortalIntegrationRoutesNeedIdentityAndRegistry(t *testing.T) {
	for name, configure := range map[string]func(*hex.Config){
		"no identity":     func(config *hex.Config) { config.Identity = nil },
		"no integrations": func(config *hex.Config) { config.Integrations = nil },
	} {
		t.Run(name, func(t *testing.T) {
			server, _ := setupPortalIntegrations(t, true, configure)
			admin := integrationFormHeaders("admin", true)
			requestAs(t, server, admin, "GET", "/integrations", nil, 404)
			requestAs(t, server, admin, "GET", "/api/hex/manage/sites/demo/integrations", nil, 404)
			formRequest(t, server, admin, "POST", portalLedgerApproval+"/approve", nil, 404)
		})
	}
}

func TestPortalIntegrationAdminCatalogDoesNotNeedAccessStore(t *testing.T) {
	server, store := setupPortalIntegrations(t, true, func(config *hex.Config) { config.Access = nil })
	if err := store.PutIntegrationApproval(context.Background(), hex.IntegrationApproval{Site: "demo", Integration: "ledger", Status: hex.ApprovalRequested}); err != nil {
		t.Fatal(err)
	}
	admin := integrationFormHeaders("admin", true)
	page := requestAs(t, server, admin, "GET", "/integrations", nil, 200).Body.String()
	if !containsIntegrationMarkup(page, "Integration approval review") || !containsIntegrationMarkup(page, "Awaiting approval") {
		t.Fatalf("admin catalog incorrectly depends on the access or site store: %s", page)
	}
	formRequest(t, server, admin, "POST", portalLedgerApproval+"/approve?view=catalog", nil, 200)
}

func TestPortalIntegrationFormsRequireRequestMarkerAndSameOrigin(t *testing.T) {
	server, store := setupPortalIntegrations(t, true)
	for _, crossOrigin := range []bool{false, true} {
		r := httptest.NewRequest("POST", portalLedgerApproval+"/request", strings.NewReader("reason=Demo"))
		r.Header = roleHeaders("owner")
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		if crossOrigin {
			r.Header.Set("X-Hex-Request", "1")
			r.Header.Set("Origin", "https://another.example.test")
		}
		w := httptest.NewRecorder()
		server.ServeHTTP(w, r)
		if w.Code != http.StatusForbidden {
			t.Fatalf("unsafe form request was accepted (cross-origin=%t): %d %s", crossOrigin, w.Code, w.Body.String())
		}
	}
	if approvals, err := store.ListIntegrationApprovals(context.Background()); err != nil || len(approvals) != 0 {
		t.Fatalf("unsafe form requests mutated approvals: %+v %v", approvals, err)
	}
}

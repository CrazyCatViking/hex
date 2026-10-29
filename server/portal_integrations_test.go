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
	"time"

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
	for _, expected := range []string{"Integration approval review", "Awaiting approval", `hx-post="` + portalLedgerApproval + `/approve?view=catalog"`, "No permission"} {
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
	for _, expected := range []string{"Team ledger", "Registered financial operations.", "Team chat", "Approval required", "All sites", "No permission", "All features granted", "1 of 2 features", `href="/integrations/ledger"`, `data-integration-card="archive"`, "Linked login"} {
		if !containsIntegrationMarkup(page.Body.String(), expected) {
			t.Fatalf("registered metadata %q was not discovered: %s", expected, page.Body.String())
		}
	}
	for _, unwanted := range []string{`id="integration-site"`, "Change a balance.", "finance.write", "Input schema"} {
		if strings.Contains(page.Body.String(), unwanted) {
			t.Fatalf("catalog eagerly renders site lookup or endpoint details: %q", unwanted)
		}
	}
	detail := requestAs(t, server, owner, "GET", "/integrations/ledger", nil, 200).Body.String()
	for _, expected := range []string{"Change a balance.", "finance.write", "ledger.adjust", "Restricted", "Permission needed", "Granted to you", "Site approval also required", "Input schema", "Output schema"} {
		if !containsIntegrationMarkup(detail, expected) {
			t.Fatalf("detail page lacks registered metadata %q: %s", expected, detail)
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

func integrationCardMarkup(t *testing.T, page, name string) string {
	t.Helper()
	_, rest, found := strings.Cut(page, `data-integration-card="`+name+`"`)
	if !found {
		t.Fatalf("integration card %q not found", name)
	}
	markup, _, found := strings.Cut(rest, "</article>")
	if !found {
		t.Fatalf("integration card %q has no closing tag", name)
	}
	return markup
}

func TestPortalIntegrationCatalogConnectionStates(t *testing.T) {
	for _, state := range []string{"unconfigured", "not connected", "connected", "expired"} {
		t.Run(state, func(t *testing.T) {
			server, store := setupPortalIntegrations(t, true, func(config *hex.Config) {
				config.IntegrationGrants = append(config.IntegrationGrants, hex.IntegrationGrant{Principal: "user:owner", Permissions: []string{"archive.pages"}})
				if state == "unconfigured" {
					return
				}
				sealer, err := hex.NewKeySealer(make([]byte, 32))
				if err != nil {
					t.Fatal(err)
				}
				config.CredentialSealer = sealer
			})
			if state == "connected" || state == "expired" {
				lastUsed := time.Now()
				if state == "expired" {
					lastUsed = lastUsed.Add(-91 * 24 * time.Hour)
				}
				if err := store.PutCredential(context.Background(), hex.CredentialRecord{Owner: "owner", Connector: "archive-account", Account: "Owner’s archive", Sealed: []byte("never rendered"), ConnectedAt: lastUsed, LastUsedAt: lastUsed}); err != nil {
					t.Fatal(err)
				}
			}
			owner := integrationFormHeaders("owner", false)
			page := requestAs(t, server, owner, "GET", "/integrations", nil, 200).Body.String()
			card := integrationCardMarkup(t, page, "archive")
			expectedAccess, expectedLogin := "Link login to use", "Not connected"
			if state == "unconfigured" {
				expectedAccess, expectedLogin = "Login unavailable", "Unavailable"
			} else if state == "connected" {
				expectedAccess, expectedLogin = "All features granted", "Connected"
			}
			for _, expected := range []string{expectedAccess, expectedLogin, "Not supported", "Supported · as you", `data-granted="1"`} {
				if !containsIntegrationMarkup(card, expected) {
					t.Fatalf("connection state lacks %q: %s", expected, card)
				}
			}
			detail := requestAs(t, server, owner, "GET", "/api/hex/manage/integrations/archive", nil, 200)
			if detail.Header().Get("X-Hex-Portal") != "1" || detail.Header().Get("Cache-Control") != "no-store" || strings.Contains(detail.Body.String(), "<!doctype html>") {
				t.Fatal("detail fragment must be identifiable, private and not a full page")
			}
			if strings.Contains(detail.Body.String(), "never rendered") {
				t.Fatal("catalog leaked credential bytes")
			}
			if state == "not connected" || state == "expired" {
				for _, expected := range []string{"Link login", "/api/hex/connections/archive-account/start?return=http", "%23archive", "archive.pages"} {
					if !containsIntegrationMarkup(detail.Body.String(), expected) {
						t.Fatalf("connection details lack %q: %s", expected, detail.Body.String())
					}
				}
			} else if state == "connected" && !containsIntegrationMarkup(detail.Body.String(), "Owner’s archive") {
				t.Fatal("connected account was not shown")
			}
			if state == "connected" {
				other := requestAs(t, server, integrationFormHeaders("viewer", false), "GET", "/api/hex/manage/integrations/archive", nil, 200).Body.String()
				if strings.Contains(other, "Owner’s archive") || !containsIntegrationMarkup(other, "Not connected") || !containsIntegrationMarkup(other, "0 of 1 permissions granted") {
					t.Fatal("catalog reused another viewer's connection or permissions")
				}
			}
		})
	}
}

type failingCatalogConnections struct {
	*memory.IntegrationStore
}

func (store failingCatalogConnections) GetCredential(context.Context, string, string) (hex.CredentialRecord, error) {
	return hex.CredentialRecord{}, errors.New("connection store unavailable")
}

func TestPortalIntegrationCatalogDoesNotHideConnectionStoreFailures(t *testing.T) {
	server, _ := setupPortalIntegrations(t, true, func(config *hex.Config) {
		sealer, err := hex.NewKeySealer(make([]byte, 32))
		if err != nil {
			t.Fatal(err)
		}
		config.CredentialSealer = sealer
		config.IntegrationStore = failingCatalogConnections{config.IntegrationStore.(*memory.IntegrationStore)}
	})
	for _, path := range []string{"/integrations", "/integrations/archive", "/api/hex/manage/integrations/archive"} {
		requestAs(t, server, integrationFormHeaders("owner", false), "GET", path, nil, http.StatusInternalServerError)
	}
}

func TestPortalIntegrationDetailsDoNotElevateAdminsOrSiteApprovals(t *testing.T) {
	server, store := setupPortalIntegrations(t, true)
	if err := store.PutIntegrationApproval(context.Background(), hex.IntegrationApproval{Site: "demo", Integration: "ledger", Status: hex.ApprovalApproved}); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/integrations/ledger", "/api/hex/manage/integrations/ledger"} {
		admin := requestAs(t, server, integrationFormHeaders("admin", true), "GET", path, nil, 200).Body.String()
		if !containsIntegrationMarkup(admin, "0 of 2 permissions granted") || strings.Count(admin, ">Permission needed<") != 2 {
			t.Fatalf("admin or site approval incorrectly granted endpoint access: %s", admin)
		}
		requestAs(t, server, nil, "GET", path, nil, 401)
		request := httptest.NewRequest("GET", path, nil)
		request.Host = "demo.example.com"
		request.Header = integrationFormHeaders("owner", false)
		response := httptest.NewRecorder()
		server.ServeHTTP(response, request)
		expectedStatus := http.StatusNotFound
		if strings.HasPrefix(path, "/api/") {
			expectedStatus = http.StatusForbidden
		}
		if response.Code != expectedStatus {
			t.Fatalf("details served on a site host: %d", response.Code)
		}
	}
	for _, path := range []string{"/integrations/missing", "/api/hex/manage/integrations/missing"} {
		requestAs(t, server, integrationFormHeaders("owner", false), "GET", path, nil, 404)
	}
}

func TestPortalIntegrationCatalogEscapesMetadataAndReportsAuditUnavailable(t *testing.T) {
	server, _ := setupPortalIntegrations(t, true, func(config *hex.Config) {
		if err := config.Integrations.Register(hex.Integration{
			Name: "audit", Title: `<img src=x onerror=alert(1)>`, Description: `<script>alert(2)</script>`, Audit: true,
			Endpoints: []hex.IntegrationEndpoint{{Name: "read", Description: `<script>alert(3)</script>`, InputSchema: json.RawMessage(`{"type":"object","description":"<script>alert(4)</script>"}`), OutputSchema: objectSchema, Handler: func(context.Context, hex.IntegrationCall, json.RawMessage) (any, error) {
				t.Fatal("discovery invoked an endpoint")
				return nil, nil
			}}},
		}); err != nil {
			t.Fatal(err)
		}
		config.IntegrationGrants = append(config.IntegrationGrants, hex.IntegrationGrant{Principal: "user:owner", Permissions: []string{"audit.read"}})
	})
	owner := integrationFormHeaders("owner", false)
	page := requestAs(t, server, owner, "GET", "/integrations", nil, 200).Body.String()
	card := integrationCardMarkup(t, page, "audit")
	if !containsIntegrationMarkup(card, "Unavailable") || !strings.Contains(page, "&lt;img") {
		t.Fatalf("unavailable audit integration was not safely summarized: %s", card)
	}
	detail := requestAs(t, server, owner, "GET", "/api/hex/manage/integrations/audit", nil, 200).Body.String()
	for _, markup := range []string{page, detail} {
		if strings.Contains(markup, "<img src=x") || strings.Contains(markup, "<script>alert(") {
			t.Fatal("untrusted catalog metadata was not escaped")
		}
	}
	if !containsIntegrationMarkup(detail, "required audit store is not configured") || !containsIntegrationMarkup(detail, "1 of 1 permissions granted") || !containsIntegrationMarkup(detail, "Unavailable") {
		t.Fatalf("audit availability was conflated with endpoint permissions: %s", detail)
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
	// Administration alone cannot request an integration as a site owner.
	formRequest(t, server, principalHeaders("admin", "admin-group"), "POST", portalLedgerApproval+"/request", nil, http.StatusForbidden)
	admin := principalHeaders("owner", "admin-group")
	formRequest(t, server, admin, "POST", portalLedgerApproval+"/request", url.Values{"reason": {"Admin setup"}}, http.StatusOK)
	approval, err := store.GetIntegrationApproval(context.Background(), "demo", "ledger")
	if err != nil || approval.Status != hex.ApprovalApproved || approval.RequestedBy == nil || approval.RequestedBy.ID != "owner" || approval.DecidedBy == nil || approval.DecidedBy.ID != "owner" {
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

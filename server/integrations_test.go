package hex_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	hex "github.com/crazycatviking/hex/server"
	"github.com/crazycatviking/hex/server/providers/easyauth"
	"github.com/crazycatviking/hex/server/providers/memory"
	"golang.org/x/oauth2"
)

func roleHeaders(id string, roles ...string) http.Header {
	claims := make([]map[string]string, 0, len(roles))
	for _, role := range roles {
		claims = append(claims, map[string]string{"typ": "roles", "val": role})
	}
	document, err := json.Marshal(map[string]any{"auth_typ": "aad", "claims": claims, "role_typ": "roles"})
	if err != nil {
		panic(err)
	}
	headers := http.Header{}
	headers.Set("X-Ms-Client-Principal", base64.StdEncoding.EncodeToString(document))
	headers.Set("X-Ms-Client-Principal-Id", id)
	headers.Set("X-Ms-Client-Principal-Name", id)
	return headers
}

var objectSchema = json.RawMessage(`{"type":"object"}`)

// anyResult accepts every result, for endpoints whose output is not under test.
var anyResult = json.RawMessage(`{}`)

func crmIntegration(calls *atomic.Int32) hex.Integration {
	return hex.Integration{
		Name: "crm", Title: "CRM", RequiresApproval: true,
		Endpoints: []hex.IntegrationEndpoint{
			{
				Name: "deals", Description: "List deals.", CacheTTL: 60e9,
				OutputSchema: anyResult, InputSchema: json.RawMessage(`{"type":"object","properties":{"stage":{"type":"string"}},"additionalProperties":false}`),
				Handler: func(_ context.Context, call hex.IntegrationCall, input json.RawMessage) (any, error) {
					calls.Add(1)
					return map[string]any{"site": call.Site, "caller": call.Identity.ID, "input": input}, nil
				},
			},
			{
				Name: "contacts", Description: "List contacts.", OutputSchema: anyResult, InputSchema: objectSchema,
				Handler: func(context.Context, hex.IntegrationCall, json.RawMessage) (any, error) {
					return []string{}, nil
				},
			},
			{
				Name: "update-deal", Description: "Change a deal.", Write: true, OutputSchema: anyResult, InputSchema: objectSchema,
				Handler: func(context.Context, hex.IntegrationCall, json.RawMessage) (any, error) {
					return map[string]bool{"ok": true}, nil
				},
			},
		},
	}
}

func setupIntegrations(t *testing.T, integrations ...hex.Integration) (*hex.Server, *memory.AccessStore, *memory.IntegrationStore) {
	t.Helper()
	registry := new(hex.IntegrationRegistry)
	for _, integration := range integrations {
		if err := registry.Register(integration); err != nil {
			t.Fatal(err)
		}
	}
	grants, err := hex.ParseIntegrationGrants(`{"role:Data.Sales":["crm.*"],"user:viewer":["crm.deals"],"site:demo":["crm.deals"]}`)
	if err != nil {
		t.Fatal(err)
	}
	access := memory.NewAccessStore()
	store := memory.NewIntegrationStore()
	server := hex.New(hex.Config{
		Identity: easyauth.Resolver{}, Access: access, AdminGroups: []string{"admin-group"},
		Integrations: registry, IntegrationGrants: grants, IntegrationStore: store,
	})
	return server, access, store
}

func TestIntegrationGrantsApprovalAndCache(t *testing.T) {
	var calls atomic.Int32
	server, access, _ := setupIntegrations(t, crmIntegration(&calls))
	if err := access.PutSiteAccess(context.Background(), "demo", hex.SiteAccess{
		Owners: []string{"user:owner"}, Editors: []string{"role:Data.Sales"},
	}); err != nil {
		t.Fatal(err)
	}
	sales := roleHeaders("seller", "Data.Sales")
	viewer := roleHeaders("viewer")
	stranger := roleHeaders("stranger")
	owner := roleHeaders("owner")
	admin := principalHeaders("admin", "admin-group")
	deals := "/api/sites/demo/integrations/crm/deals"

	requestAs(t, server, sales, "POST", deals, []byte(`{}`), 403)
	requestAs(t, server, stranger, "POST", "/api/hex/sites/demo/integrations/crm/approval", []byte(`{}`), 403)
	pending := requestAs(t, server, owner, "POST", "/api/hex/sites/demo/integrations/crm/approval", []byte(`{"reason":"Sales dashboard"}`), 200)
	if !strings.Contains(pending.Body.String(), `"status":"requested"`) {
		t.Fatal(pending.Body.String())
	}
	requestAs(t, server, owner, "PUT", "/api/hex/sites/demo/integrations/crm/approval", nil, 403)
	listed := requestAs(t, server, admin, "GET", "/api/hex/integration-approvals?status=requested", nil, 200)
	if !strings.Contains(listed.Body.String(), `"reason":"Sales dashboard"`) {
		t.Fatal(listed.Body.String())
	}
	requestAs(t, server, admin, "PUT", "/api/hex/sites/demo/integrations/crm/approval", nil, 200)

	response := requestAs(t, server, sales, "POST", deals, []byte(`{"stage":"won"}`), 200)
	if !strings.Contains(response.Body.String(), `"caller":"seller"`) || calls.Load() != 1 {
		t.Fatal(response.Body.String())
	}
	cached := requestAs(t, server, sales, "POST", deals, []byte(`{ "stage": "won" }`), 200)
	if cached.Header().Get("X-Hex-Cache") != "hit" || calls.Load() != 1 {
		t.Fatal("equivalent input was not served from the cache")
	}
	requestAs(t, server, sales, "POST", deals, []byte(`{"stage":5}`), 400)
	requestAs(t, server, sales, "POST", "/api/sites/demo/integrations/crm/missing", nil, 404)

	requestAs(t, server, viewer, "POST", deals, nil, 200)
	requestAs(t, server, stranger, "POST", deals, nil, 403)
	if err := access.PutSiteAccess(context.Background(), "demo", hex.SiteAccess{
		Owners: []string{"user:owner"}, Editors: []string{"role:Data.Sales"}, Viewers: []string{"group:nobody"},
	}); err != nil {
		t.Fatal(err)
	}
	requestAs(t, server, viewer, "POST", deals, nil, 403)
	if err := access.PutSiteAccess(context.Background(), "demo", hex.SiteAccess{
		Owners: []string{"user:owner"}, Editors: []string{"role:Data.Sales"}, Viewers: []string{"user:viewer"},
	}); err != nil {
		t.Fatal(err)
	}
	requestAs(t, server, viewer, "POST", "/api/sites/demo/integrations/crm/contacts", nil, 403)
	requestAs(t, server, viewer, "POST", "/api/sites/demo/integrations/crm/update-deal", nil, 403)
	requestAs(t, server, sales, "POST", "/api/sites/demo/integrations/crm/update-deal", nil, 200)

	listing := requestAs(t, server, viewer, "GET", "/api/sites/demo/integrations", nil, 200)
	var integrations []hex.SiteIntegration
	if err := json.Unmarshal(listing.Body.Bytes(), &integrations); err != nil {
		t.Fatal(err)
	}
	allowed := map[string]bool{}
	for _, endpoint := range integrations[0].Endpoints {
		allowed[endpoint.Name] = endpoint.Allowed
	}
	if integrations[0].Approval != "approved" || !allowed["deals"] || allowed["contacts"] || allowed["update-deal"] {
		t.Fatalf("unexpected listing: %s", listing.Body.String())
	}

	requestAs(t, server, owner, "DELETE", "/api/hex/sites/demo/integrations/crm/approval", nil, 403)
	requestAs(t, server, admin, "DELETE", "/api/hex/sites/demo/integrations/crm/approval", nil, 204)
	requestAs(t, server, sales, "POST", "/api/sites/demo/integrations/crm/contacts", nil, 403)
}

func TestIntegrationGrantValidation(t *testing.T) {
	for _, value := range []string{`{"user":["crm.*"]}`, `{"site:Bad_Name":["crm"]}`, `{"*":["crm.*.deals"]}`, `{"*":["CRM"]}`, `[]`} {
		if _, err := hex.ParseIntegrationGrants(value); err == nil {
			t.Errorf("accepted %s", value)
		}
	}
	registry := new(hex.IntegrationRegistry)
	invalid := []hex.Integration{
		{Name: "Bad", Title: "Bad"},
		{Name: "empty", Title: "Empty"},
		{Name: "linked", Title: "Linked", Connector: "unknown", Endpoints: crmIntegration(new(atomic.Int32)).Endpoints},
		{Name: "untyped", Title: "Untyped", Endpoints: []hex.IntegrationEndpoint{{
			Name: "read", Description: "Read.", InputSchema: objectSchema,
			Handler: func(context.Context, hex.IntegrationCall, json.RawMessage) (any, error) { return nil, nil },
		}}},
		{Name: "cached", Title: "Cached", Endpoints: []hex.IntegrationEndpoint{{
			Name: "write", Description: "Write.", Write: true, CacheTTL: 1, OutputSchema: anyResult, InputSchema: objectSchema,
			Handler: func(context.Context, hex.IntegrationCall, json.RawMessage) (any, error) { return nil, nil },
		}}},
	}
	for _, integration := range invalid {
		if err := registry.Register(integration); err == nil {
			t.Errorf("registered invalid integration %s", integration.Name)
		}
	}
}

// fakeAuthorizationServer issues one code and refreshes tokens, recording
// the PKCE challenge it was given.
type fakeAuthorizationServer struct {
	server    *httptest.Server
	challenge string
	refreshes atomic.Int32
}

func newFakeAuthorizationServer(t *testing.T) *fakeAuthorizationServer {
	fake := &fakeAuthorizationServer{}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /token", func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Error(err)
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.Form.Get("grant_type") {
		case "authorization_code":
			if r.Form.Get("code") != "the-code" || r.Form.Get("code_verifier") == "" {
				w.WriteHeader(http.StatusBadRequest)
				fmt.Fprint(w, `{"error":"invalid_grant"}`)
				return
			}
			fmt.Fprint(w, `{"access_token":"first","refresh_token":"refresh-1","token_type":"Bearer","expires_in":1}`)
		case "refresh_token":
			fake.refreshes.Add(1)
			if r.Form.Get("refresh_token") != "refresh-1" {
				w.WriteHeader(http.StatusBadRequest)
				fmt.Fprint(w, `{"error":"invalid_grant"}`)
				return
			}
			fmt.Fprint(w, `{"access_token":"second","refresh_token":"refresh-2","token_type":"Bearer","expires_in":3600}`)
		}
	})
	mux.HandleFunc("GET /me", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"token":%q}`, strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
	})
	fake.server = httptest.NewServer(mux)
	t.Cleanup(fake.server.Close)
	return fake
}

func TestConnectedAccountFlow(t *testing.T) {
	fake := newFakeAuthorizationServer(t)
	registry := new(hex.IntegrationRegistry)
	if err := registry.RegisterConnector(hex.Connector{
		Name: "docs", Title: "Docs",
		OAuth2: oauth2.Config{ClientID: "client", ClientSecret: "secret", Endpoint: oauth2.Endpoint{
			AuthURL: fake.server.URL + "/authorize", TokenURL: fake.server.URL + "/token",
		}},
		Account: func(context.Context, *http.Client) (string, error) { return "person@example.test", nil },
	}); err != nil {
		t.Fatal(err)
	}
	if err := registry.Register(hex.Integration{
		Name: "docs", Title: "Docs", Connector: "docs",
		Endpoints: []hex.IntegrationEndpoint{{
			Name: "me", Description: "Who the connected account is.", OutputSchema: anyResult, InputSchema: objectSchema,
			Handler: func(ctx context.Context, call hex.IntegrationCall, _ json.RawMessage) (any, error) {
				client, err := call.HTTPClient(ctx)
				if err != nil {
					return nil, err
				}
				request, err := hex.NewJSONRequest(ctx, "GET", fake.server.URL+"/me", nil)
				if err != nil {
					return nil, err
				}
				var result map[string]string
				return result, hex.DoJSON(client, request, "Docs", &result)
			},
		}},
	}); err != nil {
		t.Fatal(err)
	}
	store := memory.NewIntegrationStore()
	server := hex.New(hex.Config{
		Identity: easyauth.Resolver{}, Access: memory.NewAccessStore(), SiteBaseURL: "https://hex.example.com",
		Integrations: registry, IntegrationGrants: []hex.IntegrationGrant{{Principal: "*", Permissions: []string{"*"}}},
		IntegrationStore: store, CredentialKey: make([]byte, 32), ConnectionIdleExpiry: 30 * 24 * time.Hour,
	})
	person := roleHeaders("person")
	call := "/api/sites/demo/integrations/docs/me"

	notConnected := requestAs(t, server, person, "POST", call, nil, 409)
	if !strings.Contains(notConnected.Body.String(), "https://hex.example.com/api/hex/connections/docs/start") {
		t.Fatal(notConnected.Body.String())
	}

	start := requestAs(t, server, person, "GET", "/api/hex/connections/docs/start?return=https://evil.example.org/", nil, 302)
	location, err := url.Parse(start.Header().Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	query := location.Query()
	if query.Get("code_challenge") == "" || query.Get("redirect_uri") != "https://hex.example.com/api/hex/connections/docs/callback" {
		t.Fatalf("unexpected authorization URL %s", location)
	}
	cookie := start.Result().Cookies()[0]
	if !cookie.HttpOnly || !cookie.Secure {
		t.Fatal("the state cookie must be HttpOnly and Secure")
	}

	callback := "/api/hex/connections/docs/callback?code=the-code&state=" + url.QueryEscape(query.Get("state"))
	otherPerson := roleHeaders("other")
	otherPerson.Set("Cookie", cookie.Name+"="+cookie.Value)
	requestAs(t, server, otherPerson, "GET", callback, nil, 400)

	withCookie := roleHeaders("person")
	withCookie.Set("Cookie", cookie.Name+"="+cookie.Value)
	withCookie.Set("Sec-Fetch-Site", "cross-site")
	requestAs(t, server, withCookie, "GET", "/api/hex/connections/docs/callback?code=the-code&state=wrong", nil, 400)
	done := requestAs(t, server, withCookie, "GET", callback, nil, 200)
	if !strings.Contains(done.Body.String(), "Docs connected") {
		t.Fatal(done.Body.String())
	}

	connections := requestAs(t, server, person, "GET", "/api/hex/connections", nil, 200)
	if !strings.Contains(connections.Body.String(), `"connected":true`) || !strings.Contains(connections.Body.String(), "person@example.test") {
		t.Fatal(connections.Body.String())
	}

	// The first token expires at once, so the call refreshes it.
	result := requestAs(t, server, person, "POST", call, nil, 200)
	if !strings.Contains(result.Body.String(), `"token":"second"`) || fake.refreshes.Load() != 1 {
		t.Fatal(result.Body.String())
	}
	requestAs(t, server, person, "POST", call, nil, 200)
	if fake.refreshes.Load() != 1 {
		t.Fatal("a valid refreshed token was refreshed again")
	}
	requestAs(t, server, roleHeaders("other"), "POST", call, nil, 409)

	record, err := store.GetCredential(context.Background(), "person", "docs")
	if err != nil || record.Account != "person@example.test" || bytes.Contains(record.Sealed, []byte("second")) {
		t.Fatalf("unexpected stored connection %+v: %v", record, err)
	}

	// A connection unused for longer than the idle expiry is gone.
	record.LastUsedAt = time.Now().Add(-31 * 24 * time.Hour)
	if err := store.PutCredential(context.Background(), record); err != nil {
		t.Fatal(err)
	}
	if listed := requestAs(t, server, person, "GET", "/api/hex/connections", nil, 200).Body.String(); strings.Contains(listed, `"connected":true`) {
		t.Fatal("an idle connection is listed as connected")
	}
	requestAs(t, server, person, "POST", call, nil, 409)
	if _, err := store.GetCredential(context.Background(), "person", "docs"); err == nil {
		t.Fatal("an idle connection was not removed")
	}

	if err := store.PutCredential(context.Background(), hex.CredentialRecord{Owner: "person", Connector: "docs", Sealed: record.Sealed, LastUsedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	requestAs(t, server, person, "DELETE", "/api/hex/connections/docs", nil, 204)
	requestAs(t, server, person, "POST", call, nil, 409)

	stale := hex.CredentialRecord{Owner: "old", Connector: "docs", Sealed: []byte{1}, LastUsedAt: time.Now().Add(-60 * 24 * time.Hour)}
	if err := store.PutCredential(context.Background(), stale); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	server.RunBackground(ctx)
	if _, err := store.GetCredential(context.Background(), "old", "docs"); err == nil {
		t.Fatal("the background cleanup kept an idle connection")
	}
}

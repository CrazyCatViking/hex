package hex_test

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	hex "github.com/crazycatviking/hex/server"
	"github.com/crazycatviking/hex/server/providers/easyauth"
	"github.com/crazycatviking/hex/server/providers/memory"
	"golang.org/x/oauth2"
)

func TestConnectedAccountsPage(t *testing.T) {
	registry := new(hex.IntegrationRegistry)
	if err := registry.RegisterConnector(hex.Connector{
		Name: "docs", Title: "Docs", Description: "Pages you can read in Docs.",
		OAuth2: oauth2.Config{ClientID: "client", Endpoint: oauth2.Endpoint{AuthURL: "https://docs.test/authorize", TokenURL: "https://docs.test/token"}},
	}); err != nil {
		t.Fatal(err)
	}
	if err := registry.Register(hex.Integration{Name: "docs", Title: "Docs", Connector: "docs", Endpoints: []hex.IntegrationEndpoint{{
		Name: "me", Description: "Who you are.", InputSchema: json.RawMessage(`{"type":"object"}`),
		Handler: func(context.Context, hex.IntegrationCall, json.RawMessage) (any, error) { return nil, nil },
	}}}); err != nil {
		t.Fatal(err)
	}
	store := memory.NewIntegrationStore()
	server := hex.New(hex.Config{
		Identity: easyauth.Resolver{}, Access: memory.NewAccessStore(), People: memory.NewPeopleStore(),
		AdminGroups: []string{"admin-group"}, SiteBaseURL: "http://example.com",
		Integrations: registry, IntegrationStore: store, CredentialKey: make([]byte, 32),
	})
	person := signedIn(t, "person-id", "Per Son", "per@example.test")
	admin := principalHeaders("admin", "admin-group")

	page := requestAs(t, server, person, "GET", "/connections", nil, 200).Body.String()
	for _, expected := range []string{"Connected accounts", "Pages you can read in Docs.", "Not connected", "/api/hex/connections/docs/start?return=http%3A%2F%2Fexample.com%2Fconnections", "90 days"} {
		if !strings.Contains(page, expected) {
			t.Fatalf("the page lacks %q: %s", expected, page)
		}
	}
	if strings.Contains(page, "Everyone's connections") {
		t.Fatal("a non-admin sees everyone's connections")
	}
	if !strings.Contains(requestAs(t, server, person, "GET", "/", nil, 200).Body.String(), `href="/connections"`) {
		t.Fatal("the account menu does not link to connected accounts")
	}

	now := time.Now().UTC()
	record := hex.CredentialRecord{Owner: "person-id", Connector: "docs", Sealed: []byte{1}, Account: "per@docs.test", ConnectedAt: now, LastUsedAt: now}
	if err := store.PutCredential(context.Background(), record); err != nil {
		t.Fatal(err)
	}
	page = requestAs(t, server, person, "GET", "/connections", nil, 200).Body.String()
	if !strings.Contains(page, "Connected as per@docs.test") || !strings.Contains(page, "just now") {
		t.Fatalf("the connection is not shown: %s", page)
	}

	everyone := requestAs(t, server, admin, "GET", "/connections", nil, 200).Body.String()
	if !strings.Contains(everyone, "Everyone's connections") || !strings.Contains(everyone, "Per Son") || !strings.Contains(everyone, "/api/hex/manage/connections/docs/person-id") {
		t.Fatalf("the admin does not see everyone's connections: %s", everyone)
	}
	requestAs(t, server, person, "DELETE", "/api/hex/manage/connections/docs/person-id", nil, http.StatusForbidden)

	row := requestAs(t, server, person, "DELETE", "/api/hex/manage/connections/docs", nil, 200).Body.String()
	if !strings.Contains(row, "Not connected") {
		t.Fatalf("disconnecting did not update the row: %s", row)
	}
	if err := store.PutCredential(context.Background(), record); err != nil {
		t.Fatal(err)
	}
	requestAs(t, server, admin, "DELETE", "/api/hex/manage/connections/docs/person-id", nil, 200)
	if _, err := store.GetCredential(context.Background(), "person-id", "docs"); err == nil {
		t.Fatal("the admin's removal did not remove the connection")
	}
}

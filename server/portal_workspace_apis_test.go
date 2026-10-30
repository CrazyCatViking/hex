package hex_test

import (
	"encoding/json"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	hex "github.com/crazycatviking/hex/server"
)

func TestWorkspaceCatalogSeparatesAPISharingFromPageAdmission(t *testing.T) {
	server, _ := setupPortal(t)
	owner := principalHeaders("owner")
	visitor := principalHeaders("visitor")
	actions := `[{"name":"submit","description":"Send a request","version":1,"exported":true,"operation":"create","collection":"requests","input":{"type":"object"}}]`
	publishWithActions(t, server, owner, "private", actions)
	access := hex.SiteAccess{Version: 2, Owners: []string{"user:owner"}, Admission: hex.Subjects{Owners: true}, Grants: map[string]hex.PermissionGrant{
		"connection": {Kind: "action-consumer", Resource: "submit", Operations: []string{"run"}, Subjects: hex.Subjects{Workspace: true}},
		"execution":  {Kind: "action", Resource: "submit", Operations: []string{"run"}, Subjects: hex.Subjects{Workspace: true}},
	}}
	requestAs(t, server, owner, "PUT", "/api/hex/sites/private/access", encode(t, access), 200)
	page := strings.Join(strings.Fields(requestAs(t, server, visitor, "GET", "/workspace-apis", nil, 200).Body.String()), " ")
	if !strings.Contains(page, "Send a request") || !strings.Contains(page, "Everyone in this workspace") {
		t.Fatalf("public API on a private site missing from catalog: %s", page)
	}
	filtered := requestAs(t, server, visitor, "GET", "/workspace-apis?q=missing", nil, 200).Body.String()
	if strings.Contains(filtered, "Send a request") {
		t.Fatal("catalog search did not filter")
	}
	access.Grants = nil
	requestAs(t, server, owner, "PUT", "/api/hex/sites/private/access", encode(t, access), 200)
	page = requestAs(t, server, visitor, "GET", "/workspace-apis", nil, 200).Body.String()
	if strings.Contains(page, "Send a request") {
		t.Fatal("restricted private contract leaked")
	}
	page = strings.Join(strings.Fields(requestAs(t, server, owner, "GET", "/workspace-apis", nil, 200).Body.String()), " ")
	if !strings.Contains(page, "Send a request") || !strings.Contains(page, "Extra access may be needed") {
		t.Fatal("owner cannot see restricted API and access guidance")
	}
}

func TestWorkspaceCatalogJSONAndConsumerDiscovery(t *testing.T) {
	server, _ := setupPortal(t)
	owner := principalHeaders("owner")
	visitor := principalHeaders("visitor")
	actions := `[{"name":"submit","description":"Send a request","version":1,"exported":true,"operation":"create","collection":"requests","input":{"type":"object","additionalProperties":false}},
	{"name":"internal","description":"Not shared","operation":"create","collection":"requests","input":{"type":"object"}}]`
	publishWithActions(t, server, owner, "private", actions)
	// The per-app editor shares through a workspace role and explicit modes.
	access := hex.SiteAccess{Version: 2, Owners: []string{"user:owner"}, Admission: hex.Subjects{Owners: true},
		Roles: map[string]hex.ApplicationRole{"everyone": {Name: "All workspace apps", Workspace: true}},
		Grants: map[string]hex.PermissionGrant{
			"connection": {Kind: "action-consumer", Resource: "submit", Operations: []string{"user"}, Subjects: hex.Subjects{Roles: []string{"everyone"}}},
			"execution":  {Kind: "action", Resource: "submit", Operations: []string{"run"}, Subjects: hex.Subjects{Workspace: true}},
		}}
	requestAs(t, server, owner, "PUT", "/api/hex/sites/private/access", encode(t, access), 200)
	var catalog []struct {
		Site, Kind, Name, Sharing string
		Version                   int
		WorkspaceModes            []string
		Owners                    []string
		InputSchema               json.RawMessage
	}
	decodeResponse(t, requestAs(t, server, visitor, "GET", "/api/hex/workspace-apis", nil, 200), &catalog)
	if len(catalog) != 1 || catalog[0].Name != "submit" || catalog[0].Site != "private" || catalog[0].Sharing != "workspace" || catalog[0].Version != 1 || !slices.Equal(catalog[0].WorkspaceModes, []string{"user"}) || len(catalog[0].Owners) != 1 || len(catalog[0].InputSchema) == 0 {
		t.Fatalf("catalog = %+v", catalog)
	}
	decodeResponse(t, requestAs(t, server, visitor, "GET", "/api/hex/workspace-apis?q=missing", nil, 200), &catalog)
	if len(catalog) != 0 {
		t.Fatal("catalog search did not filter", catalog)
	}
	publishWithActions(t, server, visitor, "consumer", `[]`)
	// Unexported actions on the target must not break consumer discovery.
	var available []hex.ActionDefinition
	decodeResponse(t, requestAs(t, server, visitor, "GET", "/api/sites/consumer/site-api/actions/private", nil, 200), &available)
	if len(available) != 1 || available[0].Name != "submit" {
		t.Fatalf("available = %+v", available)
	}
	decodeResponse(t, requestAs(t, server, visitor, "GET", "/api/sites/consumer/site-api/actions/private?mode=site", nil, 200), &available)
	if len(available) != 0 {
		t.Fatal("site mode listed without a site grant", available)
	}
}

func decodeResponse(t *testing.T, response *httptest.ResponseRecorder, value any) {
	t.Helper()
	if err := json.Unmarshal(response.Body.Bytes(), value); err != nil {
		t.Fatal(err, response.Body.String())
	}
}

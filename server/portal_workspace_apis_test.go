package hex_test

import (
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

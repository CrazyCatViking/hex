package hex_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	hex "github.com/crazycatviking/hex/server"
)

func TestDeclaredReadActionsFilterBeforeProjectionAndPagination(t *testing.T) {
	policy := hex.SiteAccess{Version: 2, Owners: []string{"user:owner-id"}, Admission: hex.Subjects{SignedIn: true}, Grants: map[string]hex.PermissionGrant{
		"actions": {Kind: "action", Resource: "*", Operations: []string{"run"}, Subjects: hex.Subjects{SignedIn: true}},
		"read":    {Kind: "collection", Resource: "customers", Operations: []string{"read"}, Subjects: hex.Subjects{SignedIn: true}, Where: &hex.RecordCondition{Field: "data.team", Op: "eq", Value: json.RawMessage(`"A"`)}},
	}}
	server, policies, database := setupPermissionPolicy(t, policy)
	actions := `[
 {"name":"list-customers","description":"List safe customer names","operation":"list","collection":"customers","version":1,"exported":true,"select":["name"],"input":{"type":"object","properties":{"limit":{"type":"integer"},"after":{"type":"string"}},"additionalProperties":false},"output":{"type":"object"}},
 {"name":"get-customer","description":"Get safe customer name","operation":"get","collection":"customers","select":["name"],"input":{"type":"object","properties":{"id":{"type":"string"}},"required":["id"],"additionalProperties":false},"output":{"type":"object"}}
 ]`
	publishWithActions(t, server, principalHeaders("owner-id"), "demo", actions)
	if err := policies.PutSiteAccess(context.Background(), "demo", policy); err != nil {
		t.Fatal(err)
	}
	changed := strings.Replace(actions, `"limit":{"type":"integer"}`, `"limit":{"type":"string"}`, 1)
	requestAs(t, server, principalHeaders("owner-id"), "POST", "/api/hex/sites/demo/publish", encode(t, map[string]any{
		"files": manifest(map[string]string{"index.html": "<h1>calls</h1>"}), "actions": json.RawMessage(changed),
	}), 400)
	for id, data := range map[string]string{"a": `{"name":"Hidden","team":"B","secret":"hidden-secret"}`, "b": `{"name":"Visible","team":"A","secret":"visible-secret"}`, "c": `{"name":"Next","team":"A"}`} {
		if _, err := database.Put(context.Background(), "demo", "customers", id, json.RawMessage(data), hex.WriteOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	headers := principalHeaders("alice")
	page := requestAs(t, server, headers, "POST", "/api/sites/demo/actions/list-customers", []byte(`{"limit":1}`), 200).Body.String()
	if !strings.Contains(page, `"id":"b"`) || strings.Contains(page, "secret") || strings.Contains(page, "Hidden") {
		t.Fatal(page)
	}
	next := requestAs(t, server, headers, "POST", "/api/sites/demo/actions/list-customers", []byte(`{"limit":1,"after":"b"}`), 200).Body.String()
	if !strings.Contains(next, `"id":"c"`) {
		t.Fatal(next)
	}
	// The ordinary action handler reports missing/filtered rows without returning data.
	requestAs(t, server, headers, "POST", "/api/sites/demo/actions/get-customer", []byte(`{"id":"a"}`), 404)
	get := requestAs(t, server, headers, "POST", "/api/sites/demo/actions/get-customer", []byte(`{"id":"b"}`), 200).Body.String()
	if strings.Contains(get, "secret") || strings.Contains(get, "team") {
		t.Fatal(get)
	}
}

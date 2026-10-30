package hex

import (
	"encoding/json"
	"testing"
)

func TestSiteExecutionIsActionLocalAndScoped(t *testing.T) {
	access := SiteAccess{Version: 2, Roles: map[string]ApplicationRole{"consumer": {}}, Bindings: map[string]RoleBinding{"source": {Principal: "site:source-id", Role: "consumer"}}, Grants: map[string]PermissionGrant{
		"connection": {Kind: "action-consumer", Resource: "edit", Operations: []string{"site"}, Subjects: Subjects{Roles: []string{"consumer"}}, Where: &RecordCondition{Field: "data.project", Op: "eq", Value: json.RawMessage(`"A"`)}, Check: &RecordCondition{Field: "data.project", Op: "eq", Value: json.RawMessage(`"A"`)}, Fields: []string{"note"}},
		"broad":      {Kind: "collection", Resource: "*", Operations: []string{"update"}, Subjects: Subjects{Workspace: true}},
	}}
	if err := ValidatePublishedAccess(access); err != nil {
		t.Fatal(err)
	}
	caller := siteAuthorization{access: access, identity: &Identity{ID: "site:source-id", Provider: "site"}, role: roleViewer, apiOnly: true}
	action := &registeredAction{definition: ActionDefinition{Name: "edit"}, declared: &DeclaredAction{Name: "edit", Operation: "update", Collection: "expenses"}}
	local, err := siteActionAuthorization(caller, action)
	if err != nil {
		t.Fatal(err)
	}
	before := Document{ID: "record", CreatedBy: "human", Data: json.RawMessage(`{"project":"A","note":"old"}`)}
	after := before
	after.Data = json.RawMessage(`{"project":"A","note":"new"}`)
	if !local.canMutate("expenses", "update", &before, after) {
		t.Fatal("app cannot update an eligible human-created record")
	}
	for _, data := range []string{`{"project":"B","note":"new"}`, `{"project":"A","note":"new","amount":100}`} {
		after.Data = json.RawMessage(data)
		if local.canMutate("expenses", "update", &before, after) {
			t.Fatal("scope or fields escaped", data)
		}
	}
	if local.permits("action", "another", "run") || local.permits("collection", "other", "update") || local.permits("collection", "expenses", "delete") {
		t.Fatal("action grant escaped its declared operation")
	}
	if !caller.permits("collection", "other", "update") {
		t.Fatal("request-local policy modified the original policy")
	}
	caller.identity = &Identity{ID: "site:other", Provider: "site"}
	other, err := siteActionAuthorization(caller, action)
	if err != nil || other.permits("action", "edit", "run") {
		t.Fatal("unassigned app inherited authority", err)
	}
	if _, err := siteActionAuthorization(caller, &registeredAction{}); err == nil {
		t.Fatal("arbitrary handler received data authority")
	}
}

func TestScopedHumanDefaultDoesNotIncludeAppIdentity(t *testing.T) {
	a := siteAuthorization{role: roleViewer, apiOnly: true, identity: &Identity{ID: "site:source", Provider: "site"}, access: SiteAccess{Version: 2, Roles: map[string]ApplicationRole{"member": {SignedIn: true}}}}
	grant := PermissionGrant{Subjects: Subjects{Roles: []string{"member"}}, Scope: &PermissionScope{Kind: "project", Field: "data.project"}}
	if _, allowed := a.grantCondition(grant); allowed {
		t.Fatal("app inherited signed-in human defaults")
	}
}

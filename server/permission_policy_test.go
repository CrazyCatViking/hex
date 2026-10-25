package hex_test

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"sync"
	"testing"

	hex "github.com/crazycatviking/hex/server"
	"github.com/crazycatviking/hex/server/providers/easyauth"
	"github.com/crazycatviking/hex/server/providers/local"
	"github.com/crazycatviking/hex/server/providers/memory"
)

func setupPermissionPolicy(t *testing.T, policy hex.SiteAccess) (*hex.Server, *memory.AccessStore, *memory.Database) {
	t.Helper()
	files, err := local.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { files.Close() })
	policies := memory.NewAccessStore()
	if policy.Version == 2 && policy.Published == nil {
		baseline := hex.CloneSiteAccess(policy)
		policy.Published = &baseline
	}
	if err := policies.PutSiteAccess(context.Background(), "demo", policy); err != nil {
		t.Fatal(err)
	}
	database := memory.NewDatabase()
	server := hex.New(hex.Config{Identity: easyauth.Resolver{}, Access: policies, Database: database, Files: files, Sites: files, Publisher: files, AdminGroups: []string{"admin-group"}})
	return server, policies, database
}

func permissionPolicy() hex.SiteAccess {
	return hex.SiteAccess{
		Version: 2, Owners: []string{"user:owner-id"}, Admission: hex.Subjects{SignedIn: true},
		Roles:    map[string]hex.ApplicationRole{"employee": {SignedIn: true}, "approver": {}},
		Bindings: map[string]hex.RoleBinding{"project-approvers": {Principal: "group:managers", Role: "approver", Scope: &hex.RoleScope{Kind: "project", ID: "A"}}},
		Grants: map[string]hex.PermissionGrant{
			"pages":        {Kind: "path", Resource: "*", Operations: []string{"read"}, Subjects: hex.Subjects{SignedIn: true}},
			"own-read":     {Kind: "collection", Resource: "expenses", Operations: []string{"read"}, Subjects: hex.Subjects{Roles: []string{"employee"}}, Where: &hex.RecordCondition{Field: "createdBy", Op: "eq", Value: json.RawMessage(`"$actor.id"`)}},
			"create-draft": {Kind: "collection", Resource: "expenses", Operations: []string{"create"}, Subjects: hex.Subjects{Roles: []string{"employee"}}, Check: &hex.RecordCondition{Field: "data.status", Op: "eq", Value: json.RawMessage(`"draft"`)}, Fields: []string{"status", "amount", "projectId"}},
			"edit-draft":   {Kind: "collection", Resource: "expenses", Operations: []string{"update"}, Subjects: hex.Subjects{Roles: []string{"employee"}}, Where: &hex.RecordCondition{All: []hex.RecordCondition{{Field: "createdBy", Op: "eq", Value: json.RawMessage(`"$actor.id"`)}, {Field: "data.status", Op: "eq", Value: json.RawMessage(`"draft"`)}}}, Fields: []string{"amount"}},
			"project-read": {Kind: "collection", Resource: "expenses", Operations: []string{"read"}, Subjects: hex.Subjects{Roles: []string{"approver"}}, Scope: &hex.PermissionScope{Kind: "project", Field: "data.projectId"}},
		},
	}
}

func TestPermissionPolicyCRUDAndProtectedFields(t *testing.T) {
	server, _, database := setupPermissionPolicy(t, permissionPolicy())
	alice := principalHeaders("alice")
	requestAs(t, server, alice, "PUT", "/api/sites/demo/db/expenses/first", []byte(`{"status":"draft","amount":10,"projectId":"A"}`), 200)
	requestAs(t, server, alice, "PUT", "/api/sites/demo/db/expenses/first", []byte(`{"status":"draft","amount":20,"projectId":"A"}`), 200)
	for _, data := range []string{`{"status":"approved","amount":20,"projectId":"A"}`, `{"status":"draft","amount":20,"projectId":"B"}`, `{"status":"draft","amount":20,"projectId":"A","approvedBy":"alice"}`} {
		requestAs(t, server, alice, "PUT", "/api/sites/demo/db/expenses/first", []byte(data), 403)
	}
	requestAs(t, server, alice, "PUT", "/api/sites/demo/db/expenses/approved", []byte(`{"status":"approved","amount":10}`), 403)
	requestAs(t, server, alice, "DELETE", "/api/sites/demo/db/expenses/first", nil, 403)
	requestAs(t, server, principalHeaders("bob"), "GET", "/api/sites/demo/db/expenses/first", nil, 404)
	// Management authority is not application authority in a new policy.
	requestAs(t, server, principalHeaders("owner-id"), "GET", "/api/sites/demo/db/expenses/first", nil, 404)
	requestAs(t, server, principalHeaders("admin", "admin-group"), "GET", "/api/sites/demo/db/expenses/first", nil, 404)
	document, err := database.Get(context.Background(), "demo", "expenses", "first")
	if err != nil || document.CreatedBy != "alice" || !strings.Contains(string(document.Data), `20`) {
		t.Fatalf("unexpected stored record: %+v %v", document, err)
	}
	requestAs(t, server, alice, "POST", "/api/sites/demo/permissions/check", []byte(`{"collection":"expenses","operation":"update","id":"first","data":{"status":"draft","amount":30,"projectId":"A"}}`), 200)
	response := requestAs(t, server, alice, "POST", "/api/sites/demo/permissions/check", []byte(`{"collection":"expenses","operation":"delete","id":"first"}`), 200)
	if !strings.Contains(response.Body.String(), `"allowed":false`) {
		t.Fatal(response.Body.String())
	}
}

func TestPermissionPolicyScopedFilteringBeforePagination(t *testing.T) {
	server, _, database := setupPermissionPolicy(t, permissionPolicy())
	for id, project := range map[string]string{"a": "B", "b": "A", "c": "B", "d": "A"} {
		if _, err := database.Put(context.Background(), "demo", "expenses", id, json.RawMessage(`{"projectId":"`+project+`","status":"submitted"}`), hex.WriteOptions{Creator: "requester"}); err != nil {
			t.Fatal(err)
		}
	}
	manager := principalHeaders("manager", "managers")
	first := requestAs(t, server, manager, "GET", "/api/sites/demo/db/expenses?limit=1", nil, 200)
	second := requestAs(t, server, manager, "GET", "/api/sites/demo/db/expenses?limit=1&after=b", nil, 200)
	if !strings.Contains(first.Body.String(), `"id":"b"`) || !strings.Contains(second.Body.String(), `"id":"d"`) {
		t.Fatalf("filtering occurred after pagination: %s %s", first.Body.String(), second.Body.String())
	}
	requestAs(t, server, manager, "GET", "/api/sites/demo/db/expenses/a", nil, 404)
	permissions := requestAs(t, server, manager, "GET", "/api/hex/sites/demo/permissions", nil, 200)
	if !strings.Contains(permissions.Body.String(), `"read":"conditional"`) {
		t.Fatal(permissions.Body.String())
	}
}

func TestPermissionPolicyPortalRevocationsSurviveRepeatedPublishing(t *testing.T) {
	policy := permissionPolicy()
	server, policies, _ := setupPermissionPolicy(t, policy)
	owner := principalHeaders("owner-id")
	local := string(encode(t, policy))
	// An existing policy without a publication baseline adopts the first source.
	publishAccess(t, server, owner, "demo", local, false)
	remote := showAccess(t, server, owner, "demo")
	delete(remote.Bindings, "project-approvers")
	remote.Bindings["portal-auditor"] = hex.RoleBinding{Principal: "user:auditor", Role: "employee"}
	requestAs(t, server, owner, "PUT", "/api/hex/sites/demo/access", encode(t, remote), 200)
	policy.Bindings["local-assignment"] = hex.RoleBinding{Principal: "group:support", Role: "employee"}
	for i := 0; i < 3; i++ {
		publishAccess(t, server, owner, "demo", string(encode(t, policy)), false)
	}
	saved := showAccess(t, server, owner, "demo")
	if _, exists := saved.Bindings["project-approvers"]; exists {
		t.Fatal("publication resurrected a portal revocation")
	}
	for _, id := range []string{"portal-auditor", "local-assignment"} {
		if _, exists := saved.Bindings[id]; !exists {
			t.Fatalf("lost independent assignment %s", id)
		}
	}
	stored, err := policies.GetSiteAccess(context.Background(), "demo")
	if err != nil {
		t.Fatal(err)
	}
	if _, exists := stored.Published.Bindings["portal-auditor"]; exists {
		t.Fatal("merged remote policy incorrectly became the publication baseline")
	}
	if _, exists := stored.Published.Bindings["project-approvers"]; !exists {
		t.Fatal("baseline lost the unchanged local assignment")
	}
	// A change to the same stable assignment conflicts with the revocation.
	binding := policy.Bindings["project-approvers"]
	binding.Scope.ID = "B"
	policy.Bindings["project-approvers"] = binding
	body := encode(t, accessPublication{Files: manifest(map[string]string{"index.html": "page"}), Access: encode(t, policy)})
	response := requestAs(t, server, owner, "POST", "/api/hex/sites/demo/publish", body, 409)
	if !strings.Contains(response.Body.String(), "binding project-approvers") {
		t.Fatal(response.Body.String())
	}
}

func TestPermissionPolicyMigratesLiveAndPublishedHistory(t *testing.T) {
	base := hex.SiteAccess{Owners: []string{"user:owner-id"}, Viewers: []string{"group:team", "group:revoked"}, Editors: []string{"group:editors"}, Collections: map[string]hex.DataRule{"drafts": {Read: hex.Audience{Level: "creator"}, Write: hex.Audience{Level: "creator"}}, "locked": {Write: hex.Audience{Level: "owners"}}}}
	live := hex.CloneSiteAccess(base)
	live.Viewers = []string{"group:team", "user:portal-user"}
	live.Published = &base
	server, policies, _ := setupPermissionPolicy(t, live)
	owner := principalHeaders("owner-id")
	migrated := showAccess(t, server, owner, "demo")
	if migrated.Version != 2 || migrated.Collections != nil {
		t.Fatalf("not migrated: %+v", migrated)
	}
	stored, err := policies.GetSiteAccess(context.Background(), "demo")
	if err != nil || stored.Published == nil || stored.Published.Version != 2 {
		t.Fatalf("baseline not migrated: %+v %v", stored, err)
	}
	local := hex.MigrateSiteAccess(base)
	for i := 0; i < 2; i++ {
		publishAccess(t, server, owner, "demo", string(encode(t, local)), false)
	}
	requestAs(t, server, principalHeaders("revoked", "revoked"), "GET", "/api/sites/demo/db/locked", nil, 403)
	requestAs(t, server, principalHeaders("portal-user"), "GET", "/api/sites/demo/db/locked", nil, 200)
	requestAs(t, server, principalHeaders("editor", "editors"), "PUT", "/api/sites/demo/db/locked/x", []byte(`{}`), 403)
}

func TestPermissionPolicyRejectsLegacyBeforeClaimOrFiles(t *testing.T) {
	server, policies, _ := setupPermissionPolicy(t, permissionPolicy())
	for _, policy := range []string{`{"viewers":[]}`, `{"version":2,"viewers":[]}`, `{"version":1}`} {
		body := `{"files":[{"path":"index.html","size":1}],"access":` + policy + `}`
		response := requestAs(t, server, principalHeaders("owner-id"), "POST", "/api/hex/sites/new-site/publish", []byte(body), 400)
		if !strings.Contains(response.Body.String(), "legacy permission configuration") || !strings.Contains(response.Body.String(), "hex access show") {
			t.Fatal(response.Body.String())
		}
		if _, err := policies.GetSiteAccess(context.Background(), "new-site"); err == nil {
			t.Fatal("legacy publication claimed a name")
		}
	}
}

func TestPermissionPolicyActionCannotBeBypassedThroughDatabase(t *testing.T) {
	policy := permissionPolicy()
	policy.Grants["approve"] = hex.PermissionGrant{Kind: "action", Resource: "approve", Operations: []string{"run"}, Subjects: hex.Subjects{Roles: []string{"approver"}}, Scope: &hex.PermissionScope{Kind: "project", Field: "data.projectId"}}
	server, _, database := setupPermissionPolicy(t, policy)
	action := hex.DeclaredAction{Name: "approve", Description: "Approve an expense in your project.", Operation: "update", Collection: "expenses", Authorization: "action", Input: json.RawMessage(`{"type":"object","properties":{"id":{"type":"string"}},"required":["id"],"additionalProperties":false}`), Where: &hex.RecordCondition{All: []hex.RecordCondition{{Field: "data.status", Op: "eq", Value: json.RawMessage(`"submitted"`)}, {Field: "createdBy", Op: "ne", Value: json.RawMessage(`"$actor.id"`)}}}, Set: map[string]json.RawMessage{"status": json.RawMessage(`"approved"`), "approvedBy": json.RawMessage(`"$actor.id"`), "approvedAt": json.RawMessage(`"$now"`)}}
	files := manifest(map[string]string{"index.html": "page"})
	body := encode(t, map[string]any{"files": files, "actions": []hex.DeclaredAction{action}})
	requestAs(t, server, principalHeaders("owner-id"), "POST", "/api/hex/sites/demo/publish", body, 200)
	requestAs(t, server, principalHeaders("owner-id"), "PUT", "/api/hex/sites/demo/publish/files/index.html", []byte("page"), 204)
	requestAs(t, server, principalHeaders("owner-id"), "POST", "/api/hex/sites/demo/publish/complete", body, 200)
	for id, project := range map[string]string{"a": "A", "b": "B", "own": "A"} {
		creator := "requester"
		if id == "own" {
			creator = "manager"
		}
		if _, err := database.Put(context.Background(), "demo", "expenses", id, json.RawMessage(`{"projectId":"`+project+`","status":"submitted"}`), hex.WriteOptions{Creator: creator}); err != nil {
			t.Fatal(err)
		}
	}
	manager := principalHeaders("manager", "managers")
	requestAs(t, server, manager, "PUT", "/api/sites/demo/db/expenses/a", []byte(`{"status":"approved","projectId":"A"}`), 403)
	requestAs(t, server, manager, "POST", "/api/sites/demo/actions/approve", []byte(`{"id":"b"}`), 403)
	requestAs(t, server, manager, "POST", "/api/sites/demo/actions/approve", []byte(`{"id":"own"}`), 403)
	requestAs(t, server, manager, "POST", "/api/sites/demo/actions/approve", []byte(`{"id":"a"}`), 200)
	requestAs(t, server, manager, "POST", "/api/sites/demo/actions/approve", []byte(`{"id":"a"}`), 403)
	document, err := database.Get(context.Background(), "demo", "expenses", "a")
	if err != nil || !strings.Contains(string(document.Data), `"approvedBy":"manager"`) {
		t.Fatalf("unexpected approval: %+v %v", document, err)
	}
}

func TestPermissionPolicyConcurrentCreateUsesActualOperation(t *testing.T) {
	policy := permissionPolicy()
	policy.Grants = map[string]hex.PermissionGrant{"create-only": {Kind: "collection", Resource: "items", Operations: []string{"create"}, Subjects: hex.Subjects{SignedIn: true}}}
	server, _, _ := setupPermissionPolicy(t, policy)
	var wait sync.WaitGroup
	statuses := make(chan int, 2)
	for i := 0; i < 2; i++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			r := httptest.NewRequest("PUT", "/api/sites/demo/db/items/shared", strings.NewReader(`{}`))
			r.Header = principalHeaders("alice")
			r.Header.Set("X-Hex-Request", "1")
			w := httptest.NewRecorder()
			server.ServeHTTP(w, r)
			statuses <- w.Code
		}()
	}
	wait.Wait()
	close(statuses)
	counts := map[int]int{}
	for status := range statuses {
		counts[status]++
	}
	if counts[200] != 1 || counts[403] != 1 {
		t.Fatalf("create authorization raced: %v", counts)
	}
}

func TestPermissionPolicyPortalStaleFormPreservesRevocation(t *testing.T) {
	policy := permissionPolicy()
	server, _, _ := setupPermissionPolicy(t, policy)
	owner := principalHeaders("owner-id")
	base := string(encode(t, policy))
	remote := hex.CloneSiteAccess(policy)
	delete(remote.Bindings, "project-approvers")
	requestAs(t, server, owner, "PUT", "/api/hex/sites/demo/access", encode(t, remote), 200)
	policy.Bindings["new"] = hex.RoleBinding{Role: "employee", Principal: "user:new"}
	form := url.Values{"base": {base}, "policy": {string(encode(t, policy))}}
	headers := owner.Clone()
	headers.Set("Content-Type", "application/x-www-form-urlencoded")
	requestAs(t, server, headers, "PUT", "/api/hex/manage/sites/demo/sharing", []byte(form.Encode()), 200)
	saved := showAccess(t, server, owner, "demo")
	if _, exists := saved.Bindings["project-approvers"]; exists {
		t.Fatal("stale portal form restored a revoked assignment")
	}
	if _, exists := saved.Bindings["new"]; !exists {
		t.Fatal("portal form did not save independent changes")
	}
}

func TestPermissionPolicyOmittedAccessPreservesPolicyAndBaseline(t *testing.T) {
	policy := permissionPolicy()
	server, policies, _ := setupPermissionPolicy(t, policy)
	owner := principalHeaders("owner-id")
	publishAccess(t, server, owner, "demo", string(encode(t, policy)), false)
	delete(policy.Bindings, "project-approvers")
	requestAs(t, server, owner, "PUT", "/api/hex/sites/demo/access", encode(t, policy), 200)
	before, err := policies.GetSiteAccess(t.Context(), "demo")
	if err != nil {
		t.Fatal(err)
	}
	files := map[string]string{"index.html": "updated page"}
	publishPublication(t, server, owner, "demo", files, publication{Files: manifest(files)})
	after, err := policies.GetSiteAccess(t.Context(), "demo")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, after) {
		t.Fatal("publication without access changed the live policy or local baseline")
	}
}

func TestPermissionPolicyLegacyCompletionDoesNotEvenMigrateDeployedPolicy(t *testing.T) {
	legacy := hex.SiteAccess{Owners: []string{"user:owner-id"}, Viewers: []string{"group:team"}}
	server, policies, _ := setupPermissionPolicy(t, legacy)
	requestAs(t, server, principalHeaders("owner-id"), "POST", "/api/hex/sites/demo/publish/complete", []byte(`{"files":[{"path":"index.html","size":1}],"access":{"viewers":[]}}`), 400)
	stored, err := policies.GetSiteAccess(t.Context(), "demo")
	if err != nil || stored.Version != 0 {
		t.Fatalf("invalid publication changed the deployed policy: %+v %v", stored, err)
	}
}

func TestPermissionPolicyIntegrationsUseCustomRolesAndPlatformGrants(t *testing.T) {
	policy := permissionPolicy()
	policy.Bindings["manager"] = hex.RoleBinding{Principal: "user:alice", Role: "approver"}
	policy.Grants["crm-write"] = hex.PermissionGrant{Kind: "integration", Resource: "crm.*", Operations: []string{"write"}, Subjects: hex.Subjects{Roles: []string{"approver"}}}
	access := memory.NewAccessStore()
	if err := access.PutSiteAccess(t.Context(), "demo", policy); err != nil {
		t.Fatal(err)
	}
	registry := new(hex.IntegrationRegistry)
	calls := 0
	if err := registry.Register(hex.Integration{Name: "crm", Title: "CRM", Endpoints: []hex.IntegrationEndpoint{{Name: "close", Description: "Close a record", Write: true, InputSchema: json.RawMessage(`{"type":"object"}`), OutputSchema: json.RawMessage(`{"type":"object"}`), Handler: func(context.Context, hex.IntegrationCall, json.RawMessage) (any, error) {
		calls++
		return map[string]bool{"closed": true}, nil
	}}}}); err != nil {
		t.Fatal(err)
	}
	server := hex.New(hex.Config{Access: access, Identity: easyauth.Resolver{}, Integrations: registry, IntegrationGrants: []hex.IntegrationGrant{{Principal: "*", Permissions: []string{"crm.*"}}}})
	requestAs(t, server, principalHeaders("alice"), "POST", "/api/sites/demo/integrations/crm/close", []byte(`{}`), 200)
	requestAs(t, server, principalHeaders("bob"), "POST", "/api/sites/demo/integrations/crm/close", []byte(`{}`), 403)
	requestAs(t, server, principalHeaders("owner-id"), "POST", "/api/sites/demo/integrations/crm/close", []byte(`{}`), 403)
	ungranted := hex.New(hex.Config{Access: access, Identity: easyauth.Resolver{}, Integrations: registry})
	requestAs(t, ungranted, principalHeaders("alice"), "POST", "/api/sites/demo/integrations/crm/close", []byte(`{}`), 403)
	if calls != 1 {
		t.Fatalf("unexpected integration calls: %d", calls)
	}
}

func TestPermissionPolicyManagementDoesNotBypassAdmission(t *testing.T) {
	policy := permissionPolicy()
	policy.Admission = hex.Subjects{Principals: []string{"user:alice"}}
	policy.Grants = map[string]hex.PermissionGrant{"read": {Kind: "collection", Resource: "*", Operations: []string{"read"}, Subjects: hex.Subjects{SignedIn: true}}}
	server, _, _ := setupPermissionPolicy(t, policy)
	requestAs(t, server, principalHeaders("owner-id"), "GET", "/api/hex/sites/demo/access", nil, 200)
	requestAs(t, server, principalHeaders("owner-id"), "GET", "/api/sites/demo/db/expenses", nil, 403)
	requestAs(t, server, principalHeaders("admin", "admin-group"), "GET", "/api/sites/demo/db/expenses", nil, 403)
	requestAs(t, server, principalHeaders("alice"), "GET", "/api/sites/demo/db/expenses", nil, 200)
}

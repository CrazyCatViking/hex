package hex_test

import (
	"encoding/json"
	"net/http"
	"net/url"
	"reflect"
	"strings"
	"testing"

	hex "github.com/crazycatviking/hex/server"
)

func guiEdit(t *testing.T, server *hex.Server, base hex.SiteAccess, action, id string, entry any, extra url.Values) string {
	t.Helper()
	values := url.Values{"base": {string(encode(t, base))}, "editor_action": {action}, "id": {id}}
	if entry != nil {
		values.Set("entry", string(encode(t, entry)))
	}
	for key, value := range extra {
		values[key] = value
	}
	return formRequest(t, server, principalHeaders("owner-id"), http.MethodPut, "/api/hex/manage/sites/demo/sharing", values, http.StatusOK)
}

func guiPolicy(t *testing.T, server *hex.Server) hex.SiteAccess {
	t.Helper()
	return showAccess(t, server, principalHeaders("owner-id"), "demo")
}

func TestPermissionGUISharingChangesPagesWithoutDataEscalation(t *testing.T) {
	policy := permissionPolicy()
	policy.Admission = hex.Subjects{Owners: true}
	policy.Grants["pages"] = hex.PermissionGrant{Kind: "path", Resource: "*", Except: []string{"/admin/"}, Operations: []string{"read"}, Subjects: hex.Subjects{Owners: true}}
	policy.Grants["admin-pages"] = hex.PermissionGrant{Kind: "path", Resource: "/admin/", Operations: []string{"read"}, Subjects: hex.Subjects{Owners: true}}
	server, store, _ := setupPermissionPolicy(t, policy)
	policy = guiPolicy(t, server)
	before, err := store.GetSiteAccess(t.Context(), "demo")
	if err != nil {
		t.Fatal(err)
	}
	result := guiEdit(t, server, policy, "sharing", "", nil, url.Values{"general": {"everyone"}, "principal": {"user:owner-id"}, "role": {"owner"}})
	if !strings.Contains(result, "Permissions saved") {
		t.Fatal(result)
	}
	saved := guiPolicy(t, server)
	if !saved.Admission.SignedIn || !reflect.DeepEqual(policy.Bindings, saved.Bindings) || !reflect.DeepEqual(policy.Roles, saved.Roles) {
		t.Fatal("sharing changed memberships or failed to open site entry")
	}
	for id, grant := range policy.Grants {
		if !reflect.DeepEqual(saved.Grants[id], grant) {
			t.Fatalf("sharing silently changed rule %s", id)
		}
	}
	after, err := store.GetSiteAccess(t.Context(), "demo")
	if err != nil || !reflect.DeepEqual(before.Published, after.Published) {
		t.Fatal("GUI edit changed the publication baseline", err)
	}
	viewer := principalHeaders("visitor")
	viewer.Set("X-Hex-Site", "demo")
	viewer.Set("X-Hex-Path", "/")
	requestAs(t, server, viewer, "GET", "/api/hex/authz", nil, 204)
	viewer.Set("X-Hex-Path", "/admin/settings")
	requestAs(t, server, viewer, "GET", "/api/hex/authz", nil, 403)
	requestAs(t, server, viewer, "PUT", "/api/sites/demo/db/unlisted/record", []byte(`{}`), 403)
	// A restricted preset revokes entry without deleting data permissions.
	result = guiEdit(t, server, saved, "sharing", "", nil, url.Values{"general": {"owners"}, "principal": {"user:owner-id"}, "role": {"owner"}})
	if !strings.Contains(result, "Permissions saved") {
		t.Fatal(result)
	}
	requestAs(t, server, viewer, "GET", "/api/sites/demo/db/expenses", nil, 403)
}

func TestPermissionGUIUnchangedSharingPreservesCustomRules(t *testing.T) {
	policy := permissionPolicy()
	policy.Admission = hex.Subjects{Owners: true, Roles: []string{"approver"}, Principals: []string{"user:direct"}}
	server, _, _ := setupPermissionPolicy(t, policy)
	policy = guiPolicy(t, server)
	result := guiEdit(t, server, policy, "sharing", "", nil, url.Values{"general": {"custom"}, "principal": {"user:owner-id", "user:direct"}, "role": {"owner", "viewer"}})
	if !strings.Contains(result, "Permissions saved") {
		t.Fatal(result)
	}
	if !reflect.DeepEqual(policy, guiPolicy(t, server)) {
		t.Fatal("unchanged GUI form reconstructed a custom policy")
	}
}

func TestPermissionGUICustomInvitationDoesNotOpenPagesToOtherRoles(t *testing.T) {
	policy := permissionPolicy()
	policy.Admission = hex.Subjects{Owners: true, Roles: []string{"approver"}}
	policy.Grants["pages"] = hex.PermissionGrant{Kind: "path", Resource: "*", Operations: []string{"read"}, Subjects: hex.Subjects{Owners: true}}
	server, _, _ := setupPermissionPolicy(t, policy)
	result := guiEdit(t, server, policy, "sharing", "", nil, url.Values{"general": {"custom"}, "principal": {"user:owner-id", "user:invited"}, "role": {"owner", "viewer"}})
	if !strings.Contains(result, "Permissions saved") {
		t.Fatal(result)
	}
	for _, scenario := range []struct {
		id     string
		groups []string
		status int
	}{{"invited", nil, 204}, {"reviewer", []string{"managers"}, 403}} {
		headers := principalHeaders(scenario.id, scenario.groups...)
		headers.Set("X-Hex-Site", "demo")
		headers.Set("X-Hex-Path", "/")
		requestAs(t, server, headers, "GET", "/api/hex/authz", nil, scenario.status)
	}
}

func TestPermissionGUIGroupMembershipAndConditionalRules(t *testing.T) {
	policy := permissionPolicy()
	policy.Admission = hex.Subjects{Owners: true}
	server, _, database := setupPermissionPolicy(t, policy)
	result := guiEdit(t, server, policy, "role", "reviewers", hex.ApplicationRole{Description: "Project reviewers"}, url.Values{"creating": {"true"}, "admit_role": {"true"}})
	if !strings.Contains(result, "Permissions saved") {
		t.Fatal(result)
	}
	policy = guiPolicy(t, server)
	result = guiEdit(t, server, policy, "binding", "", hex.RoleBinding{Principal: "user:reviewer", Role: "reviewers", Scope: &hex.RoleScope{Kind: "project", ID: "A"}}, nil)
	if !strings.Contains(result, "Permissions saved") {
		t.Fatal(result)
	}
	policy = guiPolicy(t, server)
	grant := hex.PermissionGrant{Kind: "collection", Resource: "cases", Operations: []string{"read", "update"}, Subjects: hex.Subjects{Roles: []string{"reviewers"}}, Scope: &hex.PermissionScope{Kind: "project", Field: "data.projectId"}, Fields: []string{"amount"}, Where: &hex.RecordCondition{Field: "data.status", Op: "eq", Value: json.RawMessage(`"draft"`)}}
	result = guiEdit(t, server, policy, "grant", "", grant, nil)
	if !strings.Contains(result, "Permissions saved") {
		t.Fatal(result)
	}
	policy = guiPolicy(t, server)
	for _, project := range []string{"A", "B"} {
		_, err := database.Put(t.Context(), "demo", "cases", project, json.RawMessage(`{"projectId":"`+project+`","status":"draft","amount":1}`), hex.WriteOptions{Creator: "someone-else"})
		if err != nil {
			t.Fatal(err)
		}
	}
	reviewer := principalHeaders("reviewer")
	reviewer.Set("X-Hex-Site", "demo")
	reviewer.Set("X-Hex-Path", "/")
	requestAs(t, server, reviewer, "GET", "/api/hex/authz", nil, 204)
	requestAs(t, server, reviewer, "GET", "/api/sites/demo/db/cases/A", nil, 200)
	requestAs(t, server, reviewer, "GET", "/api/sites/demo/db/cases/B", nil, 404)
	requestAs(t, server, reviewer, "PUT", "/api/sites/demo/db/cases/A", []byte(`{"projectId":"A","status":"draft","amount":2}`), 200)
	requestAs(t, server, reviewer, "PUT", "/api/sites/demo/db/cases/A", []byte(`{"projectId":"A","status":"approved","amount":2}`), 403)
	var bindingID string
	for id, binding := range policy.Bindings {
		if binding.Role == "reviewers" {
			bindingID = id
		}
	}
	result = guiEdit(t, server, policy, "remove-binding", bindingID, nil, nil)
	if !strings.Contains(result, "Permissions saved") {
		t.Fatal(result)
	}
	requestAs(t, server, reviewer, "GET", "/api/hex/authz", nil, 403)
	requestAs(t, server, reviewer, "GET", "/api/sites/demo/db/cases/A", nil, 403)
}

func TestPermissionGUIEditsMergeSafelyAndPreserveExactConditions(t *testing.T) {
	policy := permissionPolicy()
	policy.Grants["exact"] = hex.PermissionGrant{Kind: "collection", Resource: "ledger", Operations: []string{"read"}, Subjects: hex.Subjects{Owners: true}, Where: &hex.RecordCondition{Field: "data.amount", Op: "eq", Value: json.RawMessage(`9007199254740993`)}}
	server, _, _ := setupPermissionPolicy(t, policy)
	remote := hex.CloneSiteAccess(policy)
	delete(remote.Bindings, "project-approvers")
	requestAs(t, server, principalHeaders("owner-id"), "PUT", "/api/hex/sites/demo/access", encode(t, remote), 200)
	changed := hex.CloneSiteAccess(policy).Grants["exact"]
	changed.Operations = []string{"read", "delete"}
	changed.Where.Value = json.RawMessage(`9007199254740992`)
	result := guiEdit(t, server, policy, "grant", "exact", changed, url.Values{"keep_where": {"true"}})
	if !strings.Contains(result, "Permissions saved") {
		t.Fatal(result)
	}
	saved := guiPolicy(t, server)
	if string(saved.Grants["exact"].Where.Value) != "9007199254740993" {
		t.Fatal("browser-rounded condition changed despite no condition edit")
	}
	if _, exists := saved.Bindings["project-approvers"]; exists {
		t.Fatal("GUI save resurrected a concurrent revocation")
	}
	changed = policy.Grants["exact"]
	changed.Resource = "different"
	result = guiEdit(t, server, policy, "grant", "exact", changed, nil)
	if !strings.Contains(result, "Nothing was saved") {
		t.Fatal("conflicting rule changes were guessed", result)
	}
	if guiPolicy(t, server).Grants["exact"].Resource != "ledger" {
		t.Fatal("conflict changed stored policy")
	}
}

func TestPermissionGUIRejectsInvalidOrDestructiveEdits(t *testing.T) {
	policy := permissionPolicy()
	server, _, _ := setupPermissionPolicy(t, policy)
	policy = guiPolicy(t, server)
	for _, values := range []url.Values{
		{"editor_action": {"sharing"}, "general": {"everyone"}},
		{"editor_action": {"sharing"}, "general": {"everyone"}, "principal": {"user:outsider"}, "role": {"owner"}},
		{"editor_action": {"remove-role"}, "id": {"approver"}},
		{"editor_action": {"grant"}, "id": {"pages"}, "entry": {`{"kind":"collection","resource":"expenses","operations":["write"],"subjects":{"owners":true}}`}},
		{"editor_action": {"role"}, "id": {"new"}, "creating": {"true"}, "entry": {`{"description":"x","unknown":true}`}},
	} {
		values.Set("base", string(encode(t, policy)))
		body := formRequest(t, server, principalHeaders("owner-id"), "PUT", "/api/hex/manage/sites/demo/sharing", values, 200)
		if !strings.Contains(body, "banner-error") {
			t.Fatal("invalid GUI edit accepted", body)
		}
		if !reflect.DeepEqual(policy, guiPolicy(t, server)) {
			t.Fatal("invalid edit changed policy")
		}
	}
	formRequest(t, server, principalHeaders("outsider"), "PUT", "/api/hex/manage/sites/demo/sharing", url.Values{"base": {string(encode(t, policy))}, "editor_action": {"remove-grant"}, "id": {"pages"}}, 403)
	// Editor resources stay on the platform host, protected like manage.js.
	asset := requestAs(t, server, principalHeaders("owner-id"), "GET", "/api/hex/permissions.js", nil, 200)
	if !strings.Contains(asset.Body.String(), "HexPermissionEditor") {
		t.Fatal("permission GUI script unavailable")
	}
	styles := requestAs(t, server, principalHeaders("owner-id"), "GET", "/api/hex/permissions.css", nil, 200)
	if !strings.Contains(styles.Body.String(), "#101a21") || !strings.Contains(styles.Body.String(), ".permissions-page") {
		t.Fatal("approved permission design stylesheet unavailable")
	}
}

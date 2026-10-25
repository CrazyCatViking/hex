package hex_test

import (
	"encoding/json"
	"net/url"
	"reflect"
	"strings"
	"testing"

	hex "github.com/crazycatviking/hex/server"
)

type matrixEdit struct {
	Action string     `json:"action"`
	Values url.Values `json:"values"`
}

func matrixValue(t *testing.T, action string, values url.Values, entry any) matrixEdit {
	t.Helper()
	if values == nil {
		values = url.Values{}
	}
	if entry != nil {
		values.Set("entry", string(encode(t, entry)))
	}
	return matrixEdit{Action: action, Values: values}
}

func saveMatrix(t *testing.T, server *hex.Server, base hex.SiteAccess, edits ...matrixEdit) string {
	t.Helper()
	return guiEdit(t, server, base, "matrix", "", edits, nil)
}

func TestPermissionMatrixCreatesGroupRulesAndMembershipAtomically(t *testing.T) {
	policy := permissionPolicy()
	policy.Admission = hex.Subjects{Owners: true}
	baseline := hex.CloneSiteAccess(policy)
	policy.Published = &baseline
	server, _, database := setupPermissionPolicy(t, policy)
	policy = guiPolicy(t, server)
	grant := hex.PermissionGrant{Kind: "collection", Resource: "requests", Operations: []string{"read"}, Subjects: hex.Subjects{Roles: []string{"reviewers"}}, Where: &hex.RecordCondition{Field: "data.status", Op: "eq", Value: json.RawMessage(`"submitted"`)}}
	result := saveMatrix(t, server, policy,
		matrixValue(t, "role", url.Values{"id": {"reviewers"}, "creating": {"true"}, "assign_owners": {"true"}}, hex.ApplicationRole{Name: "Review requests", Description: "Read submitted requests"}),
		matrixValue(t, "grant", url.Values{"id": {"review-rule"}, "creating": {"true"}}, grant),
		matrixValue(t, "membership", url.Values{"principal": {"user:alex"}, "group": {"reviewers"}, "assigned": {"true"}}, nil),
		matrixValue(t, "admission", url.Values{"general": {"selected"}, "principal": {"user:alex"}}, nil),
	)
	if !strings.Contains(result, "Permissions saved") {
		t.Fatal(result)
	}
	saved := guiPolicy(t, server)
	if saved.Roles["reviewers"].Name != "Review requests" {
		t.Fatal("group display name missing")
	}
	if !reflect.DeepEqual(saved.Published, policy.Published) {
		t.Fatal("table save rewrote publication baseline")
	}
	if !reflect.DeepEqual(saved.Grants["approve"], policy.Grants["approve"]) {
		t.Fatal("table save changed custom grant")
	}
	ownerAssigned := false
	for _, binding := range saved.Bindings {
		if binding.Principal == "user:owner-id" && binding.Role == "reviewers" {
			ownerAssigned = true
		}
	}
	if !ownerAssigned {
		t.Fatal("new group owner default was not explicit")
	}
	for _, status := range []string{"draft", "submitted"} {
		if _, err := database.Put(t.Context(), "demo", "requests", status, json.RawMessage(`{"status":"`+status+`"}`), hex.WriteOptions{Creator: "bea"}); err != nil {
			t.Fatal(err)
		}
	}
	requestAs(t, server, principalHeaders("alex"), "GET", "/api/sites/demo/db/requests/submitted", nil, 200)
	requestAs(t, server, principalHeaders("alex"), "GET", "/api/sites/demo/db/requests/draft", nil, 404)
	requestAs(t, server, principalHeaders("alex"), "DELETE", "/api/sites/demo/db/requests/submitted", nil, 403)
	result = saveMatrix(t, server, saved, matrixValue(t, "membership", url.Values{"principal": {"user:alex"}, "group": {"reviewers"}, "assigned": {"false"}}, nil))
	if !strings.Contains(result, "Permissions saved") {
		t.Fatal(result)
	}
	requestAs(t, server, principalHeaders("alex"), "GET", "/api/sites/demo/db/requests/submitted", nil, 403)
}

func TestPermissionMatrixPreservesUntouchedBindingsAndExactConditions(t *testing.T) {
	policy := permissionPolicy()
	policy.Grants["exact"] = hex.PermissionGrant{Kind: "collection", Resource: "ledger", Operations: []string{"read"}, Subjects: hex.Subjects{Owners: true}, Where: &hex.RecordCondition{Field: "data.amount", Op: "eq", Value: json.RawMessage(`9007199254740993`)}}
	server, _, _ := setupPermissionPolicy(t, policy)
	policy = guiPolicy(t, server)
	binding := policy.Bindings["project-approvers"]
	scopes := []*hex.RoleScope{binding.Scope}
	result := saveMatrix(t, server, policy, matrixValue(t, "membership", url.Values{"principal": {binding.Principal}, "group": {binding.Role}, "assigned": {"true"}, "scopes": {string(encode(t, scopes))}}, nil))
	if !strings.Contains(result, "Permissions saved") {
		t.Fatal(result)
	}
	if !reflect.DeepEqual(guiPolicy(t, server).Bindings, policy.Bindings) {
		t.Fatal("same membership changed stable binding IDs")
	}
	grant := policy.Grants["exact"]
	grant.Operations = []string{"read", "delete"}
	grant.Where = &hex.RecordCondition{Field: "data.amount", Op: "eq", Value: json.RawMessage(`9007199254740992`)}
	result = saveMatrix(t, server, policy, matrixValue(t, "grant", url.Values{"id": {"exact"}, "keep_where": {"true"}}, grant))
	if !strings.Contains(result, "Permissions saved") {
		t.Fatal(result)
	}
	if string(guiPolicy(t, server).Grants["exact"].Where.Value) != "9007199254740993" {
		t.Fatal("exact condition rounded by table save")
	}
}

func TestPermissionMatrixDeleteGroupKeepsSharedRuleAudiences(t *testing.T) {
	policy := permissionPolicy()
	policy.Roles["extra"] = hex.ApplicationRole{Description: "Another group"}
	policy.Grants["shared"] = hex.PermissionGrant{Kind: "collection", Resource: "shared", Operations: []string{"read"}, Subjects: hex.Subjects{Roles: []string{"approver", "extra"}, Principals: []string{"user:another"}}}
	policy.Admission.Roles = []string{"approver"}
	server, _, _ := setupPermissionPolicy(t, policy)
	policy = guiPolicy(t, server)
	result := saveMatrix(t, server, policy, matrixValue(t, "delete-group", url.Values{"id": {"approver"}}, nil))
	if !strings.Contains(result, "Permissions saved") {
		t.Fatal(result)
	}
	saved := guiPolicy(t, server)
	if _, exists := saved.Roles["approver"]; exists {
		t.Fatal("role not removed")
	}
	for _, binding := range saved.Bindings {
		if binding.Role == "approver" {
			t.Fatal("membership survived group removal")
		}
	}
	for _, grant := range saved.Grants {
		for _, role := range grant.Subjects.Roles {
			if role == "approver" {
				t.Fatal("deleted group left in rule")
			}
		}
	}
	shared := saved.Grants["shared"]
	if !reflect.DeepEqual(shared.Subjects.Roles, []string{"extra"}) || !reflect.DeepEqual(shared.Subjects.Principals, []string{"user:another"}) {
		t.Fatal("shared rule lost other audiences")
	}
}

func TestPermissionMatrixRejectsStaleMembershipCellsAndOwnerExpansion(t *testing.T) {
	for _, scenario := range []string{"cell", "row", "owners"} {
		t.Run(scenario, func(t *testing.T) {
			policy := permissionPolicy()
			server, _, _ := setupPermissionPolicy(t, policy)
			policy = guiPolicy(t, server)
			remote := hex.CloneSiteAccess(policy)
			var edit matrixEdit
			switch scenario {
			case "cell":
				remote.Bindings["another-scope"] = hex.RoleBinding{Principal: "group:managers", Role: "approver", Scope: &hex.RoleScope{Kind: "project", ID: "B"}}
				edit = matrixValue(t, "membership", url.Values{"principal": {"group:managers"}, "group": {"approver"}, "assigned": {"false"}}, nil)
			case "row":
				remote.Bindings["another-scope"] = hex.RoleBinding{Principal: "user:alex", Role: "approver"}
				edit = matrixValue(t, "remove-principal", url.Values{"principal": {"user:alex"}}, nil)
			case "owners":
				remote.Owners = append(remote.Owners, "user:new-owner")
				edit = matrixValue(t, "role", url.Values{"id": {"new"}, "creating": {"true"}, "assign_owners": {"true"}}, hex.ApplicationRole{Name: "New"})
			}
			requestAs(t, server, principalHeaders("owner-id"), "PUT", "/api/hex/sites/demo/access", encode(t, remote), 200)
			before := guiPolicy(t, server)
			result := saveMatrix(t, server, policy, edit)
			if !strings.Contains(result, "Nothing was saved") {
				t.Fatal("stale aggregate edit accepted", result)
			}
			if !reflect.DeepEqual(before, guiPolicy(t, server)) {
				t.Fatal("conflict wrote a partial batch")
			}
		})
	}
}

func TestPermissionMatrixInvalidBatchDoesNotSavePartialChanges(t *testing.T) {
	policy := permissionPolicy()
	server, _, _ := setupPermissionPolicy(t, policy)
	policy = guiPolicy(t, server)
	result := saveMatrix(t, server, policy,
		matrixValue(t, "role", url.Values{"id": {"new"}, "creating": {"true"}}, hex.ApplicationRole{Name: "New"}),
		matrixValue(t, "membership", url.Values{"principal": {"user:alex"}, "group": {"new"}, "assigned": {"true"}, "scopes": {"[]"}}, nil),
	)
	if !strings.Contains(result, "banner-error") {
		t.Fatal(result)
	}
	if !reflect.DeepEqual(policy, guiPolicy(t, server)) {
		t.Fatal("invalid batch wrote earlier entries")
	}
}

func TestPermissionMatrixAdmissionReplacementRejectsConcurrentAudienceChanges(t *testing.T) {
	policy := permissionPolicy()
	policy.Admission = hex.Subjects{Owners: true, Principals: []string{"user:alex"}}
	server, _, _ := setupPermissionPolicy(t, policy)
	policy = guiPolicy(t, server)
	remote := hex.CloneSiteAccess(policy)
	remote.Admission.Principals = append(remote.Admission.Principals, "user:bea")
	requestAs(t, server, principalHeaders("owner-id"), "PUT", "/api/hex/sites/demo/access", encode(t, remote), 200)
	result := saveMatrix(t, server, policy, matrixValue(t, "admission", url.Values{"general": {"owners"}}, nil))
	if !strings.Contains(result, "Nothing was saved") {
		t.Fatal("restrictive setting silently kept concurrent new audience", result)
	}
	if !reflect.DeepEqual(guiPolicy(t, server).Admission, remote.Admission) {
		t.Fatal("conflict changed admission")
	}
}

func TestPermissionMatrixIndependentEditPreservesRemoteRevocation(t *testing.T) {
	policy := permissionPolicy()
	server, _, _ := setupPermissionPolicy(t, policy)
	policy = guiPolicy(t, server)
	remote := hex.CloneSiteAccess(policy)
	delete(remote.Bindings, "project-approvers")
	requestAs(t, server, principalHeaders("owner-id"), "PUT", "/api/hex/sites/demo/access", encode(t, remote), 200)
	role := policy.Roles["employee"]
	role.Name = "Employees"
	result := saveMatrix(t, server, policy, matrixValue(t, "role", url.Values{"id": {"employee"}}, role))
	if !strings.Contains(result, "Permissions saved") {
		t.Fatal(result)
	}
	saved := guiPolicy(t, server)
	if _, exists := saved.Bindings["project-approvers"]; exists {
		t.Fatal("table edit resurrected revoked membership")
	}
	if saved.Roles["employee"].Name != "Employees" {
		t.Fatal("independent display name edit did not merge")
	}
}

func TestPermissionMatrixCanCorrectAnInvalidDraftBeforeSaving(t *testing.T) {
	policy := permissionPolicy()
	server, _, _ := setupPermissionPolicy(t, policy)
	policy = guiPolicy(t, server)
	invalid := policy.Grants["own-read"]
	invalid.Resource = "invalid/name"
	corrected := policy.Grants["own-read"]
	corrected.Resource = "requests"
	result := saveMatrix(t, server, policy,
		matrixValue(t, "grant", url.Values{"id": {"own-read"}}, invalid),
		matrixValue(t, "grant", url.Values{"id": {"own-read"}}, corrected),
	)
	if !strings.Contains(result, "Permissions saved") {
		t.Fatal("an earlier invalid draft prevented its correction", result)
	}
	if guiPolicy(t, server).Grants["own-read"].Resource != "requests" {
		t.Fatal("corrected final draft not saved")
	}
}

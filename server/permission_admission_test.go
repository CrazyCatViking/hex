package hex_test

import (
	"encoding/json"
	"net/url"
	"reflect"
	"slices"
	"strings"
	"testing"

	hex "github.com/crazycatviking/hex/server"
)

func TestPermissionAdmissionMigrationSeparatesGroupsAndKeepsPublicationBaseline(t *testing.T) {
	policy := hex.SiteAccess{
		Version: 2, Owners: []string{"user:owner-id"},
		Admission: hex.Subjects{Owners: true, Principals: []string{"user:direct"}, Roles: []string{"members"}},
		Roles:     map[string]hex.ApplicationRole{"members": {}},
		Bindings: map[string]hex.RoleBinding{
			"alice": {Principal: "user:alice", Role: "members"},
			"bob":   {Principal: "user:bob", Role: "members", Scope: &hex.RoleScope{Kind: "project", ID: "A"}},
			"team":  {Principal: "group:team", Role: "members"},
		},
		Grants: map[string]hex.PermissionGrant{
			"pages":   {Kind: "path", Resource: "*", Except: []string{"/admin/"}, Operations: []string{"read"}, Subjects: hex.Subjects{Roles: []string{"members"}}},
			"admin":   {Kind: "path", Resource: "/admin/", Operations: []string{"read"}, Subjects: hex.Subjects{Roles: []string{"members"}}},
			"records": {Kind: "collection", Resource: "ledger", Operations: []string{"read", "update"}, Subjects: hex.Subjects{Roles: []string{"members"}}, Fields: []string{"amount"}, Where: &hex.RecordCondition{Field: "data.amount", Op: "eq", Value: json.RawMessage(`9007199254740993`)}},
		},
	}
	baseline := hex.CloneSiteAccess(policy)
	baseline.Bindings["revoked"] = hex.RoleBinding{Principal: "user:revoked", Role: "members"}
	policy.Published = &baseline
	server, policies, _ := setupPermissionPolicy(t, policy)
	migrated := guiPolicy(t, server)
	if len(migrated.Admission.Roles) != 0 || !sameStrings(migrated.Admission.Principals, []string{"user:alice", "user:bob", "user:direct", "group:team"}) {
		t.Fatalf("unexpected migrated admission: %+v", migrated.Admission)
	}
	if !reflect.DeepEqual(migrated.Grants["records"], policy.Grants["records"]) || !reflect.DeepEqual(migrated.Grants["admin"], policy.Grants["admin"]) || !reflect.DeepEqual(migrated.Bindings, policy.Bindings) {
		t.Fatal("admission migration changed app rules, protected pages, or membership limits")
	}
	stored, err := policies.GetSiteAccess(t.Context(), "demo")
	if err != nil {
		t.Fatal(err)
	}
	if len(stored.Published.Admission.Roles) != 0 || !slices.Contains(stored.Published.Admission.Principals, "user:revoked") || slices.Contains(stored.Admission.Principals, "user:revoked") {
		t.Fatal("migration replaced the publication baseline with live access")
	}
	if !reflect.DeepEqual(hex.MigrateSiteAccess(stored), stored) {
		t.Fatal("migration is not idempotent")
	}
	for _, scenario := range []struct {
		id     string
		status int
	}{{"alice", 204}, {"bob", 403}, {"direct", 403}, {"outsider", 403}, {"revoked", 403}} {
		headers := principalHeaders(scenario.id)
		headers.Set("X-Hex-Site", "demo")
		headers.Set("X-Hex-Path", "/")
		requestAs(t, server, headers, "GET", "/api/hex/authz", nil, scenario.status)
	}
	// App membership/default changes cannot admit outsiders or remove entry.
	result := saveMatrix(t, server, migrated,
		matrixValue(t, "role", url.Values{"id": {"members"}}, hex.ApplicationRole{SignedIn: true}),
		matrixValue(t, "membership", url.Values{"principal": {"user:outsider"}, "group": {"members"}, "assigned": {"true"}}, nil),
		matrixValue(t, "remove-principal", url.Values{"principal": {"user:alice"}}, nil))
	if !strings.Contains(result, "Permissions saved") {
		t.Fatal(result)
	}
	changed := guiPolicy(t, server)
	if !reflect.DeepEqual(changed.Admission, migrated.Admission) {
		t.Fatal("app assignments changed admission")
	}
	result = saveMatrix(t, server, changed, matrixValue(t, "delete-group", url.Values{"id": {"members"}}, nil))
	if !strings.Contains(result, "Permissions saved") {
		t.Fatal(result)
	}
	for _, scenario := range []struct {
		id     string
		status int
	}{{"alice", 204}, {"outsider", 403}, {"revoked", 403}} {
		headers := principalHeaders(scenario.id)
		headers.Set("X-Hex-Site", "demo")
		headers.Set("X-Hex-Path", "/")
		requestAs(t, server, headers, "GET", "/api/hex/authz", nil, scenario.status)
	}
	publishAccess(t, server, principalHeaders("owner-id"), "demo", string(encode(t, *stored.Published)), false)
	after := guiPolicy(t, server)
	if slices.Contains(after.Admission.Principals, "user:revoked") || !reflect.DeepEqual(after.Admission, migrated.Admission) || len(after.Roles) != 0 {
		t.Fatal("unchanged publication resurrected revoked entry or app permissions")
	}
}

func sameStrings(left, right []string) bool {
	return slices.Equal(slices.Sorted(slices.Values(left)), slices.Sorted(slices.Values(right)))
}

func TestPermissionAdmissionDefaultMigrationAndNewPolicyValidation(t *testing.T) {
	policy := permissionPolicy()
	policy.Admission = hex.Subjects{Owners: true, Roles: []string{"employee"}}
	migrated := hex.MigrateSiteAccess(policy)
	if !migrated.Admission.SignedIn || len(migrated.Admission.Roles) != 0 {
		t.Fatal("default admission was not preserved independently")
	}
	if err := hex.ValidatePublishedAccess(policy); err == nil || !strings.Contains(err.Error(), "admission cannot reference") {
		t.Fatal("new role-based admission should be rejected with migration guidance", err)
	}
	if err := hex.ValidatePublishedAccess(migrated); err != nil {
		t.Fatal(err)
	}
}

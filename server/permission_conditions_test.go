package hex

import (
	"encoding/json"
	"testing"
)

func TestPermissionConditionsHandleJSONNumbersWithoutExponentExpansion(t *testing.T) {
	for _, pair := range [][2]string{{"1", "1.00"}, {"100", "1e2"}, {"-0", "0.00"}, {"1e1000000000", "10e999999999"}, {"-1.20e-1000000000", "-12e-1000000001"}} {
		if !equalConditionValue(json.Number(pair[0]), json.Number(pair[1])) {
			t.Fatalf("equal decimal values differ: %v", pair)
		}
	}
	if equalConditionValue(json.Number("100000000000000000001"), json.Number("100000000000000000002")) {
		t.Fatal("numeric comparison lost precision")
	}
}

func TestPermissionActorTokenUsesDecodedJSONValue(t *testing.T) {
	condition := &RecordCondition{Field: "createdBy", Op: "ne", Value: json.RawMessage(`"$actor\u002eid"`)}
	resolved := resolveRecordCondition(condition, &Identity{ID: "alice"})
	if resolved.Matches(Document{ID: "one", CreatedBy: "alice"}) || !resolved.Matches(Document{ID: "one", CreatedBy: "bob"}) {
		t.Fatal("escaped actor token bypassed the actor constraint")
	}
}

func TestPermissionMutationRequiresOneCompleteGrant(t *testing.T) {
	policy := SiteAccess{Version: 2, Admission: Subjects{SignedIn: true}, Grants: map[string]PermissionGrant{
		"amount": {Kind: "collection", Resource: "expenses", Operations: []string{"update"}, Subjects: Subjects{SignedIn: true}, Fields: []string{"amount"}},
		"status": {Kind: "collection", Resource: "expenses", Operations: []string{"update"}, Subjects: Subjects{SignedIn: true}, Fields: []string{"status"}},
	}}
	authorization := siteAuthorization{access: policy, role: roleViewer, identity: &Identity{ID: "alice"}}
	before := Document{ID: "one", Data: json.RawMessage(`{"amount":10,"status":"draft"}`), CreatedBy: "alice"}
	after := before
	after.Data = json.RawMessage(`{"amount":20,"status":"approved"}`)
	if authorization.canMutate("expenses", "update", &before, after) {
		t.Fatal("mutation combined two partial field grants")
	}
	grant := policy.Grants["amount"]
	grant.Fields = []string{}
	policy.Grants = map[string]PermissionGrant{"no-fields": grant}
	authorization.access = CloneSiteAccess(policy)
	if authorization.access.Grants["no-fields"].Fields == nil {
		t.Fatal("clone broadened an empty field allowlist")
	}
	after.Data = json.RawMessage(`{"amount":20,"status":"draft"}`)
	if authorization.canMutate("expenses", "update", &before, after) {
		t.Fatal("empty field allowlist allowed a mutation")
	}
}

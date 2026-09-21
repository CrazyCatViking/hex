package easyauth

import (
	"encoding/base64"
	"encoding/json"
	"net/http/httptest"
	"testing"
)

func encodePrincipal(t *testing.T, principal map[string]any) string {
	t.Helper()
	data, err := json.Marshal(principal)
	if err != nil {
		t.Fatal(err)
	}
	return base64.StdEncoding.EncodeToString(data)
}

func TestResolveIdentityFromClientPrincipal(t *testing.T) {
	r := httptest.NewRequest("GET", "/api/hex/me", nil)
	r.Header.Set("X-Ms-Client-Principal-Id", "object-id")
	r.Header.Set("X-Ms-Client-Principal-Name", "user@example.test")
	r.Header.Set("X-Ms-Client-Principal", encodePrincipal(t, map[string]any{
		"auth_typ": "aad",
		"name_typ": "name",
		"role_typ": "roles",
		"claims": []map[string]string{
			{"typ": "groups", "val": "11111111-aaaa-4bbb-8ccc-222222222222"},
			{"typ": "groups", "val": "sales"},
			{"typ": "roles", "val": "Hex.Admin"},
			{"typ": "name", "val": "Displayed Name"},
		},
	}))

	identity, err := Resolver{}.ResolveIdentity(r)
	if err != nil {
		t.Fatal(err)
	}
	if identity.Provider != "aad" || identity.ID != "object-id" || identity.Name != "user@example.test" {
		t.Fatalf("unexpected identity: %+v", identity)
	}
	if len(identity.Groups) != 2 || identity.Groups[1] != "sales" {
		t.Fatalf("unexpected groups: %v", identity.Groups)
	}
	if len(identity.Roles) != 1 || identity.Roles[0] != "Hex.Admin" {
		t.Fatalf("unexpected roles: %v", identity.Roles)
	}
}

func TestResolveIdentityFallsBackToClaims(t *testing.T) {
	r := httptest.NewRequest("GET", "/api/hex/me", nil)
	r.Header.Set("X-Ms-Client-Principal", encodePrincipal(t, map[string]any{
		"auth_typ": "aad",
		"name_typ": "preferred_username",
		"claims": []map[string]string{
			{"typ": "oid", "val": "claim-object-id"},
			{"typ": "preferred_username", "val": "user@example.test"},
		},
	}))

	identity, err := Resolver{}.ResolveIdentity(r)
	if err != nil {
		t.Fatal(err)
	}
	if identity.ID != "claim-object-id" || identity.Name != "user@example.test" {
		t.Fatalf("unexpected identity: %+v", identity)
	}
}

func TestResolveIdentityRejectsBadInput(t *testing.T) {
	anonymous := httptest.NewRequest("GET", "/", nil)
	identity, err := Resolver{}.ResolveIdentity(anonymous)
	if identity != nil || err != nil {
		t.Fatalf("expected anonymous request, got %+v, %v", identity, err)
	}

	invalid := map[string]string{
		"not base64":         "%%%",
		"not JSON":           base64.StdEncoding.EncodeToString([]byte("hello")),
		"missing identifier": "",
	}
	invalid["missing identifier"] = base64.StdEncoding.EncodeToString([]byte(`{"auth_typ":"aad","claims":[]}`))
	for name, value := range invalid {
		t.Run(name, func(t *testing.T) {
			r := httptest.NewRequest("GET", "/", nil)
			r.Header.Set("X-Ms-Client-Principal", value)
			resolver := Resolver{}
			if _, err := resolver.ResolveIdentity(r); err == nil {
				t.Fatal("expected an error")
			}
		})
	}
}

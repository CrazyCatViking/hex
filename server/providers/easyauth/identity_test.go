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

func TestResolveIdentityEmail(t *testing.T) {
	resolve := func(claims []map[string]string) string {
		t.Helper()
		r := httptest.NewRequest("GET", "/api/hex/me", nil)
		r.Header.Set("X-Ms-Client-Principal-Id", "object-id")
		r.Header.Set("X-Ms-Client-Principal-Name", "Displayed Name")
		r.Header.Set("X-Ms-Client-Principal", encodePrincipal(t, map[string]any{"auth_typ": "aad", "name_typ": "name", "claims": claims}))
		identity, err := Resolver{}.ResolveIdentity(r)
		if err != nil {
			t.Fatal(err)
		}
		return identity.Email
	}

	if email := resolve([]map[string]string{{"typ": "preferred_username", "val": "alex@example.test"}}); email != "alex@example.test" {
		t.Fatalf("preferred_username not used: %q", email)
	}
	both := []map[string]string{{"typ": "preferred_username", "val": "alex@contoso.test"}, {"typ": "email", "val": "alex@example.test"}}
	if email := resolve(both); email != "alex@example.test" {
		t.Fatalf("email claim should win: %q", email)
	}
	if email := resolve([]map[string]string{{"typ": "preferred_username", "val": "not-an-email"}}); email != "" {
		t.Fatalf("a non-email value was used: %q", email)
	}
}

func TestGUIDCanonicalizationIsEntraSpecific(t *testing.T) {
	upper := "11111111-AAAA-4BBB-8CCC-222222222222"
	lower := "11111111-aaaa-4bbb-8ccc-222222222222"
	for _, provider := range []string{"aad", "oidc"} {
		r := httptest.NewRequest("GET", "/", nil)
		r.Header.Set("X-Ms-Client-Principal-Id", upper)
		r.Header.Set("X-Ms-Client-Principal", encodePrincipal(t, map[string]any{
			"auth_typ": provider,
			"claims": []map[string]string{
				{"typ": "groups", "val": upper}, {"typ": "groups", "val": "Opaque:AbC|1"},
				{"typ": "roles", "val": "Hex.Admin"}, {"typ": "roles", "val": upper},
			},
		}))
		identity, err := (Resolver{}).ResolveIdentity(r)
		if err != nil {
			t.Fatal(err)
		}
		want := upper
		if provider == "aad" {
			want = lower
		}
		if identity.ID != want || identity.Groups[0] != want || identity.Groups[1] != "Opaque:AbC|1" || identity.Roles[0] != "Hex.Admin" || identity.Roles[1] != upper {
			t.Fatalf("provider %s changed opaque claims or failed GUID normalization: %+v", provider, identity)
		}
	}
	for _, test := range []struct{ principal, want string }{
		{upper, lower}, {"user:" + upper, "user:" + lower}, {"group:" + upper, "group:" + lower},
		{"role:" + upper, "role:" + upper}, {"group:Opaque:AbC|1", "group:Opaque:AbC|1"},
	} {
		if got := CanonicalPrincipal(test.principal); got != test.want {
			t.Errorf("canonical principal %q: got %q, want %q", test.principal, got, test.want)
		}
	}
}

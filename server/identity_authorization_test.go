package hex

import (
	"bytes"
	"context"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestPrincipalMatchingPreservesOpaqueClaims(t *testing.T) {
	identity := &Identity{ID: "Subject:AbC|123", Groups: []string{"Team:Sales|A"}, Roles: []string{"Hex.Admin"}}
	for _, principal := range []string{"user:Subject:AbC|123", "Subject:AbC|123", "group:Team:Sales|A", "Team:Sales|A", "role:Hex.Admin", "Hex.Admin"} {
		if !identity.matches(principal) {
			t.Errorf("exact principal %q did not match", principal)
		}
	}
	for _, principal := range []string{"user:subject:abc|123", "subject:abc|123", "group:team:Sales|A", "role:hex.admin", "group:Subject:AbC|123", "User:Subject:AbC|123", "user:"} {
		if identity.matches(principal) {
			t.Errorf("different principal %q matched", principal)
		}
	}
	server := &Server{config: Config{AdminGroups: []string{"role:hex.admin"}, PublisherGroups: []string{"group:team:Sales|A"}}}
	if server.isAdmin(identity) || server.canCreateSites(identity) {
		t.Fatal("case-colliding admin/publisher claims granted access")
	}
	if server.siteRole(identity, SiteAccess{Owners: []string{"user:subject:abc|123"}, Viewers: []string{"group:team:Sales|A"}}, true) != roleNone {
		t.Fatal("case-colliding policy claims granted access")
	}
}

func TestOpaquePolicyPrincipalBounds(t *testing.T) {
	for _, id := range []string{"oidc|AbC:123", "https://issuer.example/Subject?tenant=A", "!subject", strings.Repeat("x", 256)} {
		if err := validateSiteAccess(SiteAccess{Owners: []string{"user:" + id}}); err != nil {
			t.Errorf("valid opaque ID %q rejected: %v", id, err)
		}
	}
	for _, id := range []string{"", "has space", "line\nfeed", "tab\t", "nul\x00", "delete\x7f", strings.Repeat("x", 257)} {
		if err := validateSiteAccess(SiteAccess{Owners: []string{"user:" + id}}); err == nil {
			t.Errorf("invalid opaque ID %q accepted", id)
		}
	}
}

func TestPathComparisonFollowsHostStorage(t *testing.T) {
	identity := &Identity{ID: "viewer"}
	access := SiteAccess{Paths: []PathRule{
		{Prefix: "/Admin/", Viewers: Audience{Level: LevelOwners}},
		{Prefix: "/Admin/help/", Viewers: Audience{Level: LevelViewers}},
	}}
	for _, insensitive := range []bool{false, true} {
		server := &Server{config: Config{PathCaseInsensitive: insensitive}}
		for _, test := range []struct {
			path string
			want bool
		}{
			{"/Admin/private.html", false}, {"/Admin", false},
			{"/admin/private.html", !insensitive}, {"/ADMIN", !insensitive},
			{"/Admin/help/faq.html", true}, {"/Administration.html", true},
		} {
			if got := server.pathAllowed(roleViewer, identity, access, test.path); got != test.want {
				t.Errorf("caseInsensitive=%v, path=%q: got %v, want %v", insensitive, test.path, got, test.want)
			}
		}
		if !server.pathAllowed(roleOwner, identity, access, "/Admin/private.html") {
			t.Fatal("owner did not pass path rule")
		}
	}
}

func TestPathPrefixAmbiguityIsHostSpecific(t *testing.T) {
	access := SiteAccess{Paths: []PathRule{
		{Prefix: "/Admin/", Viewers: Audience{Level: LevelOwners}},
		{Prefix: "/admin/", Viewers: Audience{Level: LevelViewers}},
	}}
	if err := validateSiteAccess(access); err != nil {
		t.Fatalf("portable validation assumed a host: %v", err)
	}
	exact := &Server{}
	if err := exact.validateSiteAccess(access); err != nil {
		t.Fatalf("case-distinct paths rejected on exact storage: %v", err)
	}
	folded := &Server{config: Config{PathCaseInsensitive: true}}
	if err := folded.validateSiteAccess(access); err == nil {
		t.Fatal("casefold collision accepted")
	}
	access.Paths[1].Prefix = "/Admin/"
	if err := exact.validateSiteAccess(access); err == nil {
		t.Fatal("duplicate exact prefix accepted")
	}
}

type labelPeopleStore struct {
	PeopleStore
	people []Person
}

func (store labelPeopleStore) GetPeople(context.Context, []string) ([]Person, error) {
	return store.people, nil
}

func TestDirectoryLabelsUseAuthorizationIdentitySemantics(t *testing.T) {
	server := &Server{config: Config{
		People: labelPeopleStore{people: []Person{
			{ID: "Subject:AbC|1", Name: "Upper"}, {ID: "subject:abc|1", Name: "Lower"},
		}},
		Groups: []NamedGroup{{ID: "Team:A", Name: "Upper team"}, {ID: "team:a", Name: "Lower team"}},
	}}
	principals := []string{"user:Subject:AbC|1", "user:subject:abc|1", "user:SUBJECT:ABC|1", "group:Team:A", "group:team:a", "group:TEAM:A", "Subject:AbC|1"}
	want := []string{"Upper", "Lower", "SUBJECT:ABC|1", "Upper team", "Lower team", "TEAM:A", "Subject:AbC|1"}
	for i, label := range server.describePrincipals(context.Background(), principals) {
		if label.Name != want[i] {
			t.Errorf("%q labeled %q, want %q", label.Principal, label.Name, want[i])
		}
	}
}

func TestInvalidResolvedIdentityFailsClosed(t *testing.T) {
	for _, id := range []string{"", "Subject\nInjected", strings.Repeat("x", 257)} {
		server := &Server{config: Config{Identity: StaticIdentity{Identity: Identity{ID: id}}}}
		request := httptest.NewRequest("GET", "/api/hex/me", nil)
		if server.requestIdentity(request) != nil {
			t.Fatalf("invalid identity %q was admitted", id)
		}
		response := httptest.NewRecorder()
		server.me(response, request)
		if response.Code != 401 {
			t.Fatalf("invalid identity %q: status %d", id, response.Code)
		}
	}
}

func TestAccountMenuUsesHostCapabilities(t *testing.T) {
	for _, test := range []struct {
		name   string
		manage bool
		logout string
	}{
		{name: "identity only"},
		{name: "managed", manage: true},
		{name: "custom logout", logout: "/gateway/signout?next=/"},
	} {
		t.Run(test.name, func(t *testing.T) {
			view := map[string]any{
				"Platform": "Hex", "Active": "home", "AdminLink": "", "Connections": false,
				"Manage": test.manage, "LogoutURL": test.logout,
				"Viewer": &viewerView{Name: "Alex", FirstName: "Alex", Initials: "A"},
			}
			var output bytes.Buffer
			if err := portalTemplates.ExecuteTemplate(&output, "header", view); err != nil {
				t.Fatal(err)
			}
			html := output.String()
			if got := strings.Contains(html, `href="/manage"`); got != test.manage {
				t.Fatalf("management link present=%v, want %v", got, test.manage)
			}
			if got := strings.Contains(html, "Sign out"); got != (test.logout != "") {
				t.Fatalf("logout link present=%v for URL %q", got, test.logout)
			}
			if test.logout != "" && !strings.Contains(html, `href="`+test.logout+`"`) {
				t.Fatal("configured host logout URL was not rendered")
			}
		})
	}
}

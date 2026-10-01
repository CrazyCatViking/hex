package hex

import (
	"slices"
	"testing"
)

func TestSharingRoundTrips(t *testing.T) {
	owner := "user:owner"
	for _, test := range []struct {
		name       string
		general    string
		principals []string
		roles      []string
		want       SiteAccess
	}{
		{
			name:    "only owners",
			general: GeneralRestricted, principals: []string{owner}, roles: []string{RoleOwner},
			want: SiteAccess{Owners: []string{owner}, Editors: []string{owner}, Viewers: []string{owner}},
		},
		{
			name:       "restricted viewers cannot edit",
			general:    GeneralRestricted,
			principals: []string{owner, "group:sales", "user:ann"},
			roles:      []string{RoleOwner, RoleViewer, RoleEditor},
			want:       SiteAccess{Owners: []string{owner}, Editors: []string{"user:ann"}, Viewers: []string{"group:sales"}},
		},
		{
			name:       "restricted viewers only",
			general:    GeneralRestricted,
			principals: []string{owner, "group:sales"},
			roles:      []string{RoleOwner, RoleViewer},
			want:       SiteAccess{Owners: []string{owner}, Editors: []string{owner}, Viewers: []string{"group:sales"}},
		},
		{
			name:       "everyone views, some edit",
			general:    GeneralView,
			principals: []string{owner, "user:ann"},
			roles:      []string{RoleOwner, RoleEditor},
			want:       SiteAccess{Owners: []string{owner}, Editors: []string{"user:ann"}, Viewers: []string{}},
		},
		{
			name:    "everyone views, owners edit",
			general: GeneralView, principals: []string{owner}, roles: []string{RoleOwner},
			want: SiteAccess{Owners: []string{owner}, Editors: []string{owner}, Viewers: []string{}},
		},
		{
			name:    "everyone edits",
			general: GeneralEdit, principals: []string{owner}, roles: []string{RoleOwner},
			want: SiteAccess{Owners: []string{owner}, Editors: []string{}, Viewers: []string{}},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			access, err := encodeSharing(test.general, test.principals, test.roles, []string{owner})
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(access.Owners, test.want.Owners) || !slices.Equal(access.Editors, test.want.Editors) || !slices.Equal(access.Viewers, test.want.Viewers) {
				t.Fatalf("encoded %+v, want %+v", access, test.want)
			}

			// Reading the policy back gives the same general access and roles.
			general, order, roles := decodeSharing(access, true)
			if general != test.general {
				t.Fatalf("general access %q, want %q", general, test.general)
			}
			for i, principal := range test.principals {
				if test.general == GeneralEdit && test.roles[i] != RoleOwner {
					continue
				}
				if roles[principal] != test.roles[i] {
					t.Fatalf("%s decoded as %q, want %q (order %v)", principal, roles[principal], test.roles[i], order)
				}
			}

			// And the policy grants what the roles promise.
			server := &Server{}
			for i, principal := range test.principals {
				identity := identityFor(principal)
				role := server.siteRole(identity, access, true)
				wantRole := map[string]siteRole{RoleOwner: roleOwner, RoleEditor: roleEditor, RoleViewer: roleViewer}[test.roles[i]]
				if role != wantRole {
					t.Fatalf("%s has role %d, want %d", principal, role, wantRole)
				}
			}
			stranger := server.siteRole(&Identity{ID: "stranger"}, access, true)
			wantStranger := map[string]siteRole{GeneralRestricted: roleNone, GeneralView: roleViewer, GeneralEdit: roleEditor}[test.general]
			if stranger != wantStranger {
				t.Fatalf("someone not added has role %d, want %d", stranger, wantStranger)
			}
		})
	}
}

func identityFor(principal string) *Identity {
	switch kind, value := principal[:5], principal[5:]; kind {
	case "user:":
		return &Identity{ID: value}
	default:
		return &Identity{ID: "member-of-" + principal[6:], Groups: []string{principal[6:]}}
	}
}

func TestDecodingLegacyPolicies(t *testing.T) {
	// A restricted policy without editors lets every viewer edit.
	general, _, roles := decodeSharing(SiteAccess{Owners: []string{"user:o"}, Viewers: []string{"group:sales"}}, true)
	if general != GeneralRestricted || roles["group:sales"] != RoleEditor {
		t.Fatalf("legacy viewers should decode as editors: %s %v", general, roles)
	}
	// No policy: open to everyone, nobody listed.
	general, order, _ := decodeSharing(SiteAccess{}, false)
	if general != GeneralEdit || len(order) != 0 {
		t.Fatalf("missing policy: %s %v", general, order)
	}
}

func TestEncodingRejectsMistakes(t *testing.T) {
	if _, err := encodeSharing(GeneralRestricted, []string{"user:a"}, []string{"admin"}, []string{"user:a"}); err == nil {
		t.Fatal("unknown role accepted")
	}
	if _, err := encodeSharing("public", []string{"user:a"}, []string{RoleOwner}, []string{"user:a"}); err == nil {
		t.Fatal("unknown general access accepted")
	}
	if _, err := encodeSharing(GeneralRestricted, []string{"user:a"}, nil, []string{"user:a"}); err == nil {
		t.Fatal("principals without roles accepted")
	}
}

package hex

import (
	"slices"
	"testing"
)

func TestMergePrincipals(t *testing.T) {
	cases := []struct {
		name               string
		base, live, next   []string
		emptyMeansEveryone bool
		want               []string
		ok                 bool
	}{
		{"unchanged hex.json keeps live", []string{"a", "b"}, []string{"a"}, []string{"b", "a"}, true, []string{"a"}, true},
		{"unchanged live takes hex.json", []string{"a"}, []string{"a"}, []string{"a", "c"}, true, []string{"a", "c"}, true},
		{"both changed", []string{"a", "b"}, []string{"a", "d"}, []string{"b", "c"}, true, []string{"d", "c"}, true},
		{"same change on both sides", []string{"a"}, []string{}, []string{}, true, []string{}, true},
		{"opening while live changed", []string{"a"}, []string{"a", "b"}, []string{}, true, []string{"a", "b"}, false},
		{"restricting an opened list", []string{"a"}, []string{}, []string{"a", "c"}, true, []string{}, false},
		{"removing the last one", []string{"a", "b"}, []string{"a"}, []string{"b"}, true, []string{"a"}, false},
		{"owners added to an unmanaged base", []string{}, []string{"me"}, []string{"me", "team"}, false, []string{"me", "team"}, true},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			got, ok := mergePrincipals(test.base, test.live, test.next, test.emptyMeansEveryone)
			if ok != test.ok || !slices.Equal(got, test.want) && len(got)+len(test.want) > 0 {
				t.Fatalf("got %v, %v; want %v, %v", got, ok, test.want, test.ok)
			}
		})
	}
}

func TestMergeSiteAccessKeepsOwnersWithoutHexJSONOwners(t *testing.T) {
	base := SiteAccess{Viewers: []string{"group:sales"}}
	live := SiteAccess{Owners: []string{"user:me", "user:colleague"}, Viewers: []string{"group:sales"}}
	next := SiteAccess{Viewers: []string{"group:sales", "group:hr"}}

	merged, conflicts := mergeSiteAccess(base, live, next)
	if len(conflicts) != 0 || !slices.Equal(merged.Owners, live.Owners) || !slices.Equal(merged.Viewers, []string{"group:sales", "group:hr"}) {
		t.Fatalf("got %+v, %v", merged, conflicts)
	}
}

func TestMergeSiteAccessRules(t *testing.T) {
	owners := DataRule{Write: Audience{Level: LevelOwners}}
	editors := DataRule{Write: Audience{Level: LevelEditors}}
	base := SiteAccess{Collections: map[string]DataRule{"settings": owners, "notes": owners}}
	live := SiteAccess{Collections: map[string]DataRule{"settings": editors}}
	next := SiteAccess{Collections: map[string]DataRule{"settings": owners, "notes": owners, "tasks": editors}, Paths: []PathRule{{Prefix: "/admin/", Viewers: Audience{Level: LevelOwners}}}}

	merged, conflicts := mergeSiteAccess(base, live, next)
	if len(conflicts) != 0 {
		t.Fatalf("unexpected conflicts: %v", conflicts)
	}
	if _, kept := merged.Collections["notes"]; kept {
		t.Fatal("a rule removed outside hex.json came back")
	}
	if merged.Collections["settings"].Write.Level != LevelEditors || merged.Collections["tasks"].Write.Level != LevelEditors || len(merged.Paths) != 1 {
		t.Fatalf("rules were not merged: %+v", merged)
	}

	next.Collections["settings"] = DataRule{Write: Audience{Level: LevelViewers}}
	if _, conflicts := mergeSiteAccess(base, live, next); !slices.Equal(conflicts, []string{"collection settings"}) {
		t.Fatalf("got conflicts %v", conflicts)
	}
}

func TestAccessDriftComparesEffectiveRoles(t *testing.T) {
	published := SiteAccess{Viewers: []string{"group:sales"}}
	// The portal saves a restricted site without editors with its viewers as
	// editors and its owners as viewers, which grants the same access.
	saved := SiteAccess{Owners: []string{"user:me"}, Editors: []string{"group:sales"}, Viewers: []string{"user:me"}}
	if drift := accessDrift(published, saved); len(drift) != 0 {
		t.Fatalf("equivalent policies drift: %v", drift)
	}

	saved.Editors = []string{"group:hr"}
	drift := accessDrift(published, saved)
	want := []string{
		"group:sales: editor in hex.json, not listed on the site",
		"group:hr: not listed in hex.json, editor on the site",
	}
	if !slices.Equal(drift, want) {
		t.Fatalf("got %q", drift)
	}
}

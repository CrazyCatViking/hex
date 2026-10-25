package hex

import (
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"testing"
)

func TestAdmissionMigrationPreservesEntryAndPageAudiences(t *testing.T) {
	identities := []*Identity{
		nil, {ID: "owner"}, {ID: "unscoped"}, {ID: "scoped"},
		{ID: "directory-member", Groups: []string{"team"}},
		{ID: "direct"}, {ID: "outsider"}, {ID: "job", Provider: "automation"},
	}
	for _, defaultMembership := range []bool{false, true} {
		for _, pageResource := range []string{"*", "/", "/index.html"} {
			t.Run(fmt.Sprintf("default=%t/resource=%s", defaultMembership, pageResource), func(t *testing.T) {
				old := SiteAccess{
					Version: 2, Owners: []string{"user:owner"},
					Admission: Subjects{Owners: true, Roles: []string{"members"}, Principals: []string{"user:direct"}},
					Roles:     map[string]ApplicationRole{"members": {SignedIn: defaultMembership}},
					Bindings: map[string]RoleBinding{
						"unscoped": {Principal: "user:unscoped", Role: "members"},
						"scoped":   {Principal: "user:scoped", Role: "members", Scope: &RoleScope{Kind: "project", ID: "A"}},
						"team":     {Principal: "group:team", Role: "members"},
						"job":      {Principal: "automation:job", Role: "members"},
					},
					Grants: map[string]PermissionGrant{
						"ordinary":  {Kind: "path", Resource: pageResource, Except: []string{"/admin/"}, Operations: []string{"read"}, Subjects: Subjects{Roles: []string{"members"}}},
						"protected": {Kind: "path", Resource: "/admin/", Operations: []string{"read"}, Subjects: Subjects{Roles: []string{"members"}}},
					},
				}
				migrated := MigrateSiteAccess(old)
				if err := ValidatePublishedAccess(migrated); err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(old.Grants["protected"], migrated.Grants["protected"]) {
					t.Fatal("migration changed a protected page rule")
				}
				for _, identity := range identities {
					role := roleViewer
					if identity.matchesAny(old.Owners) {
						role = roleOwner
					}
					before := siteAuthorization{access: old, identity: identity, role: role}
					after := siteAuthorization{access: migrated, identity: identity, role: role}
					admittedBefore := before.subjectsMatch(old.Admission)
					for _, binding := range old.Bindings {
						if slices.Contains(old.Admission.Roles, binding.Role) && identity.matchesAny([]string{binding.Principal}) {
							admittedBefore = true
						}
					}
					// Named automations bypass human admission, but still need grants.
					if identity != nil && identity.Provider == "automation" {
						admittedBefore = true
					}
					if admittedBefore != after.applicationAdmitted() {
						t.Fatalf("entry changed for %+v", identity)
					}
					for _, path := range []string{"/", "/index.html", "/index.html/child", "/assets/app.js", "/admin", "/admin/report"} {
						allowedBefore := false
						for _, grant := range old.Grants {
							if !permissionSelectorMatches("path", grant.Resource, path) || slices.ContainsFunc(grant.Except, func(except string) bool { return permissionSelectorMatches("path", except, path) }) {
								continue
							}
							allowedBefore = allowedBefore || admittedBefore && before.subjectsMatch(grant.Subjects)
						}
						if allowedBefore != after.permits("path", path, "read") {
							t.Fatalf("page %s changed for %+v", path, identity)
						}
					}
				}
			})
		}
	}
}

func TestAdmissionMigrationSupportsMaximumExistingAudience(t *testing.T) {
	old := SiteAccess{
		Version: 2, Admission: Subjects{Roles: []string{"members"}},
		Roles: map[string]ApplicationRole{"members": {}}, Bindings: map[string]RoleBinding{},
		Grants: map[string]PermissionGrant{"pages": {Kind: "path", Resource: "*", Operations: []string{"read"}, Subjects: Subjects{Roles: []string{"members"}}}},
	}
	for i := range 1024 {
		old.Bindings[fmt.Sprintf("binding-%d", i)] = RoleBinding{Principal: fmt.Sprintf("user:bound-%d", i), Role: "members"}
	}
	page := old.Grants["pages"]
	for i := range 256 {
		principal := fmt.Sprintf("user:direct-%d", i)
		old.Admission.Principals = append(old.Admission.Principals, principal)
		page.Subjects.Principals = append(page.Subjects.Principals, principal)
	}
	old.Grants["pages"] = page
	migrated := MigrateSiteAccess(old)
	if len(migrated.Admission.Principals) != 1280 || len(migrated.Grants["pages"].Subjects.Principals) != 1280 {
		t.Fatal("migration dropped admission/page principals")
	}
	if err := ValidatePublishedAccess(migrated); err != nil {
		t.Fatal(err)
	}
}

func TestPermissionMigrationPreservesEffectiveAccess(t *testing.T) {
	server := New(Config{AdminGroups: []string{"group:admins"}})
	identities := []*Identity{nil, {ID: "owner"}, {ID: "alice", Groups: []string{"team"}}, {ID: "bob", Groups: []string{"editors"}}, {ID: "outsider"}, {ID: "admin", Groups: []string{"admins"}}}
	audiences := []Audience{{}, {Level: LevelViewers}, {Level: LevelEditors}, {Level: LevelOwners}, {Principals: []string{"user:alice"}}}
	for _, viewers := range [][]string{nil, {"group:team"}} {
		for _, editors := range [][]string{nil, {"group:editors"}} {
			for index, audience := range audiences {
				t.Run(fmt.Sprintf("viewers=%v/editors=%v/audience=%d", viewers, editors, index), func(t *testing.T) {
					legacy := SiteAccess{
						Owners: []string{"user:owner"}, Viewers: viewers, Editors: editors,
						Collections: map[string]DataRule{"*": {Read: audience, Write: audience}, "tasks": {Read: Audience{Level: LevelCreator}, Write: Audience{Level: LevelCreator}}, "locked": {Read: Audience{Level: LevelOwners}, Write: Audience{Level: LevelOwners}}},
						Files:       map[string]DataRule{"*": {Read: audience, Write: audience}, "docs/": {Read: Audience{Level: LevelOwners}, Write: Audience{Level: LevelOwners}}, "docs/public/": {Read: Audience{Level: LevelViewers}, Write: Audience{Level: LevelEditors}}},
						Channels:    map[string]DataRule{"*": {Read: audience, Write: audience}, "private": {Read: Audience{Level: LevelOwners}, Write: Audience{Level: LevelOwners}}},
						Paths:       []PathRule{{Prefix: "/admin/", Viewers: Audience{Level: LevelOwners}}, {Prefix: "/admin/help/", Viewers: audience}},
					}
					migrated := MigrateSiteAccess(legacy)
					if err := ValidatePublishedAccess(migrated); err != nil {
						t.Fatal(err)
					}
					for _, identity := range identities {
						oldRole := server.siteRole(identity, legacy, true)
						newRole := server.siteRole(identity, migrated, true)
						if oldRole != newRole {
							t.Fatalf("role changed for %+v: %v != %v", identity, oldRole, newRole)
						}
						old := siteAuthorization{identity: identity, access: legacy, role: oldRole}
						next := siteAuthorization{identity: identity, access: migrated, role: newRole}
						for _, collection := range []string{"tasks", "locked", "unlisted"} {
							for _, creator := range []string{"alice", "bob", "owner", ""} {
								document := Document{ID: "record", Data: json.RawMessage(`{}`), CreatedBy: creator}
								for _, operation := range []string{"read", "update", "delete"} {
									grant := old.collection(collection).write
									if operation == "read" {
										grant = old.collection(collection).read
									}
									allowed := grant == grantAll || grant == grantOwn && creator == old.creator()
									actual := next.canMutate(collection, operation, &document, document)
									if operation == "read" {
										actual = next.canReadDocument(collection, document)
									}
									if allowed != actual {
										t.Fatalf("%s %s creator=%s identity=%+v changed: %t != %t", collection, operation, creator, identity, allowed, actual)
									}
								}
							}
						}
						for _, key := range []string{"other", "docs", "docs/private/x", "docs/public/x"} {
							before, after := old.file(key), next.file(key)
							if before != after || (before.write == grantAll) != next.permits("file", key, "delete") {
								t.Fatalf("file %s changed for %+v: %+v != %+v", key, identity, before, after)
							}
						}
						for _, channel := range []string{"other", "private"} {
							if old.channel(channel) != next.channel(channel) {
								t.Fatalf("channel %s changed for %+v", channel, identity)
							}
						}
						for _, path := range []string{"/", "/admin", "/admin/private", "/admin/help", "/admin/help/article"} {
							before := oldRole != roleNone && server.pathAllowed(oldRole, identity, legacy, path)
							after := newRole != roleNone && server.pathAllowed(newRole, identity, migrated, path)
							if before != after {
								t.Fatalf("path %s changed for %+v", path, identity)
							}
						}
					}
				})
			}
		}
	}
}

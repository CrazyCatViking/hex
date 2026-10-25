package hex

import (
	"encoding/json"
	"fmt"
	"testing"
)

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

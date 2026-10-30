package hex

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"slices"
	"strings"
)

type workspaceShareRow struct {
	Choices                                                    []choice
	Name, Description, Kind, Mode, GrantID, ConsumerID, Detail string
	Version                                                    int
	// Modes lists the workspace-shared connection modes of an action.
	Modes      []string
	definition *ActionDefinition
	// versions holds every registered version of an event, oldest first.
	versions []EventType
}

func workspaceGrantID(kind, resource string) string {
	return "workspace-" + internalKey(kind, resource)[:32]
}

func (s *Server) workspaceShareRows(ctx context.Context, site string, access SiteAccess) ([]workspaceShareRow, error) {
	rows := []workspaceShareRow{}
	if s.actionsEnabled() {
		actions, err := s.siteActions(ctx, site)
		if err != nil {
			return nil, err
		}
		for _, action := range actions {
			if !action.definition.Exported {
				continue
			}
			mode, modes := "restricted", workspaceConnectionModes(access, action.definition.Name)
			if len(modes) > 0 {
				mode = "workspace"
			}
			rows = append(rows, workspaceShareRow{Name: action.definition.Name, Description: action.definition.Description, Kind: "action", Version: action.definition.Version, Mode: mode, Modes: modes, definition: &action.definition, GrantID: workspaceGrantID("action", action.definition.Name), ConsumerID: workspaceGrantID("action-consumer", action.definition.Name), Detail: "People, AI assistants and automations can call this action through another site. Its data and record rules still apply."})
		}
	}
	if s.eventsEnabled() {
		value, _ := json.Marshal(site)
		after := ""
		for {
			documents, err := s.config.Database.(GuardedDatabase).ListMatching(ctx, systemSite, "event-types", ListOptions{Limit: 100, After: after}, &RecordCondition{Field: "data.site", Op: "eq", Value: value})
			if err != nil {
				return nil, err
			}
			for _, document := range documents {
				var topic eventTopic
				if err := json.Unmarshal(document.Data, &topic); err != nil {
					return nil, err
				}
				mode := "restricted"
				if workspaceShared(access, "event", topic.Name, "subscribe") {
					mode = "workspace"
				}
				// Sharing is by name and applies to every declared version.
				if index := slices.IndexFunc(rows, func(row workspaceShareRow) bool { return row.Kind == "event" && row.Name == topic.Name }); index >= 0 {
					rows[index].versions = append(rows[index].versions, topic.EventType)
					continue
				}
				rows = append(rows, workspaceShareRow{Name: topic.Name, Description: topic.Description, Kind: "event", Mode: mode, versions: []EventType{topic.EventType}, GrantID: workspaceGrantID("event", topic.Name), Detail: "Other sites can subscribe and trigger their automations or workflows when this event happens. Publishing events remains separately controlled."})
			}
			if len(documents) < 100 {
				break
			}
			after = documents[len(documents)-1].ID
		}
	}
	slices.SortFunc(rows, func(a, b workspaceShareRow) int { return strings.Compare(a.Kind+"/"+a.Name, b.Kind+"/"+b.Name) })
	for i := range rows {
		slices.SortFunc(rows[i].versions, func(a, b EventType) int { return a.Version - b.Version })
		rows[i].Choices = choices(rows[i].Mode, [2]string{"restricted", "Restricted · existing permissions only"}, [2]string{"workspace", "Everyone in this workspace · all sites"})
	}
	return rows, nil
}

// Probe with no claims: only a workspace audience can satisfy this check.
// The portal's simple selector and the per-app editor share actions with
// different connection modes; any of them opens the action to the workspace.
func workspaceConnectionModes(access SiteAccess, action string) []string {
	var modes []string
	for _, mode := range []string{"user", "site", "run"} {
		if workspaceShared(access, "action-consumer", action, mode) {
			modes = append(modes, mode)
		}
	}
	return modes
}

func workspaceShared(access SiteAccess, kind, resource, operation string) bool {
	authorization := siteAuthorization{access: access, identity: &Identity{Provider: "site"}, role: roleViewer, apiOnly: true}
	return authorization.operationGrant(kind, resource, operation) == grantAll
}

func editWorkspaceSharing(access SiteAccess, form url.Values) (SiteAccess, error) {
	kind, resource, mode := form.Get("kind"), form.Get("resource"), form.Get("mode")
	if (kind != "action" && kind != "event") || !validCollectionName(resource) || (mode != "workspace" && mode != "restricted") {
		return access, errors.New("choose an action or event and a valid sharing setting")
	}
	if access.Grants == nil {
		access.Grants = map[string]PermissionGrant{}
	}
	kinds := []string{kind}
	operation := "subscribe"
	if kind == "action" {
		kinds = append(kinds, "action-consumer")
		operation = "run"
	}
	for _, grantKind := range kinds {
		id := workspaceGrantID(grantKind, resource)
		if mode == "restricted" {
			delete(access.Grants, id)
			continue
		}
		access.Grants[id] = PermissionGrant{Kind: grantKind, Resource: resource, Operations: []string{operation}, Subjects: Subjects{Workspace: true}}
	}
	if mode == "restricted" {
		for _, grantKind := range kinds {
			if workspaceShared(access, grantKind, resource, operation) {
				return access, errors.New("a custom policy also shares this resource with the workspace; update that rule in Developer tools before restricting access")
			}
		}
	}
	return access, nil
}

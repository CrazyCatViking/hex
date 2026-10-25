package hex

import "context"

func (s *Server) migrationExtras(ctx context.Context, site string) ([]*registeredAction, []ScheduledAutomation, error) {
	actions, err := s.siteActions(ctx, site)
	if err != nil {
		return nil, nil, err
	}
	var automations []ScheduledAutomation
	if s.config.Automations != nil {
		automations, err = s.config.Automations.ListSiteAutomations(ctx, site)
		if err != nil {
			return nil, nil, err
		}
	}
	return actions, automations, nil
}

func migratePermissionExtras(access SiteAccess, actions []*registeredAction, automations []ScheduledAutomation) SiteAccess {
	for _, action := range actions {
		if action.audience.IsZero() {
			continue
		}
		fallback := access.Grants[stablePermissionID("actions")]
		fallback.Except = appendUnique(fallback.Except, action.definition.Name)
		access.Grants[stablePermissionID("actions")] = fallback
		var subjects Subjects
		switch action.audience.Level {
		case LevelViewers:
			subjects = access.Admission
		case LevelOwners:
			subjects.Owners = true
		case LevelEditors:
			subjects.Roles = []string{"legacy-editor"}
		default:
			subjects.Principals = typedLegacyPrincipals(action.audience.Principals)
		}
		access.Grants[stablePermissionID("action", action.definition.Name)] = PermissionGrant{Kind: "action", Resource: action.definition.Name, Operations: []string{"run"}, Subjects: subjects}
	}
	for _, scheduled := range automations {
		principal := "automation:" + scheduled.Automation.Name
		for _, kind := range []string{"collection", "file", "channel", "action", "integration"} {
			grant := access.Grants[stablePermissionID("owners", kind)]
			grant.Subjects = Subjects{Principals: []string{principal}}
			access.Grants[stablePermissionID("automation", scheduled.Automation.Name, kind)] = grant
		}
	}
	if access.Published != nil {
		base := migratePermissionExtras(*access.Published, actions, nil)
		access.Published = &base
	}
	return access
}

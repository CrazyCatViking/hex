package hex

import "slices"

func permissionMembers(values []string) []string {
	if values == nil {
		return nil
	}
	result := slices.Clone(values)
	slices.Sort(result)
	return slices.Compact(result)
}

func normalizeSubjects(subjects Subjects) Subjects {
	subjects.Principals = permissionMembers(subjects.Principals)
	subjects.Roles = permissionMembers(subjects.Roles)
	return subjects
}

func normalizePermissionPolicy(access SiteAccess) SiteAccess {
	access = CloneSiteAccess(access)
	access.Owners = permissionMembers(access.Owners)
	access.Admission = normalizeSubjects(access.Admission)
	for id, grant := range access.Grants {
		grant.Subjects = normalizeSubjects(grant.Subjects)
		grant.Operations = permissionMembers(grant.Operations)
		grant.Except = permissionMembers(grant.Except)
		grant.Fields = permissionMembers(grant.Fields)
		access.Grants[id] = grant
	}
	if access.Published != nil {
		baseline := normalizePermissionPolicy(*access.Published)
		access.Published = &baseline
	}
	return access
}

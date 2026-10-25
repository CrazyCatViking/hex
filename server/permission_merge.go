package hex

import (
	"fmt"
	"reflect"
	"slices"
)

func mergePermissionMap[T any](kind string, base, live, local map[string]T) (map[string]T, []string) {
	merged := map[string]T{}
	var conflicts []string
	for _, id := range sortedKeys(base, live, local) {
		value, ok := mergeEntry(lookup(base, id), lookup(live, id), lookup(local, id), func(a, b T) bool { return reflect.DeepEqual(a, b) })
		if !ok {
			conflicts = append(conflicts, kind+" "+id)
			continue
		}
		if value.present {
			merged[id] = value.value
		}
	}
	return merged, conflicts
}

func roleBindingsChanged(role string, base, next SiteAccess) bool {
	for _, id := range sortedKeys(base.Bindings, next.Bindings) {
		before, after := base.Bindings[id], next.Bindings[id]
		if (before.Role == role || after.Role == role) && !reflect.DeepEqual(before, after) {
			return true
		}
	}
	return false
}

func mergeExplicitMembers(base, live, local []string) []string {
	merged := slices.Clone(live)
	for _, principal := range base {
		if !slices.Contains(local, principal) {
			merged = slices.DeleteFunc(merged, func(value string) bool { return value == principal })
		}
	}
	for _, principal := range local {
		if !slices.Contains(base, principal) {
			merged = appendUnique(merged, principal)
		}
	}
	return merged
}

func mergePermissionPolicy(base, live, local SiteAccess) (SiteAccess, []string) {
	base = normalizePermissionPolicy(base)
	live = normalizePermissionPolicy(live)
	local = normalizePermissionPolicy(local)
	merged := SiteAccess{Version: 2}
	if len(local.Owners) == 0 {
		local.Owners = base.Owners
	}
	merged.Owners = mergeExplicitMembers(base.Owners, live.Owners, local.Owners)
	merged.Admission.Principals = mergeExplicitMembers(base.Admission.Principals, live.Admission.Principals, local.Admission.Principals)
	mergeBool := func(base, live, local bool) bool {
		if local == base {
			return live
		}
		return local
	}
	merged.Admission.SignedIn = mergeBool(base.Admission.SignedIn, live.Admission.SignedIn, local.Admission.SignedIn)
	merged.Admission.Owners = mergeBool(base.Admission.Owners, live.Admission.Owners, local.Admission.Owners)
	var conflicts, found []string
	merged.Roles, found = mergePermissionMap("role", base.Roles, live.Roles, local.Roles)
	conflicts = append(conflicts, found...)
	merged.Bindings, found = mergePermissionMap("binding", base.Bindings, live.Bindings, local.Bindings)
	conflicts = append(conflicts, found...)
	merged.Grants, found = mergePermissionMap("grant", base.Grants, live.Grants, local.Grants)
	conflicts = append(conflicts, found...)
	// Opening admission on one side while the other restricts its subjects is
	// security-sensitive, even though the changes occupy different fields.
	if local.Admission.SignedIn != base.Admission.SignedIn && !reflect.DeepEqual(live.Admission, base.Admission) && !reflect.DeepEqual(live.Admission, local.Admission) || live.Admission.SignedIn != base.Admission.SignedIn && !reflect.DeepEqual(local.Admission, base.Admission) && !reflect.DeepEqual(live.Admission, local.Admission) {
		conflicts = append(conflicts, "admission")
	}
	for _, role := range sortedKeys(base.Roles, live.Roles, local.Roles) {
		localOpennessChanged := base.Roles[role].SignedIn != local.Roles[role].SignedIn
		liveOpennessChanged := base.Roles[role].SignedIn != live.Roles[role].SignedIn
		if localOpennessChanged && roleBindingsChanged(role, base, live) || liveOpennessChanged && roleBindingsChanged(role, base, local) {
			conflicts = append(conflicts, "role audience "+role)
		}
	}
	return merged, conflicts
}

func permissionDrift(local, live SiteAccess) []string {
	local = normalizePermissionPolicy(local)
	live = normalizePermissionPolicy(live)
	var drift []string
	if len(local.Owners) == 0 {
		local.Owners = live.Owners
	}
	if !sameMembers(local.Owners, live.Owners) {
		drift = append(drift, "site owners differ; portal changes preserved")
	}
	if !reflect.DeepEqual(local.Admission, live.Admission) {
		drift = append(drift, "site admission differs; portal changes preserved")
	}
	compare := func(kind string, a, b any) {
		if !reflect.DeepEqual(a, b) {
			drift = append(drift, fmt.Sprintf("%s differ; portal additions, changes and revocations preserved", kind))
		}
	}
	compare("application roles", local.Roles, live.Roles)
	compare("role assignments", local.Bindings, live.Bindings)
	for _, id := range sortedKeys(local.Bindings, live.Bindings) {
		left, inLocal := local.Bindings[id]
		right, inLive := live.Bindings[id]
		if inLocal == inLive && reflect.DeepEqual(left, right) {
			continue
		}
		principal := left.Principal
		if !inLocal {
			principal = right.Principal
		}
		drift = append(drift, fmt.Sprintf("assignment %s (%s) differs; portal changes preserved", id, principal))
	}
	compare("permission grants", local.Grants, live.Grants)
	return drift
}

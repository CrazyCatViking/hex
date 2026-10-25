package hex

import (
	"errors"
	"net/url"
	"reflect"
	"slices"
)

type portalPermissionEdit struct {
	Action string     `json:"action"`
	Values url.Values `json:"values"`
}

// The browser sends only explicit edits, not a reconstructed policy. In
// particular, untouched JSON numbers, custom grants and stable IDs stay intact.
func editPermissionMatrix(base SiteAccess, entry string) (SiteAccess, error) {
	var edits []portalPermissionEdit
	if err := decodeEditorValue(entry, &edits); err != nil {
		return base, err
	}
	if len(edits) > 4096 {
		return base, errors.New("too many pending permission edits; save smaller batches")
	}
	local := CloneSiteAccess(base)
	for _, edit := range edits {
		switch edit.Action {
		case "role", "grant", "remove-grant", "membership", "owner-membership", "owners", "admission", "remove-principal", "delete-group":
		default:
			return base, errors.New("unsupported permission-table edit; reload and try again")
		}
		values := edit.Values
		if values == nil {
			values = url.Values{}
		}
		values.Set("editor_action", edit.Action)
		var err error
		local, err = applyPermissionEditorChange(local, values)
		if err != nil {
			return base, err
		}
	}
	return local, ValidatePublishedAccess(local)
}

func editPermissionMembership(local SiteAccess, form url.Values) (SiteAccess, error) {
	principal, role := form.Get("principal"), form.Get("group")
	if !principalPattern.MatchString(principal) {
		return local, errors.New("choose a valid person or directory group")
	}
	if _, exists := local.Roles[role]; !exists {
		return local, errors.New("that permission group no longer exists; reload the page")
	}
	var scopes []*RoleScope
	if form.Get("assigned") == "true" {
		if form.Get("scopes") != "" {
			if err := decodeEditorValue(form.Get("scopes"), &scopes); err != nil {
				return local, err
			}
			if len(scopes) == 0 {
				return local, errors.New("choose at least one membership limit, or remove the assignment")
			}
		}
		if len(scopes) == 0 {
			scopes = []*RoleScope{nil}
		}
		if len(scopes) > 256 {
			return local, errors.New("limit a membership to at most 256 scopes")
		}
	}
	if local.Bindings == nil {
		local.Bindings = map[string]RoleBinding{}
	}
	for id, binding := range local.Bindings {
		if binding.Principal != principal || binding.Role != role {
			continue
		}
		if !slices.ContainsFunc(scopes, func(scope *RoleScope) bool { return reflect.DeepEqual(binding.Scope, scope) }) {
			delete(local.Bindings, id)
		}
	}
	for _, scope := range scopes {
		binding := RoleBinding{Principal: principal, Role: role, Scope: scope}
		found := false
		for _, existing := range local.Bindings {
			if reflect.DeepEqual(existing, binding) {
				found = true
				break
			}
		}
		if found {
			continue
		}
		parts := []string{"portal", "membership", principal, role}
		if scope != nil {
			parts = append(parts, scope.Kind, scope.ID)
		}
		id := stablePermissionID(parts...)
		if _, exists := local.Bindings[id]; exists {
			return local, errors.New("a stable membership ID was customized; reload and edit that membership separately")
		}
		local.Bindings[id] = binding
	}
	return local, nil
}

func editPermissionAdmission(local SiteAccess, form url.Values) (SiteAccess, error) {
	mode, principals := form.Get("general"), form["principal"]
	if mode == sharingMode(local) && sameMembers(principals, local.Admission.Principals) {
		return local, nil
	}
	if mode == sharingMode(local) {
		local.Admission.Principals = slices.Clone(principals)
	} else {
		switch mode {
		case "owners":
			local.Admission = Subjects{Owners: true}
		case "selected":
			local.Admission = Subjects{Owners: true, Principals: slices.Clone(principals)}
		case "everyone":
			local.Admission = Subjects{Owners: true, SignedIn: true, Principals: slices.Clone(principals)}
		default:
			return local, errors.New("choose a supported general access setting")
		}
	}
	if mode == "owners" {
		local.Admission.Principals = nil
	}
	subjects := Subjects{Owners: true, Principals: slices.Clone(local.Admission.Principals), SignedIn: mode == "everyone"}
	if err := ensureSharingPages(&local, stablePermissionID("portal", "general-pages"), subjects); err != nil {
		return local, err
	}
	return local, nil
}

func deletePermissionGroup(local SiteAccess, role string) (SiteAccess, error) {
	if _, exists := local.Roles[role]; !exists {
		return local, errors.New("that permission group no longer exists")
	}
	local.Admission.Roles = slices.DeleteFunc(local.Admission.Roles, func(value string) bool { return value == role })
	for id, binding := range local.Bindings {
		if binding.Role == role {
			delete(local.Bindings, id)
		}
	}
	for id, grant := range local.Grants {
		if !slices.Contains(grant.Subjects.Roles, role) {
			continue
		}
		grant.Subjects.Roles = slices.DeleteFunc(grant.Subjects.Roles, func(value string) bool { return value == role })
		if !grant.Subjects.SignedIn && !grant.Subjects.Owners && len(grant.Subjects.Roles) == 0 && len(grant.Subjects.Principals) == 0 {
			delete(local.Grants, id)
		} else {
			local.Grants[id] = grant
		}
	}
	delete(local.Roles, role)
	return local, nil
}

func matrixAssignsOwners(form url.Values) bool {
	if form.Get("editor_action") != "matrix" {
		return false
	}
	var edits []portalPermissionEdit
	if decodeEditorValue(form.Get("entry"), &edits) != nil {
		return false
	}
	return slices.ContainsFunc(edits, func(edit portalPermissionEdit) bool {
		return edit.Action == "owner-membership" || edit.Action == "role" && edit.Values.Get("assign_owners") == "true"
	})
}

// A table cell edits the whole principal/role membership, which can span
// several stable binding IDs. Detect concurrent changes across that whole
// cell so an old checkbox cannot leave a new membership silently in place.
func permissionMatrixMembershipConflict(base, current SiteAccess, form url.Values) bool {
	if form.Get("editor_action") != "matrix" {
		return false
	}
	var edits []portalPermissionEdit
	if decodeEditorValue(form.Get("entry"), &edits) != nil {
		return false
	}
	changed := func(principal, role string) bool {
		matching := func(policy SiteAccess) map[string]RoleBinding {
			result := map[string]RoleBinding{}
			for id, binding := range policy.Bindings {
				if binding.Principal == principal && (role == "" || binding.Role == role) {
					result[id] = binding
				}
			}
			return result
		}
		return !reflect.DeepEqual(matching(base), matching(current))
	}
	for _, edit := range edits {
		switch edit.Action {
		case "membership":
			if changed(edit.Values.Get("principal"), edit.Values.Get("group")) {
				return true
			}
		case "remove-principal":
			if changed(edit.Values.Get("principal"), "") {
				return true
			}
		case "owner-membership":
			for _, principal := range base.Owners {
				if changed(principal, edit.Values.Get("group")) {
					return true
				}
			}
		}
	}
	return false
}

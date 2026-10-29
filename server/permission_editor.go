package hex

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"reflect"
	"slices"
	"strings"
)

// GUI edits are narrow changes to the form's original policy, never a
// reconstruction of the full policy from a simplified sharing model.
func permissionEditorChange(base SiteAccess, form url.Values) (SiteAccess, error) {
	local, err := applyPermissionEditorChange(base, form)
	if err != nil {
		return local, err
	}
	return local, ValidatePublishedAccess(local)
}

func applyPermissionEditorChange(base SiteAccess, form url.Values) (SiteAccess, error) {
	local := CloneSiteAccess(base)
	switch form.Get("editor_action") {
	case "workspace-sharing":
		return editWorkspaceSharing(local, form)
	case "matrix":
		return editPermissionMatrix(base, form.Get("entry"))
	case "membership":
		return editPermissionMembership(local, form)
	case "owner-membership":
		for _, principal := range local.Owners {
			values := url.Values{"principal": {principal}, "group": {form.Get("group")}, "assigned": {form.Get("assigned")}}
			var err error
			local, err = editPermissionMembership(local, values)
			if err != nil {
				return local, err
			}
		}
	case "owners":
		if err := decodeEditorValue(form.Get("entry"), &local.Owners); err != nil {
			return local, err
		}
		if len(local.Owners) == 0 {
			return local, errors.New("keep at least one site owner")
		}
	case "admission":
		return editPermissionAdmission(local, form)
	case "remove-principal":
		for id, binding := range local.Bindings {
			if binding.Principal == form.Get("principal") {
				delete(local.Bindings, id)
			}
		}
	case "delete-group":
		return deletePermissionGroup(local, form.Get("id"))
	case "sharing":
		return editSiteSharing(local, form)
	case "role":
		var role ApplicationRole
		if err := decodeEditorValue(form.Get("entry"), &role); err != nil {
			return local, err
		}
		id := strings.TrimSpace(form.Get("id"))
		if !validCollectionName(id) {
			return local, errors.New("give the permission group a name using letters, numbers, spaces converted to dashes, or underscores")
		}
		if local.Roles == nil {
			local.Roles = map[string]ApplicationRole{}
		}
		if form.Get("creating") == "true" {
			if _, exists := local.Roles[id]; exists {
				return local, errors.New("a permission group with that name already exists; choose a different name")
			}
		} else if _, exists := local.Roles[id]; !exists {
			return local, errors.New("that permission group no longer exists; reload the page")
		}
		local.Roles[id] = role
		if form.Get("creating") == "true" && form.Get("assign_owners") == "true" {
			for _, principal := range local.Owners {
				var err error
				local, err = editPermissionMembership(local, url.Values{"principal": {principal}, "group": {id}, "assigned": {"true"}})
				if err != nil {
					return local, err
				}
			}
		}
		if form.Get("admit_role") == "true" {
			return local, errors.New("permission groups cannot grant site entry; add people or directory groups under Site access")
		}
	case "binding":
		var binding RoleBinding
		if err := decodeEditorValue(form.Get("entry"), &binding); err != nil {
			return local, err
		}
		id, err := permissionEditorID(form.Get("id"), local.Bindings, form.Get("creating") == "true")
		if err != nil {
			return local, err
		}
		if local.Bindings == nil {
			local.Bindings = map[string]RoleBinding{}
		}
		local.Bindings[id] = binding
	case "grant":
		var grant PermissionGrant
		if err := decodeEditorValue(form.Get("entry"), &grant); err != nil {
			return local, err
		}
		id, err := permissionEditorID(form.Get("id"), local.Grants, form.Get("creating") == "true")
		if err != nil {
			return local, err
		}
		originalID := id
		if source := form.Get("copy_from"); source != "" {
			if form.Get("creating") != "true" {
				return local, errors.New("only a new group-specific rule can copy an existing rule")
			}
			if _, exists := base.Grants[source]; !exists {
				return local, errors.New("the original shared rule no longer exists; reload and try again")
			}
			originalID = source
		}
		if original, exists := base.Grants[originalID]; exists {
			if form.Get("keep_where") == "true" {
				grant.Where = original.Where
			}
			if form.Get("keep_check") == "true" {
				grant.Check = original.Check
			}
		}
		if local.Grants == nil {
			local.Grants = map[string]PermissionGrant{}
		}
		local.Grants[id] = grant
	case "remove-role":
		id := form.Get("id")
		if _, exists := local.Roles[id]; !exists {
			return local, errors.New("that permission group no longer exists")
		}
		for _, binding := range local.Bindings {
			if binding.Role == id {
				return local, errors.New("remove this group's memberships before deleting the group")
			}
		}
		for _, grant := range local.Grants {
			if slices.Contains(grant.Subjects.Roles, id) {
				return local, errors.New("remove this group from its permission rules before deleting the group")
			}
		}
		delete(local.Roles, id)
	case "remove-binding":
		if _, exists := local.Bindings[form.Get("id")]; !exists {
			return local, errors.New("that membership no longer exists")
		}
		delete(local.Bindings, form.Get("id"))
	case "remove-grant":
		if _, exists := local.Grants[form.Get("id")]; !exists {
			return local, errors.New("that permission rule no longer exists")
		}
		delete(local.Grants, form.Get("id"))
	default:
		return local, errors.New("unknown permission edit; reload the page and try again")
	}
	return local, nil
}

func decodeEditorValue(value string, target any) error {
	if len(value) > 1<<20 {
		return errors.New("the permission edit is too large")
	}
	decoder := json.NewDecoder(strings.NewReader(value))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("check the permission settings: %w", err)
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		return errors.New("submit only one permission edit")
	}
	return nil
}

func permissionEditorID[T any](id string, entries map[string]T, creating bool) (string, error) {
	if id != "" {
		if creating {
			if !validCollectionName(id) {
				return "", errors.New("use a valid stable permission entry ID")
			}
			if _, exists := entries[id]; exists {
				return "", errors.New("that permission entry already exists; reload and try again")
			}
			return id, nil
		}
		if _, exists := entries[id]; !exists {
			return "", errors.New("that entry no longer exists; reload the page")
		}
		return id, nil
	}
	generated, err := newID()
	if err != nil {
		return "", err
	}
	return "portal-" + generated, nil
}

func sharingMode(access SiteAccess) string {
	switch {
	case access.Admission.SignedIn:
		return "everyone"
	case len(access.Admission.Principals) > 0:
		return "selected"
	case access.Admission.Owners:
		return "owners"
	default:
		return "custom"
	}
}

func sharingSummary(access SiteAccess, platform string) string {
	switch sharingMode(access) {
	case "everyone":
		return "Everyone signed in to " + platform + " may enter this site."
	case "owners":
		return "Only site owners and platform administrators may enter this site."
	case "selected":
		return "Site entry is limited to selected people and directory groups."
	default:
		return "Site entry has custom admission settings. Permission groups do not grant site entry."
	}
}

func editSiteSharing(local SiteAccess, form url.Values) (SiteAccess, error) {
	principals, roles := form["principal"], form["role"]
	if len(principals) != len(roles) || len(principals) > 512 {
		return local, errors.New("every person or group needs an access choice")
	}
	var owners, viewers []string
	for i, principal := range principals {
		principal = strings.TrimSpace(principal)
		if !principalPattern.MatchString(principal) {
			return local, errors.New("choose a valid person or group from the people picker")
		}
		switch roles[i] {
		case RoleOwner:
			owners = appendUnique(owners, principal)
		case RoleViewer:
			viewers = appendUnique(viewers, principal)
		default:
			return local, errors.New("choose Can open site or Manage site for each person")
		}
	}
	if len(owners) == 0 {
		return local, errors.New("keep at least one site manager before saving")
	}
	mode := form.Get("general")
	originalMode := sharingMode(local)
	if mode == "owners" && originalMode == "owners" && len(viewers) > 0 {
		return local, errors.New("choose Only selected people and groups to give the added people access")
	}
	originalViewers := slices.DeleteFunc(slices.Clone(local.Admission.Principals), func(value string) bool { return slices.Contains(local.Owners, value) })
	changed := mode != originalMode || !sameMembers(viewers, originalViewers) || !sameMembers(owners, local.Owners)
	local.Owners = owners
	if !changed {
		return local, ValidatePublishedAccess(local)
	}
	if mode == originalMode {
		// Preserve custom admission selectors while changing direct entries.
		local.Admission.Principals = viewers
	} else {
		switch mode {
		case "owners":
			local.Admission = Subjects{Owners: true}
		case "selected":
			local.Admission = Subjects{Owners: true, Principals: viewers}
		case "everyone":
			local.Admission = Subjects{SignedIn: true, Owners: true, Principals: viewers}
		case "custom":
			if originalMode != "custom" {
				return local, errors.New("choose a supported general access setting")
			}
			local.Admission.Principals = viewers
		default:
			return local, errors.New("choose who can open the site")
		}
	}
	// Admission gates every app API. The separate page grant makes ordinary
	// sharing useful without granting data authority or bypassing protected paths.
	subjects := Subjects{Owners: true, Principals: viewers}
	if mode == "everyone" {
		subjects.SignedIn = true
	}
	if mode == "owners" {
		subjects.Principals = nil
	}
	if err := ensureSharingPages(&local, stablePermissionID("portal", "general-pages"), subjects); err != nil {
		return local, err
	}
	return local, ValidatePublishedAccess(local)
}

func protectedPagePaths(access SiteAccess) []string {
	var except []string
	for _, grant := range access.Grants {
		if grant.Kind != "path" {
			continue
		}
		for _, resource := range grant.Except {
			except = appendUnique(except, resource)
		}
		if grant.Resource != "*" && grant.Resource != "/" && grant.Resource != "/index.html" {
			except = appendUnique(except, grant.Resource)
		}
	}
	slices.Sort(except)
	return except
}

func ensureSharingPages(local *SiteAccess, id string, subjects Subjects) error {
	except := protectedPagePaths(*local)
	if current, exists := local.Grants[id]; exists && (current.Kind != "path" || current.Resource != "*" || !sameMembers(current.Operations, []string{"read"})) {
		return errors.New("the general page rule was customized; edit that rule before changing general access")
	}
	if local.Grants == nil {
		local.Grants = map[string]PermissionGrant{}
	}
	local.Grants[id] = PermissionGrant{Kind: "path", Resource: "*", Except: except, Operations: []string{"read"}, Subjects: subjects}
	return nil
}

func portalPageRulesChanged(base, local SiteAccess) bool {
	ids := []string{stablePermissionID("portal", "general-pages")}
	for role := range local.Roles {
		ids = append(ids, stablePermissionID("portal", "group-pages", role))
	}
	for _, id := range ids {
		if !reflect.DeepEqual(base.Grants[id], local.Grants[id]) {
			return true
		}
	}
	return false
}

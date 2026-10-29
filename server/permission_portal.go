package hex

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"reflect"
	"slices"
	"strings"
)

func decodePermissionPolicy(data []byte) (SiteAccess, error) {
	var policy SiteAccess
	if len(data) > 1<<20 {
		return policy, errors.New("access policies are limited to 1 MiB")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&policy); err != nil {
		return policy, err
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		return policy, errors.New("expected exactly one permission policy")
	}
	policy.Published = nil
	return policy, ValidatePublishedAccess(policy)
}

func (s *Server) updatePortalPermissions(w http.ResponseWriter, r *http.Request, identity *Identity, site string, access SiteAccess, exists bool) {
	failed := func(err error) {
		trigger, encodeErr := json.Marshal(map[string]any{"permissionError": map[string]string{"message": err.Error()}})
		if encodeErr == nil {
			w.Header().Set("HX-Reswap", "none")
			w.Header().Set("HX-Trigger", string(trigger))
		} else {
			slog.Error("encode permission editor error", "error", encodeErr)
		}
		view := s.sharingView(r.Context(), site, access, exists)
		view.Error = err.Error()
		if r.PostForm.Has("policy") {
			view.Policy = r.PostForm.Get("policy")
		}
		s.renderFragment(w, "sharing", view)
	}
	base, err := decodePermissionPolicy([]byte(r.PostForm.Get("base")))
	if err != nil {
		failed(errors.New("the permission form's baseline is missing or invalid; reload and try again"))
		return
	}
	var local SiteAccess
	if r.PostForm.Has("editor_action") {
		local, err = permissionEditorChange(base, r.PostForm)
	} else {
		local, err = decodePermissionPolicy([]byte(r.PostForm.Get("policy")))
	}
	if err != nil {
		failed(err)
		return
	}
	var saved SiteAccess
	ctx, err := s.managementContext(r.Context(), identity, site)
	if err != nil {
		failed(err)
		return
	}
	err = s.config.Access.UpdateSiteAccess(ctx, site, func(current SiteAccess, exists bool) (*SiteAccess, error) {
		if err := s.mayChangeAccess(ctx, identity, site, current, exists); err != nil {
			return nil, err
		}
		if matrixAssignsOwners(r.PostForm) && !sameMembers(base.Owners, current.Owners) {
			return nil, errors.New("site owners changed while you were editing. Nothing was saved. Reload before assigning owner permissions")
		}
		if permissionMatrixMembershipConflict(base, current, r.PostForm) {
			return nil, errors.New("someone changed the same memberships while you were editing. Nothing was saved. Reload and review the latest assignments before saving")
		}
		if r.PostForm.Has("editor_action") && sharingMode(base) != sharingMode(local) && !reflect.DeepEqual(base.Admission, current.Admission) {
			return nil, errors.New("site-entry rules changed while you were editing. Nothing was saved. Reload before replacing general access")
		}
		if r.PostForm.Has("editor_action") && portalPageRulesChanged(base, local) && !sameMembers(protectedPagePaths(base), protectedPagePaths(current)) {
			return nil, errors.New("page restrictions changed while you were editing. Nothing was saved. Reload and review the protected paths before changing site access")
		}
		merged, conflicts := mergePermissionPolicy(base, current, local)
		if len(conflicts) > 0 {
			return nil, errors.New("someone changed the same permissions while you were editing. Nothing was saved. Reload, review the latest settings, and try again. Conflicting entries: " + strings.Join(conflicts, ", "))
		}
		if err := s.validateSiteAccess(merged); err != nil {
			return nil, err
		}
		if err := s.keepsCallerOwner(ctx, identity, site, merged); err != nil {
			return nil, err
		}
		merged.Published = current.Published
		saved = merged
		return &merged, nil
	})
	if err != nil {
		failed(err)
		return
	}
	slog.Info("site permissions changed in the portal", "site", site, "by", identity.ID)
	view := s.sharingView(r.Context(), site, saved, true)
	view.Message = "Permissions saved. Changes apply to new requests; republishing unchanged settings will not undo them."
	s.renderFragment(w, "sharing", view)
}

func (s *Server) permissionSharingView(ctx context.Context, site string, access SiteAccess) sharingView {
	access = CloneSiteAccess(access)
	access.Published = nil
	encoded, err := json.MarshalIndent(access, "", "  ")
	if err != nil {
		return sharingView{Site: site, V2: true, Error: err.Error()}
	}
	view := sharingView{Site: site, Platform: s.platformName(), V2: true, Policy: string(encoded), Base: string(encoded), General: sharingMode(access), Summary: sharingSummary(access, s.platformName())}
	view.WorkspaceAPIs, err = s.workspaceShareRows(ctx, site, access)
	if err != nil {
		slog.Error("load workspace sharing", "site", site, "error", err)
		view.Error = "Workspace APIs could not be loaded. Reload to try again."
	}
	view.GeneralChoices = choices(view.General, [2]string{"owners", "Only site owners"}, [2]string{"selected", "Only selected people and groups"}, [2]string{"everyone", "Everyone signed in to " + view.Platform})
	if view.General == "custom" {
		view.GeneralChoices = append(view.GeneralChoices, choices("custom", [2]string{"custom", "Custom site admission (keep current settings)"})...)
	}
	var principals []string
	for _, value := range access.Owners {
		principals = appendUnique(principals, value)
	}
	for _, value := range access.Admission.Principals {
		principals = appendUnique(principals, value)
	}
	for _, label := range s.describePrincipals(ctx, principals) {
		role := RoleViewer
		if slices.Contains(access.Owners, label.Principal) {
			role = RoleOwner
		}
		view.Entries = append(view.Entries, shareEntry{Principal: label.Principal, Name: label.Name, Detail: label.Detail, Kind: label.Kind, Initials: initials(label.Name), Tone: tone(label.Principal), Role: role, RoleChoices: choices(role, [2]string{RoleOwner, "Manage site"}, [2]string{RoleViewer, "Can open site"})})
	}
	view.EmptyAttr = flag("hidden", len(view.Entries) > 0)
	for _, binding := range access.Bindings {
		principals = appendUnique(principals, binding.Principal)
	}
	for _, grant := range access.Grants {
		for _, principal := range grant.Subjects.Principals {
			principals = appendUnique(principals, principal)
		}
	}
	labels, err := json.Marshal(s.describePrincipals(ctx, principals))
	if err != nil {
		view.Error = err.Error()
	} else {
		view.Labels = string(labels)
	}
	return view
}

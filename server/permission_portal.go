package hex

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
)

type permissionMatrixRow struct {
	ID, Resource, Operations, Subjects, Condition string
}

type permissionRoleRow struct {
	ID, Description string
	SignedIn        bool
}

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
		view := s.sharingView(r.Context(), site, access, exists)
		view.Error = err.Error()
		view.Policy = r.PostForm.Get("policy")
		s.renderFragment(w, "sharing", view)
	}
	local, err := decodePermissionPolicy([]byte(r.PostForm.Get("policy")))
	if err != nil {
		failed(err)
		return
	}
	base, err := decodePermissionPolicy([]byte(r.PostForm.Get("base")))
	if err != nil {
		failed(errors.New("the permission form's baseline is missing or invalid; reload and try again"))
		return
	}
	var saved SiteAccess
	err = s.config.Access.UpdateSiteAccess(r.Context(), site, func(current SiteAccess, exists bool) (*SiteAccess, error) {
		if err := s.mayChangeAccess(identity, current, exists); err != nil {
			return nil, err
		}
		merged, conflicts := mergePermissionPolicy(base, current, local)
		if len(conflicts) > 0 {
			return nil, errors.New("permissions changed while this form was open; reload and reconcile: " + strings.Join(conflicts, ", "))
		}
		if err := s.validateSiteAccess(merged); err != nil {
			return nil, err
		}
		if err := s.keepsCallerOwner(identity, merged); err != nil {
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
	view.Message = "Permissions saved. New requests use these rules; future publications preserve unchanged portal edits."
	s.renderFragment(w, "sharing", view)
}

package hex

import (
	"errors"
	"net/http"
)

// reconcileAccess establishes a known local baseline without granting or
// revoking any live permission. It is deliberately separate from publishing.
func (s *Server) reconcileAccess(w http.ResponseWriter, r *http.Request) {
	identity, ok := s.accessCaller(w, r)
	if !ok {
		return
	}
	baseline, ok := readSiteAccess(w, r)
	if !ok {
		return
	}
	if err := ValidatePublishedAccess(baseline); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	baseline.Published = nil
	status := 0
	var saved SiteAccess
	ctx, err := s.managementContext(r.Context(), identity, r.PathValue("site"))
	if err != nil {
		writeServerError(w, err)
		return
	}
	err = s.config.Access.UpdateSiteAccess(ctx, r.PathValue("site"), func(current SiteAccess, exists bool) (*SiteAccess, error) {
		if !exists {
			status = http.StatusNotFound
			return nil, ErrNotFound
		}
		if err := s.mayChangeAccess(ctx, identity, r.PathValue("site"), current, true); err != nil {
			status = http.StatusForbidden
			return nil, err
		}
		if current.Version != 2 {
			status = http.StatusConflict
			return nil, errors.New("read the migrated policy with hex access show before reconciling")
		}
		current.Published = &baseline
		saved = current
		return &current, nil
	})
	if err != nil {
		if status == 0 {
			writeServerError(w, err)
		} else {
			writeError(w, status, err.Error())
		}
		return
	}
	saved.Published = nil
	writeJSON(w, http.StatusOK, map[string]any{"access": saved, "accessDrift": accessDrift(baseline, saved)})
}

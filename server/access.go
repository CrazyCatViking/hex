package hex

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"regexp"
)

// SiteAccess is the server-owned access entry for a site. Owners manage the
// entry; groups may view the site and use its API namespace. An entry with no
// groups reserves ownership without restricting viewers. Values are matched
// against the caller's identity ID, group and role claims.
type SiteAccess struct {
	Owners []string `json:"owners"`
	Groups []string `json:"groups"`
}

// AccessStore persists site access entries. Implementations return
// hex.ErrNotFound for sites without an entry; such sites are open to every
// authenticated user.
type AccessStore interface {
	GetSiteAccess(ctx context.Context, site string) (SiteAccess, error)
	PutSiteAccess(ctx context.Context, site string, access SiteAccess) error
	DeleteSiteAccess(ctx context.Context, site string) error
}

var accessValuePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9@._-]{0,127}$`)

const maxAccessValues = 64

func (s *Server) isAdmin(identity *Identity) bool {
	return identity.memberOf(s.config.AdminGroups)
}

// canViewSite decides whether the caller may see a site's assets and use its
// API namespace. Sites without an access entry, or with an entry that lists
// no groups, remain open to every authenticated user.
func (s *Server) canViewSite(ctx context.Context, identity *Identity, site string) (bool, error) {
	if s.config.Access == nil {
		return true, nil
	}

	access, err := s.config.Access.GetSiteAccess(ctx, site)
	if errors.Is(err, ErrNotFound) {
		return true, nil
	}
	if err != nil {
		return false, err
	}

	if len(access.Groups) == 0 {
		return true, nil
	}
	if identity == nil {
		return false, nil
	}

	allowed := s.isAdmin(identity) ||
		identity.memberOf(access.Owners) ||
		identity.memberOf(access.Groups)
	return allowed, nil
}

func (s *Server) canManageSite(identity *Identity, access SiteAccess) bool {
	return s.isAdmin(identity) || identity.memberOf(access.Owners)
}

// siteScoped guards a site-namespaced API handler with the site's access
// entry. Without an access store every request passes through unchanged.
func (s *Server) siteScoped(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.config.Access != nil {
			allowed, err := s.canViewSite(r.Context(), s.requestIdentity(r), r.PathValue("site"))
			if err != nil {
				writeServerError(w, err)
				return
			}
			if !allowed {
				writeError(w, http.StatusForbidden, "access to this site is restricted")
				return
			}
		}

		next(w, r)
	}
}

// staticAuthz answers NGINX auth_request subrequests for static site assets.
// NGINX supplies the validated site name in X-Hex-Site; the response status
// is the whole answer, so this endpoint never returns a body on success.
func (s *Server) staticAuthz(w http.ResponseWriter, r *http.Request) {
	site := r.Header.Get("X-Hex-Site")
	if site == "" || !namePattern.MatchString(site) {
		w.WriteHeader(http.StatusNoContent)
		return
	}

	allowed, err := s.canViewSite(r.Context(), s.requestIdentity(r), site)
	if err != nil {
		writeServerError(w, err)
		return
	}
	if !allowed {
		writeError(w, http.StatusForbidden, "access to this site is restricted")
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) getSiteAccess(w http.ResponseWriter, r *http.Request) {
	identity, ok := s.accessCaller(w, r)
	if !ok {
		return
	}

	access, err := s.config.Access.GetSiteAccess(r.Context(), r.PathValue("site"))
	if err != nil {
		writeServerError(w, err)
		return
	}
	if !s.canManageSite(identity, access) {
		writeError(w, http.StatusForbidden, "only site owners can read the access entry")
		return
	}

	writeJSON(w, http.StatusOK, access)
}

func (s *Server) putSiteAccess(w http.ResponseWriter, r *http.Request) {
	identity, ok := s.accessCaller(w, r)
	if !ok {
		return
	}

	requested, ok := readSiteAccess(w, r)
	if !ok {
		return
	}

	site := r.PathValue("site")
	existing, err := s.config.Access.GetSiteAccess(r.Context(), site)
	if err != nil && !errors.Is(err, ErrNotFound) {
		writeServerError(w, err)
		return
	}
	if err == nil && !s.canManageSite(identity, existing) {
		writeError(w, http.StatusForbidden, "only site owners can change the access entry")
		return
	}
	if !s.canManageSite(identity, requested) {
		writeError(w, http.StatusBadRequest, "owners must keep you able to manage this entry; include your ID or one of your groups")
		return
	}

	if err := s.config.Access.PutSiteAccess(r.Context(), site, requested); err != nil {
		writeServerError(w, err)
		return
	}

	writeJSON(w, http.StatusOK, requested)
}

func (s *Server) deleteSiteAccess(w http.ResponseWriter, r *http.Request) {
	identity, ok := s.accessCaller(w, r)
	if !ok {
		return
	}

	site := r.PathValue("site")
	access, err := s.config.Access.GetSiteAccess(r.Context(), site)
	if err != nil {
		writeServerError(w, err)
		return
	}
	if !s.canManageSite(identity, access) {
		writeError(w, http.StatusForbidden, "only site owners can remove the access entry")
		return
	}

	if err := s.config.Access.DeleteSiteAccess(r.Context(), site); err != nil {
		writeServerError(w, err)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// accessCaller validates the site path value and requires an authenticated
// caller for access-management requests.
func (s *Server) accessCaller(w http.ResponseWriter, r *http.Request) (*Identity, bool) {
	if !validateIdentifiers(w, r) {
		return nil, false
	}

	identity := s.requestIdentity(r)
	if identity == nil {
		writeError(w, http.StatusUnauthorized, "not authenticated")
		return nil, false
	}

	return identity, true
}

func readSiteAccess(w http.ResponseWriter, r *http.Request) (SiteAccess, bool) {
	data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 64<<10))
	if err != nil {
		writeError(w, http.StatusBadRequest, "expected a JSON access entry of at most 64 KiB")
		return SiteAccess{}, false
	}

	var access SiteAccess
	if err := json.Unmarshal(data, &access); err != nil {
		writeError(w, http.StatusBadRequest, "expected a JSON access entry with owners and groups arrays")
		return SiteAccess{}, false
	}

	for _, values := range [][]string{access.Owners, access.Groups} {
		if len(values) > maxAccessValues {
			writeError(w, http.StatusBadRequest, "too many access values; at most 64 owners and 64 groups")
			return SiteAccess{}, false
		}
		for _, value := range values {
			if !accessValuePattern.MatchString(value) {
				writeError(w, http.StatusBadRequest, "invalid access value: "+value)
				return SiteAccess{}, false
			}
		}
	}
	if access.Owners == nil {
		access.Owners = []string{}
	}
	if access.Groups == nil {
		access.Groups = []string{}
	}

	return access, true
}

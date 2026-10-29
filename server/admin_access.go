package hex

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"
)

const (
	adminManage = "management"
	adminRead   = "app-read"
	adminWrite  = "app-write"
)

func (s *Server) adminHuman(identity *Identity) bool {
	return identity != nil && identity.ID != "" && identity.Provider != "automation" && s.isAdmin(identity)
}

func (s *Server) activeAdminAccess(ctx context.Context, identity *Identity, site string) ([]AdminAccessGrant, error) {
	if !s.adminHuman(identity) || s.config.AdminAccess == nil {
		return nil, nil
	}
	query := AdminAccessQuery{Site: site, Subject: preferenceID(identity), ActiveAt: time.Now(), Limit: 1000}
	var grants []AdminAccessGrant
	for {
		page, err := s.config.AdminAccess.ListAdminAccess(ctx, query)
		if err != nil {
			return nil, err
		}
		grants = append(grants, page...)
		if len(page) < query.Limit {
			return grants, nil
		}
		cursor := page[len(page)-1].ID
		if cursor == "" || query.Before != "" && cursor >= query.Before {
			return nil, errors.New("temporary-access store returned a non-advancing cursor")
		}
		query.Before = cursor
	}
}

func hasAdminScope(grants []AdminAccessGrant, site, scope string) bool {
	return slices.ContainsFunc(grants, func(grant AdminAccessGrant) bool {
		return grant.Site == site && grant.Scope == scope && grant.Active(time.Now())
	})
}

// Application privileges are projected into a request-local policy. They never
// turn the caller into a site owner or alter the saved/publication baseline.
func adminApplicationPolicy(access SiteAccess, identity *Identity, grants []AdminAccessGrant, site string) SiteAccess {
	write := hasAdminScope(grants, site, adminWrite)
	if !write && !hasAdminScope(grants, site, adminRead) {
		return access
	}
	access = CloneSiteAccess(MigrateSiteAccess(access))
	principal := "user:" + identity.ID
	access.Admission.Principals = appendUnique(access.Admission.Principals, principal)
	if access.Grants == nil {
		access.Grants = make(map[string]PermissionGrant)
	}
	operations := map[string][]string{
		"path": {"read"}, "collection": {"read"}, "file": {"read"}, "channel": {"read"},
	}
	if write {
		operations["collection"] = []string{"read", "create", "update", "delete"}
		operations["file"] = []string{"read", "upload", "delete"}
		operations["channel"] = []string{"read", "send"}
		operations["action"] = []string{"run"}
		operations["integration"] = []string{"read", "write"}
	}
	for kind, ops := range operations {
		access.Grants["@hex-admin-"+kind] = PermissionGrant{Kind: kind, Resource: "*", Operations: ops, Subjects: Subjects{Principals: []string{principal}}}
	}
	return access
}

type managementAccessKey struct{}

func (s *Server) managementContext(ctx context.Context, identity *Identity, site string) (context.Context, error) {
	grants, err := s.activeAdminAccess(ctx, identity, site)
	if err != nil {
		return ctx, err
	}
	return context.WithValue(ctx, managementAccessKey{}, grants), nil
}

func temporaryManagement(ctx context.Context, site string) bool {
	grants, _ := ctx.Value(managementAccessKey{}).([]AdminAccessGrant)
	return hasAdminScope(grants, site, adminManage)
}

func (s *Server) canManageSite(ctx context.Context, identity *Identity, site string, access SiteAccess, exists bool) (bool, error) {
	if s.siteRole(identity, access, exists) == roleOwner {
		return true, nil
	}
	ctx, err := s.managementContext(ctx, identity, site)
	return err == nil && temporaryManagement(ctx, site), err
}

type adminAccessView struct {
	Chrome
	Site    string
	Enabled bool
	Admin   bool
	Locked  bool
	Grants  []AdminAccessGrant
	Next    string
}

func (s *Server) registerAdminAccessRoutes() {
	if !s.manageEnabled() {
		return
	}
	s.mux.HandleFunc("POST /api/hex/admin/sites/{site}/access", s.grantAdminAccess)
	s.mux.HandleFunc("POST /api/hex/admin/sites/{site}/access/{id}/revoke", s.revokeAdminAccess)
}

func (s *Server) adminAccessTargetExists(ctx context.Context, site string, policyExists bool) (bool, error) {
	if policyExists {
		return true, nil
	}
	// Older published sites may have no saved policy. Allow deliberate recovery
	// without creating permanent ownership as a side effect of granting access.
	names, err := s.config.Sites.ListSites(ctx)
	if err != nil {
		return false, err
	}
	return slices.Contains(names, site), nil
}

// This page is deliberately limited to administrative identity/access metadata.
// It is available before elevation and does not load app content or analytics.
func (s *Server) adminAccessPage(w http.ResponseWriter, r *http.Request) bool {
	site := r.PathValue("site")
	identity := s.requestIdentity(r)
	if identity == nil || !siteNamePattern.MatchString(site) {
		return false
	}
	if r.URL.Query().Get("tab") != "admin-access" && !s.adminHuman(identity) {
		return false
	}
	access, exists, err := s.sitePolicy(r.Context(), site)
	if err != nil {
		writeServerError(w, err)
		return true
	}
	allowed, err := s.canManageSite(r.Context(), identity, site, access, exists)
	if err != nil {
		writeServerError(w, err)
		return true
	}
	if r.URL.Query().Get("tab") != "admin-access" && (allowed || !s.adminHuman(identity)) {
		return false
	}
	if !allowed && !s.adminHuman(identity) {
		return false
	}
	view := adminAccessView{Chrome: s.chromeFor(identity, "admin"), Site: site, Enabled: s.config.AdminAccess != nil, Admin: s.adminHuman(identity), Locked: !allowed}
	found, err := s.adminAccessTargetExists(r.Context(), site, exists)
	if err != nil {
		writeServerError(w, err)
		return true
	}
	if !found {
		writeError(w, http.StatusNotFound, "site not found")
		return true
	}
	if s.config.AdminAccess != nil {
		view.Grants, err = s.config.AdminAccess.ListAdminAccess(r.Context(), AdminAccessQuery{Site: site, Before: r.URL.Query().Get("before"), Limit: 26})
		if err != nil {
			writeServerError(w, err)
			return true
		}
		if len(view.Grants) > 25 {
			view.Grants = view.Grants[:25]
			view.Next = "/manage/" + site + "?tab=admin-access&before=" + view.Grants[24].ID
		}
	}
	s.writePortalPage(w, "admin-access", view)
	return true
}

func (s *Server) grantAdminAccess(w http.ResponseWriter, r *http.Request) {
	identity, ok := s.adminCaller(w, r)
	if !ok {
		return
	}
	if !s.adminHuman(identity) {
		writeError(w, 403, "only human platform administrators can elevate access")
		return
	}
	if s.config.AdminAccess == nil {
		writeError(w, 501, "temporary access requires an AdminAccessStore")
		return
	}
	site := r.PathValue("site")
	if !siteNamePattern.MatchString(site) {
		writeError(w, 400, "invalid site")
		return
	}
	_, exists, err := s.sitePolicy(r.Context(), site)
	if err != nil {
		writeServerError(w, err)
		return
	}
	found, err := s.adminAccessTargetExists(r.Context(), site, exists)
	if err != nil {
		writeServerError(w, err)
		return
	}
	if !found {
		writeError(w, http.StatusNotFound, "site not found")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 8192)
	if err := r.ParseForm(); err != nil {
		writeError(w, 400, "invalid access request")
		return
	}
	scope, reason := r.PostForm.Get("scope"), strings.TrimSpace(r.PostForm.Get("reason"))
	minutes := 60
	if value := r.PostForm.Get("minutes"); value != "" {
		minutes, err = strconv.Atoi(value)
	}
	if err != nil || minutes < 1 || minutes > 240 || len(reason) < 3 || len(reason) > 1000 || !slices.Contains([]string{adminManage, adminRead, adminWrite}, scope) {
		writeError(w, 400, "choose an access scope, a reason of 3–1000 bytes and a duration of 1–240 minutes")
		return
	}
	id, err := newID()
	if err != nil {
		writeServerError(w, err)
		return
	}
	now := time.Now().UTC()
	// Timestamp prefix gives audit pages a stable, newest-first cursor.
	grant := AdminAccessGrant{ID: fmt.Sprintf("%020d-%s", now.UnixMicro(), id), Site: site, Subject: preferenceID(identity), Actor: Person{ID: identity.ID, Name: identity.Name, Email: identity.Email}, Scope: scope, Reason: reason, StartedAt: now, ExpiresAt: now.Add(time.Duration(minutes) * time.Minute)}
	if err := s.config.AdminAccess.GrantAdminAccess(r.Context(), grant); err != nil {
		writeServerError(w, err)
		return
	}
	adminAccessRedirect(w, r, site)
}

func (s *Server) revokeAdminAccess(w http.ResponseWriter, r *http.Request) {
	if !s.platformRequest(r) {
		http.NotFound(w, r)
		return
	}
	identity, ok := s.accessCaller(w, r)
	if !ok {
		return
	}
	if s.config.AdminAccess == nil {
		writeError(w, 501, "temporary access is unavailable")
		return
	}
	site, id := r.PathValue("site"), r.PathValue("id")
	access, exists, err := s.sitePolicy(r.Context(), site)
	if err != nil {
		writeServerError(w, err)
		return
	}
	grants, err := s.config.AdminAccess.ListAdminAccess(r.Context(), AdminAccessQuery{ID: id, Site: site, Limit: 1})
	if err != nil {
		writeServerError(w, err)
		return
	}
	index := slices.IndexFunc(grants, func(g AdminAccessGrant) bool { return g.ID == id })
	if index < 0 {
		writeError(w, 404, "grant not found")
		return
	}
	if s.siteRole(identity, access, exists) != roleOwner && (!s.adminHuman(identity) || grants[index].Subject != preferenceID(identity)) {
		writeError(w, 403, "only a site owner or the elevated administrator can end this access")
		return
	}
	err = s.config.AdminAccess.RevokeAdminAccess(r.Context(), site, id, Person{ID: identity.ID, Name: identity.Name, Email: identity.Email}, time.Now().UTC())
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			writeError(w, 404, "grant not found")
		} else {
			writeServerError(w, err)
		}
		return
	}
	adminAccessRedirect(w, r, site)
}

func adminAccessRedirect(w http.ResponseWriter, r *http.Request, site string) {
	target := "/manage/" + site + "?tab=admin-access"
	if r.Header.Get("HX-Request") == "true" {
		w.Header().Set("HX-Redirect", target)
		w.WriteHeader(http.StatusNoContent)
		return
	}
	http.Redirect(w, r, target, http.StatusSeeOther)
}

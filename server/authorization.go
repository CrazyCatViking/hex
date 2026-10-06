package hex

import (
	"context"
	"errors"
	"net/http"
	"strings"
)

type siteRole int

const (
	roleNone siteRole = iota
	roleViewer
	roleEditor
	roleOwner
)

// grant is the outcome of checking a data rule: no access, access to the
// caller's own documents only, or access to everything the rule covers.
type grant int

const (
	grantNone grant = iota
	grantOwn
	grantAll
)

func (s *Server) isAdmin(identity *Identity) bool {
	return identity.matchesAny(s.config.AdminGroups)
}

// canCreateSites decides whether the caller may claim an unclaimed site name.
// Without configured publisher groups every signed-in user may.
func (s *Server) canCreateSites(identity *Identity) bool {
	if identity == nil {
		return false
	}
	if len(s.config.PublisherGroups) == 0 {
		return true
	}
	return s.isAdmin(identity) || identity.matchesAny(s.config.PublisherGroups)
}

// siteRole resolves the caller's role on a site. Sites without a policy are
// open: everyone may view and edit, and only admins own them. A configured
// identity resolver requires a signed-in caller. Within a policy, empty viewers
// means every signed-in user may view and empty editors means every viewer may edit.
func (s *Server) siteRole(identity *Identity, access SiteAccess, exists bool) siteRole {
	if s.config.Identity != nil && identity == nil {
		return roleNone
	}
	if s.isAdmin(identity) {
		return roleOwner
	}
	if !exists {
		return roleEditor
	}
	if identity.matchesAny(access.Owners) {
		return roleOwner
	}

	explicitEditor := identity.matchesAny(access.Editors)
	viewer := explicitEditor || len(access.Viewers) == 0 || identity.matchesAny(access.Viewers)
	if !viewer {
		return roleNone
	}
	if explicitEditor || len(access.Editors) == 0 {
		return roleEditor
	}
	return roleViewer
}

// audienceGrant checks one audience of a rule. Owners pass every rule and
// callers who cannot view the site pass none. An unset audience falls back to
// defaultLevel.
func audienceGrant(role siteRole, identity *Identity, audience Audience, defaultLevel string) grant {
	if role == roleOwner {
		return grantAll
	}
	if role == roleNone {
		return grantNone
	}
	if len(audience.Principals) > 0 {
		if identity.matchesAny(audience.Principals) {
			return grantAll
		}
		return grantNone
	}

	level := audience.Level
	if level == "" {
		level = defaultLevel
	}
	switch level {
	case LevelViewers:
		return grantAll
	case LevelEditors:
		if role >= roleEditor {
			return grantAll
		}
	case LevelCreator:
		if identity != nil && identity.ID != "" {
			return grantOwn
		}
	}
	return grantNone
}

type dataAccess struct {
	read  grant
	write grant
}

func ruleAccess(role siteRole, identity *Identity, rule DataRule) dataAccess {
	return dataAccess{
		read:  audienceGrant(role, identity, rule.Read, LevelViewers),
		write: audienceGrant(role, identity, rule.Write, LevelEditors),
	}
}

func namedRule(rules map[string]DataRule, name string) DataRule {
	if rule, ok := rules[name]; ok {
		return rule
	}
	return rules["*"]
}

// fileRule picks the longest file prefix rule covering key, then "*".
func fileRule(rules map[string]DataRule, key string) DataRule {
	best := ""
	found := false
	for prefix := range rules {
		if prefix == "*" || !keyHasPrefix(key, prefix) {
			continue
		}
		if !found || len(prefix) > len(best) {
			best = prefix
			found = true
		}
	}
	if found {
		return rules[best]
	}
	return rules["*"]
}

func keyHasPrefix(key, prefix string) bool {
	if strings.HasSuffix(prefix, "/") {
		return strings.HasPrefix(key, prefix)
	}
	return key == prefix || strings.HasPrefix(key, prefix+"/")
}

// pathAllowed applies the longest matching path rule to a static asset path.
// A rule for /admin/ also covers /admin itself, which NGINX would otherwise
// resolve to /admin/index.html after this check.
func (s *Server) pathAllowed(role siteRole, identity *Identity, access SiteAccess, path string) bool {
	path = s.pathComparisonKey(path)
	var matched *PathRule
	for i := range access.Paths {
		prefix := s.pathComparisonKey(access.Paths[i].Prefix)
		covers := strings.HasPrefix(path, prefix) || path+"/" == prefix
		if covers && (matched == nil || len(prefix) > len(matched.Prefix)) {
			matched = &access.Paths[i]
		}
	}
	if matched == nil {
		return true
	}
	return audienceGrant(role, identity, matched.Viewers, LevelViewers) == grantAll
}

func (s *Server) pathComparisonKey(path string) string {
	if s.config.PathCaseInsensitive {
		return strings.ToLower(path)
	}
	return path
}

// siteAuthorization is the resolved view of one caller on one site.
type siteAuthorization struct {
	identity *Identity
	access   SiteAccess
	role     siteRole
}

func (a siteAuthorization) collection(name string) dataAccess {
	return ruleAccess(a.role, a.identity, namedRule(a.access.Collections, name))
}

// collectionWriteOptions records the creator and applies creator-only writes.
func (a siteAuthorization) collectionWriteOptions(name string) (WriteOptions, error) {
	options := WriteOptions{Creator: a.creator()}
	switch a.collection(name).write {
	case grantNone:
		return WriteOptions{}, ErrForbidden
	case grantOwn:
		options.CreatorOnly = true
	}
	return options, nil
}

func (a siteAuthorization) file(key string) dataAccess {
	return ruleAccess(a.role, a.identity, fileRule(a.access.Files, key))
}

func (a siteAuthorization) channel(name string) dataAccess {
	return ruleAccess(a.role, a.identity, namedRule(a.access.Channels, name))
}

// creator is the identity ID recorded on documents the caller creates.
func (a siteAuthorization) creator() string {
	if a.identity == nil {
		return ""
	}
	return a.identity.ID
}

func (s *Server) authorizeSite(ctx context.Context, identity *Identity, site string) (siteAuthorization, error) {
	access, exists, err := s.sitePolicy(ctx, site)
	if err != nil {
		return siteAuthorization{}, err
	}
	return siteAuthorization{
		identity: identity,
		access:   access,
		role:     s.siteRole(identity, access, exists),
	}, nil
}

// sitePolicy reads the current policy. Security decisions must not use a
// replica-local cache: another replica may have restricted a site immediately
// before uploading private files. Without an access store every site is open.
func (s *Server) sitePolicy(ctx context.Context, site string) (SiteAccess, bool, error) {
	if s.config.Access == nil {
		return SiteAccess{}, false, nil
	}
	access, err := s.config.Access.GetSiteAccess(ctx, site)
	if errors.Is(err, ErrNotFound) {
		return SiteAccess{}, false, nil
	}
	if err != nil {
		return SiteAccess{}, false, err
	}
	if err := s.validatePathPrefixes(access.Paths); err != nil {
		return SiteAccess{}, false, err
	}
	return NormalizeSiteAccess(access), true, nil
}

func (s *Server) canViewSite(ctx context.Context, identity *Identity, site string) (bool, error) {
	authorization, err := s.authorizeSite(ctx, identity, site)
	if err != nil {
		return false, err
	}
	return authorization.role != roleNone, nil
}

type siteAuthorizationKey struct{}

func requestAuthorization(r *http.Request) siteAuthorization {
	authorization, _ := r.Context().Value(siteAuthorizationKey{}).(siteAuthorization)
	return authorization
}

// siteScoped guards a site-namespaced API handler: the caller must be able to
// view the site, and the resolved authorization is passed to the handler for
// its data rules.
func (s *Server) siteScoped(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !validateIdentifiers(w, r) {
			return
		}

		identity := s.requestIdentity(r)
		if s.config.Identity != nil && identity == nil {
			writeError(w, http.StatusUnauthorized, "authentication required")
			return
		}

		authorization, err := s.authorizeSite(r.Context(), identity, r.PathValue("site"))
		if err != nil {
			writeServerError(w, err)
			return
		}
		if authorization.role == roleNone {
			writeError(w, http.StatusForbidden, "access to this site is restricted")
			return
		}

		ctx := context.WithValue(r.Context(), siteAuthorizationKey{}, authorization)
		next(w, r.WithContext(ctx))
	}
}

// staticAuthz answers NGINX auth_request subrequests for static site assets.
// NGINX supplies the validated site name in X-Hex-Site and the normalized
// request path in X-Hex-Path; the response status is the whole answer, so
// this endpoint never returns a body on success.
func (s *Server) staticAuthz(w http.ResponseWriter, r *http.Request) {
	site := r.Header.Get("X-Hex-Site")
	if site == "" || !namePattern.MatchString(site) {
		w.WriteHeader(http.StatusNoContent)
		return
	}

	identity := s.requestIdentity(r)
	s.analyticsIdentity(w, identity)
	authorization, err := s.authorizeSite(r.Context(), identity, site)
	if err != nil {
		writeServerError(w, err)
		return
	}
	path := r.Header.Get("X-Hex-Path")
	if path == "" {
		path = "/"
	}
	allowed := authorization.role != roleNone &&
		s.pathAllowed(authorization.role, authorization.identity, authorization.access, path)
	if !allowed {
		writeError(w, http.StatusForbidden, "access to this page is restricted")
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

type permissionsDocument struct {
	Role        string                    `json:"role"`
	Admin       bool                      `json:"admin"`
	Publish     bool                      `json:"publish"`
	Paths       []pathPermission          `json:"paths"`
	Collections map[string]dataPermission `json:"collections"`
	Files       map[string]dataPermission `json:"files"`
	Channels    map[string]dataPermission `json:"channels"`
}

type pathPermission struct {
	Prefix  string `json:"prefix"`
	Allowed bool   `json:"allowed"`
}

type dataPermission struct {
	Read  string `json:"read"`
	Write string `json:"write"`
}

var roleNames = map[siteRole]string{
	roleNone:   "none",
	roleViewer: "viewer",
	roleEditor: "editor",
	roleOwner:  "owner",
}

var grantNames = map[grant]string{
	grantNone: "none",
	grantOwn:  "own",
	grantAll:  "all",
}

// permissions describes what the caller may do on a site, so apps can hide
// links and controls. It is advisory: every request is still checked.
func (s *Server) permissions(w http.ResponseWriter, r *http.Request) {
	if !validateIdentifiers(w, r) {
		return
	}

	identity := s.requestIdentity(r)
	site := r.PathValue("site")
	access, exists, err := s.sitePolicy(r.Context(), site)
	if err != nil {
		writeServerError(w, err)
		return
	}
	role := s.siteRole(identity, access, exists)

	// Callers who cannot view the site learn nothing about its rules.
	if role == roleNone {
		access = SiteAccess{}
	}
	document := permissionsDocument{
		Role:        roleNames[role],
		Admin:       s.isAdmin(identity),
		Publish:     role == roleOwner || (!exists && s.canCreateSites(identity)),
		Paths:       []pathPermission{},
		Collections: describeRules(role, identity, access.Collections),
		Files:       describeRules(role, identity, access.Files),
		Channels:    describeRules(role, identity, access.Channels),
	}
	for _, rule := range access.Paths {
		document.Paths = append(document.Paths, pathPermission{
			Prefix:  rule.Prefix,
			Allowed: role != roleNone && audienceGrant(role, identity, rule.Viewers, LevelViewers) == grantAll,
		})
	}

	writeJSON(w, http.StatusOK, document)
}

func describeRules(role siteRole, identity *Identity, rules map[string]DataRule) map[string]dataPermission {
	described := map[string]dataPermission{}
	if _, ok := rules["*"]; !ok {
		rules = withDefaultRule(rules)
	}
	for name, rule := range rules {
		result := ruleAccess(role, identity, rule)
		described[name] = dataPermission{Read: grantNames[result.read], Write: grantNames[result.write]}
	}
	return described
}

func withDefaultRule(rules map[string]DataRule) map[string]DataRule {
	extended := map[string]DataRule{"*": {}}
	for name, rule := range rules {
		extended[name] = rule
	}
	return extended
}

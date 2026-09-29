package hex

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"regexp"
	"slices"
	"strings"
)

// SiteAccess is the server-owned access policy for a site.
//
// Owners manage the policy and publish the site. Editors may write the site's
// data and viewers may see it; an empty Editors list lets every viewer edit,
// and an empty Viewers list lets every signed-in user view. Paths restrict
// static URL prefixes further, and Collections, Files and Channels override
// who may read and write each part of the site's data. Values are typed
// principals: user:<id or principal name>, group:<id> or role:<value>.
type SiteAccess struct {
	Owners      []string            `json:"owners"`
	Editors     []string            `json:"editors"`
	Viewers     []string            `json:"viewers"`
	Paths       []PathRule          `json:"paths,omitempty"`
	Collections map[string]DataRule `json:"collections,omitempty"`
	Files       map[string]DataRule `json:"files,omitempty"`
	Channels    map[string]DataRule `json:"channels,omitempty"`
}

// PathRule limits the static assets under Prefix to Viewers. Prefixes are
// matched case-insensitively because Azure Files, which backs published
// sites, resolves paths case-insensitively.
type PathRule struct {
	Prefix  string   `json:"prefix"`
	Viewers Audience `json:"viewers"`
}

// DataRule decides who may read and write one collection, file prefix or
// realtime channel. An unset Read defaults to viewers and an unset Write to
// editors.
type DataRule struct {
	Read  Audience `json:"read,omitzero"`
	Write Audience `json:"write,omitzero"`
}

// Audience is either a level (viewers, editors, owners or creator) or an
// explicit list of principals. In JSON it is a string or an array.
type Audience struct {
	Level      string
	Principals []string
}

const (
	LevelViewers = "viewers"
	LevelEditors = "editors"
	LevelOwners  = "owners"
	LevelCreator = "creator"
)

func (a Audience) IsZero() bool {
	return a.Level == "" && len(a.Principals) == 0
}

func (a Audience) MarshalJSON() ([]byte, error) {
	if a.Level != "" {
		return json.Marshal(a.Level)
	}
	return json.Marshal(a.Principals)
}

func (a *Audience) UnmarshalJSON(data []byte) error {
	var level string
	if err := json.Unmarshal(data, &level); err == nil {
		*a = Audience{Level: level}
		return nil
	}

	var principals []string
	if err := json.Unmarshal(data, &principals); err != nil {
		return errors.New("an audience is a level name or an array of principals")
	}
	*a = Audience{Principals: principals}
	return nil
}

// AccessStore persists site access policies. Implementations return
// hex.ErrNotFound for sites without a policy; such sites are open to every
// authenticated user.
type AccessStore interface {
	GetSiteAccess(ctx context.Context, site string) (SiteAccess, error)
	PutSiteAccess(ctx context.Context, site string, access SiteAccess) error
	DeleteSiteAccess(ctx context.Context, site string) error
}

var (
	principalPattern  = regexp.MustCompile(`^(user|group|role):[A-Za-z0-9][A-Za-z0-9@._-]{0,127}$`)
	pathPrefixPattern = regexp.MustCompile(`^/[A-Za-z0-9._~/-]{0,255}$`)
)

const (
	maxAccessValues = 64
	maxPathRules    = 32
	maxDataRules    = 64
)

// NormalizeSiteAccess replaces nil principal lists with empty ones so that
// stored and returned policies always carry arrays.
func NormalizeSiteAccess(access SiteAccess) SiteAccess {
	if access.Owners == nil {
		access.Owners = []string{}
	}
	if access.Editors == nil {
		access.Editors = []string{}
	}
	if access.Viewers == nil {
		access.Viewers = []string{}
	}
	return access
}

// CloneSiteAccess returns a deep copy, for stores that keep policies in
// memory.
func CloneSiteAccess(access SiteAccess) SiteAccess {
	cloned := SiteAccess{
		Owners:      slices.Clone(access.Owners),
		Editors:     slices.Clone(access.Editors),
		Viewers:     slices.Clone(access.Viewers),
		Collections: cloneRules(access.Collections),
		Files:       cloneRules(access.Files),
		Channels:    cloneRules(access.Channels),
	}
	for _, rule := range access.Paths {
		cloned.Paths = append(cloned.Paths, PathRule{Prefix: rule.Prefix, Viewers: cloneAudience(rule.Viewers)})
	}
	return NormalizeSiteAccess(cloned)
}

func cloneRules(rules map[string]DataRule) map[string]DataRule {
	if rules == nil {
		return nil
	}
	cloned := make(map[string]DataRule, len(rules))
	for name, rule := range rules {
		cloned[name] = DataRule{Read: cloneAudience(rule.Read), Write: cloneAudience(rule.Write)}
	}
	return cloned
}

func cloneAudience(audience Audience) Audience {
	return Audience{Level: audience.Level, Principals: slices.Clone(audience.Principals)}
}

func validateSiteAccess(access SiteAccess) error {
	lists := map[string][]string{"owners": access.Owners, "editors": access.Editors, "viewers": access.Viewers}
	for _, name := range slices.Sorted(maps.Keys(lists)) {
		if err := validatePrincipals(name, lists[name]); err != nil {
			return err
		}
	}

	if len(access.Paths) > maxPathRules {
		return fmt.Errorf("at most %d path rules are allowed", maxPathRules)
	}
	for _, rule := range access.Paths {
		if !pathPrefixPattern.MatchString(rule.Prefix) || strings.Contains(rule.Prefix, "..") || strings.Contains(rule.Prefix, "//") {
			return fmt.Errorf("invalid path prefix %q; use an absolute URL path such as /admin/", rule.Prefix)
		}
		if rule.Viewers.IsZero() {
			return fmt.Errorf("path rule %s needs viewers", rule.Prefix)
		}
		if err := validateAudience("path "+rule.Prefix, rule.Viewers, false); err != nil {
			return err
		}
	}

	if err := validateRules("collection", access.Collections, validCollectionName, true); err != nil {
		return err
	}
	if err := validateRules("file prefix", access.Files, validFilePrefix, false); err != nil {
		return err
	}
	return validateRules("channel", access.Channels, validCollectionName, false)
}

func validatePrincipals(field string, principals []string) error {
	if len(principals) > maxAccessValues {
		return fmt.Errorf("at most %d %s are allowed", maxAccessValues, field)
	}
	for _, principal := range principals {
		if !principalPattern.MatchString(principal) {
			return fmt.Errorf("invalid principal %q in %s; use user:<id>, group:<id> or role:<value>", principal, field)
		}
	}
	return nil
}

func validateRules(kind string, rules map[string]DataRule, validName func(string) bool, allowCreator bool) error {
	if len(rules) > maxDataRules {
		return fmt.Errorf("at most %d %s rules are allowed", maxDataRules, kind)
	}
	for _, name := range slices.Sorted(maps.Keys(rules)) {
		if name != "*" && !validName(name) {
			return fmt.Errorf("invalid %s %q", kind, name)
		}
		rule := rules[name]
		label := kind + " " + name
		if err := validateAudience(label+" read", rule.Read, allowCreator); err != nil {
			return err
		}
		if err := validateAudience(label+" write", rule.Write, allowCreator); err != nil {
			return err
		}
	}
	return nil
}

func validateAudience(label string, audience Audience, allowCreator bool) error {
	if audience.Level != "" && len(audience.Principals) > 0 {
		return fmt.Errorf("%s: choose a level or principals, not both", label)
	}
	switch audience.Level {
	case "", LevelViewers, LevelEditors, LevelOwners:
	case LevelCreator:
		if !allowCreator {
			return fmt.Errorf("%s: the creator level is only available for collections", label)
		}
	default:
		return fmt.Errorf("%s: unknown level %q; use viewers, editors, owners or creator", label, audience.Level)
	}
	return validatePrincipals(label, audience.Principals)
}

func validCollectionName(name string) bool {
	return namePattern.MatchString(name)
}

func validFilePrefix(prefix string) bool {
	return validKey(strings.TrimSuffix(prefix, "/"))
}

func (s *Server) getSiteAccess(w http.ResponseWriter, r *http.Request) {
	identity, ok := s.accessCaller(w, r)
	if !ok {
		return
	}

	site := r.PathValue("site")
	access, exists, err := s.sitePolicy(r.Context(), site)
	if err != nil {
		writeServerError(w, err)
		return
	}
	if !exists {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	if s.siteRole(identity, access, true) != roleOwner {
		writeError(w, http.StatusForbidden, "only site owners can read the access policy")
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
	saved, status, err := s.replaceSiteAccess(r.Context(), identity, site, requested)
	if err != nil {
		if status == 0 {
			writeServerError(w, err)
			return
		}
		writeError(w, status, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, saved)
}

// replaceSiteAccess stores a new policy on behalf of the caller. Existing
// policies may only be replaced by their owners; an unclaimed site may be
// claimed by anyone allowed to create sites. A policy without owners keeps
// the current owners, or makes the caller the owner of a new policy, and the
// result must keep the caller able to manage it. A non-zero status describes
// a client error; zero means err is a server failure.
func (s *Server) replaceSiteAccess(ctx context.Context, identity *Identity, site string, requested SiteAccess) (SiteAccess, int, error) {
	if err := validateSiteAccess(requested); err != nil {
		return SiteAccess{}, http.StatusBadRequest, err
	}

	existing, exists, err := s.sitePolicy(ctx, site)
	if err != nil {
		return SiteAccess{}, 0, err
	}
	if exists && s.siteRole(identity, existing, true) != roleOwner {
		return SiteAccess{}, http.StatusForbidden, errors.New("only site owners can change the access policy")
	}
	if !exists && !s.canCreateSites(identity) {
		return SiteAccess{}, http.StatusForbidden, errors.New("you are not allowed to create sites on this platform")
	}

	requested = s.withOwners(identity, requested, existing, exists)
	if err := s.keepsCallerOwner(identity, requested); err != nil {
		return SiteAccess{}, http.StatusBadRequest, err
	}

	if err := s.config.Access.PutSiteAccess(ctx, site, requested); err != nil {
		return SiteAccess{}, 0, err
	}
	s.policies.invalidate(site)
	return requested, 0, nil
}

// withOwners fills in the owners of a policy that names none: the current
// owners, or the caller for a new policy.
func (s *Server) withOwners(identity *Identity, requested, existing SiteAccess, exists bool) SiteAccess {
	requested = NormalizeSiteAccess(requested)
	if len(requested.Owners) > 0 {
		return requested
	}
	if exists {
		requested.Owners = slices.Clone(existing.Owners)
	} else {
		requested.Owners = []string{"user:" + identity.ID}
	}
	return requested
}

func (s *Server) keepsCallerOwner(identity *Identity, access SiteAccess) error {
	if s.siteRole(identity, access, true) != roleOwner {
		return errors.New("owners must keep you able to manage this policy; include your user or one of your groups")
	}
	return nil
}

func (s *Server) deleteSiteAccess(w http.ResponseWriter, r *http.Request) {
	identity, ok := s.accessCaller(w, r)
	if !ok {
		return
	}

	site := r.PathValue("site")
	access, exists, err := s.sitePolicy(r.Context(), site)
	if err != nil {
		writeServerError(w, err)
		return
	}
	if !exists {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	if s.siteRole(identity, access, true) != roleOwner {
		writeError(w, http.StatusForbidden, "only site owners can remove the access policy")
		return
	}

	if err := s.config.Access.DeleteSiteAccess(r.Context(), site); err != nil {
		writeServerError(w, err)
		return
	}
	s.policies.invalidate(site)

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
		writeError(w, http.StatusBadRequest, "expected a JSON access policy of at most 64 KiB")
		return SiteAccess{}, false
	}

	var access SiteAccess
	if err := json.Unmarshal(data, &access); err != nil {
		writeError(w, http.StatusBadRequest, "expected a JSON access policy: "+err.Error())
		return SiteAccess{}, false
	}

	return access, true
}

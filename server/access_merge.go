package hex

import (
	"context"
	"fmt"
	"maps"
	"net/http"
	"slices"
	"strings"
)

// A publication's access section is merged into the live policy instead of
// replacing it, so changes made in the portal or with hex access survive
// republishing. The policy keeps the access section of the last publication
// as the common base: only what the publication changed since then is
// applied, and a rule changed differently on both sides is a conflict.

// publishSiteAccess applies a publication's access section on behalf of the
// caller. New sites and replace requests take it as it is. Otherwise it is
// merged into the live policy; a missing baseline requires explicit
// reconciliation. It returns the saved policy and how it still differs from the
// section. A non-zero status describes a client error; zero means err is a
// server failure.
func (s *Server) publishSiteAccess(ctx context.Context, identity *Identity, site string, published SiteAccess, replace bool) (SiteAccess, []string, int, error) {
	var err error
	ctx, err = s.managementContext(ctx, identity, site)
	if err != nil {
		return SiteAccess{}, nil, 0, err
	}
	if err := ValidatePublishedAccess(published); err != nil {
		return SiteAccess{}, nil, http.StatusBadRequest, err
	}
	if err := s.validateSiteAccess(published); err != nil {
		return SiteAccess{}, nil, http.StatusBadRequest, err
	}
	published = CloneSiteAccess(published)
	published.Published = nil

	var saved SiteAccess
	status := 0
	err = s.config.Access.UpdateSiteAccess(ctx, site, func(existing SiteAccess, exists bool) (*SiteAccess, error) {
		if exists {
			existing = MigrateSiteAccess(existing)
		}
		if err := s.mayChangeAccess(ctx, identity, site, existing, exists); err != nil {
			status = http.StatusForbidden
			return nil, err
		}

		next := existing
		switch {
		case !exists || replace:
			next = s.withOwners(identity, published, existing, exists)
		case existing.Published != nil:
			merged, conflicts := mergeSiteAccess(*existing.Published, existing, published)
			if len(conflicts) > 0 {
				status = http.StatusConflict
				return nil, fmt.Errorf("the access section of hex.json conflicts with changes made to %s since its last publication (%s); make hex.json match hex access show %s, or publish with --replace-access to apply hex.json as it is", site, strings.Join(conflicts, ", "), site)
			}
			if err := s.validateSiteAccess(merged); err != nil {
				status = http.StatusConflict
				return nil, fmt.Errorf("merging the access section of hex.json into %s gives an invalid policy: %w; publish with --replace-access to apply hex.json as it is", site, err)
			}
			next = merged
		default:
			status = http.StatusConflict
			return nil, fmt.Errorf("%s has no recorded local permission baseline; reconcile explicitly with 'hex access reconcile %s --file <policy.json>' to preserve the live policy, or use --replace-access to replace it. Publishing without access leaves permissions unchanged", site, site)
		}
		next.Published = nil
		if err := s.keepsCallerOwner(ctx, identity, site, next); err != nil {
			status = http.StatusBadRequest
			return nil, err
		}

		saved = next
		stored := next
		stored.Published = &published
		return &stored, nil
	})
	if err != nil {
		return SiteAccess{}, nil, status, err
	}
	return saved, accessDrift(published, saved), 0, nil
}

// mergeSiteAccess applies the changes from base to next on top of live. It
// returns the merged policy and the parts changed differently on both sides.
func mergeSiteAccess(base, live, next SiteAccess) (SiteAccess, []string) {
	if next.Version == 2 {
		return mergePermissionPolicy(base, live, next)
	}
	if len(next.Owners) == 0 {
		// A section without owners keeps whoever owns the site.
		next.Owners = base.Owners
	}

	var merged SiteAccess
	var conflicts []string
	lists := []struct {
		name               string
		target             *[]string
		base, live, next   []string
		emptyMeansEveryone bool
	}{
		{"owners", &merged.Owners, base.Owners, live.Owners, next.Owners, false},
		{"editors", &merged.Editors, base.Editors, live.Editors, next.Editors, true},
		{"viewers", &merged.Viewers, base.Viewers, live.Viewers, next.Viewers, true},
	}
	for _, list := range lists {
		principals, ok := mergePrincipals(list.base, list.live, list.next, list.emptyMeansEveryone)
		if !ok {
			conflicts = append(conflicts, list.name)
		}
		*list.target = principals
	}

	paths, pathConflicts := mergePaths(base.Paths, live.Paths, next.Paths)
	merged.Paths = paths
	conflicts = append(conflicts, pathConflicts...)

	rules := []struct {
		kind             string
		target           *map[string]DataRule
		base, live, next map[string]DataRule
	}{
		{"collection", &merged.Collections, base.Collections, live.Collections, next.Collections},
		{"file prefix", &merged.Files, base.Files, live.Files, next.Files},
		{"channel", &merged.Channels, base.Channels, live.Channels, next.Channels},
	}
	for _, rule := range rules {
		mergedRules, ruleConflicts := mergeRules(rule.kind, rule.base, rule.live, rule.next)
		*rule.target = mergedRules
		conflicts = append(conflicts, ruleConflicts...)
	}

	return NormalizeSiteAccess(merged), conflicts
}

// mergePrincipals removes from live what next removed from base and adds
// what next added. Where an empty list lets everyone in, opening or
// restricting a list on both sides cannot be combined. A merge that would
// leave the list empty is a conflict too.
func mergePrincipals(base, live, next []string, emptyMeansEveryone bool) ([]string, bool) {
	if sameMembers(next, base) || sameMembers(live, next) {
		return slices.Clone(live), true
	}
	if sameMembers(live, base) {
		return slices.Clone(next), true
	}
	if emptyMeansEveryone && (len(base) == 0 || len(live) == 0 || len(next) == 0) {
		return slices.Clone(live), false
	}

	var merged []string
	for _, principal := range live {
		removed := slices.Contains(base, principal) && !slices.Contains(next, principal)
		if !removed {
			merged = appendUnique(merged, principal)
		}
	}
	for _, principal := range next {
		if !slices.Contains(base, principal) {
			merged = appendUnique(merged, principal)
		}
	}
	if len(merged) == 0 {
		return slices.Clone(live), false
	}
	return merged, true
}

// mergePaths merges path rules by prefix, keeping the live order and adding
// new prefixes in the order of next.
func mergePaths(base, live, next []PathRule) ([]PathRule, []string) {
	baseRules := pathRulesByPrefix(base)
	liveRules := pathRulesByPrefix(live)
	nextRules := pathRulesByPrefix(next)

	var prefixes []string
	for _, rule := range slices.Concat(live, next, base) {
		if !slices.Contains(prefixes, rule.Prefix) {
			prefixes = append(prefixes, rule.Prefix)
		}
	}

	var merged []PathRule
	var conflicts []string
	for _, prefix := range prefixes {
		viewers, ok := mergeEntry(lookup(baseRules, prefix), lookup(liveRules, prefix), lookup(nextRules, prefix), sameAudience)
		if !ok {
			conflicts = append(conflicts, "path "+prefix)
			viewers = lookup(liveRules, prefix)
		}
		if viewers.present {
			merged = append(merged, PathRule{Prefix: prefix, Viewers: viewers.value})
		}
	}
	return merged, conflicts
}

func pathRulesByPrefix(rules []PathRule) map[string]Audience {
	byPrefix := make(map[string]Audience, len(rules))
	for _, rule := range rules {
		byPrefix[rule.Prefix] = rule.Viewers
	}
	return byPrefix
}

// mergeRules merges collection, file prefix or channel rules by name.
func mergeRules(kind string, base, live, next map[string]DataRule) (map[string]DataRule, []string) {
	names := slices.Sorted(maps.Keys(base))
	names = append(names, slices.Sorted(maps.Keys(live))...)
	names = append(names, slices.Sorted(maps.Keys(next))...)
	slices.Sort(names)
	names = slices.Compact(names)

	merged := map[string]DataRule{}
	var conflicts []string
	for _, name := range names {
		rule, ok := mergeEntry(lookup(base, name), lookup(live, name), lookup(next, name), sameRule)
		if !ok {
			conflicts = append(conflicts, kind+" "+name)
			rule = lookup(live, name)
		}
		if rule.present {
			merged[name] = rule.value
		}
	}
	if len(merged) == 0 {
		return nil, conflicts
	}
	return merged, conflicts
}

// entry is a rule that may be absent from a policy.
type entry[V any] struct {
	value   V
	present bool
}

func lookup[V any](values map[string]V, key string) entry[V] {
	value, present := values[key]
	return entry[V]{value: value, present: present}
}

// mergeEntry keeps live when next did not change base, takes next when live
// did not, and reports a conflict when both changed it differently.
func mergeEntry[V any](base, live, next entry[V], equal func(V, V) bool) (entry[V], bool) {
	switch {
	case sameEntry(next, base, equal):
		return live, true
	case sameEntry(live, base, equal), sameEntry(live, next, equal):
		return next, true
	default:
		return entry[V]{}, false
	}
}

func sameMembers(a, b []string) bool {
	for _, value := range a {
		if !slices.Contains(b, value) {
			return false
		}
	}
	for _, value := range b {
		if !slices.Contains(a, value) {
			return false
		}
	}
	return true
}

func sameAudience(a, b Audience) bool {
	return a.Level == b.Level && sameMembers(a.Principals, b.Principals)
}

func sameRule(a, b DataRule) bool {
	return sameAudience(a.Read, b.Read) && sameAudience(a.Write, b.Write)
}

// accessDrift describes how the live policy differs from the published
// access section, comparing people by the role they effectively have.
func accessDrift(published, live SiteAccess) []string {
	if published.Version == 2 {
		return permissionDrift(published, live)
	}
	if len(published.Owners) == 0 {
		published.Owners = live.Owners
	}

	var drift []string
	publishedGeneral, publishedOrder, publishedRoles := decodeSharing(published, true)
	liveGeneral, liveOrder, liveRoles := decodeSharing(live, true)
	if publishedGeneral != liveGeneral {
		drift = append(drift, fmt.Sprintf("general access: %s in hex.json, %s on the site", generalDescription(publishedGeneral), generalDescription(liveGeneral)))
	}
	var principals []string
	for _, principal := range slices.Concat(publishedOrder, liveOrder) {
		principals = appendUnique(principals, principal)
	}
	for _, principal := range principals {
		publishedRole := publishedRoles[principal]
		liveRole := liveRoles[principal]
		if publishedRole != liveRole {
			drift = append(drift, fmt.Sprintf("%s: %s in hex.json, %s on the site", principal, roleDescription(publishedRole), roleDescription(liveRole)))
		}
	}

	publishedPaths := pathRulesByPrefix(published.Paths)
	livePaths := pathRulesByPrefix(live.Paths)
	for _, prefix := range sortedKeys(publishedPaths, livePaths) {
		if !sameEntry(lookup(publishedPaths, prefix), lookup(livePaths, prefix), sameAudience) {
			drift = append(drift, ruleDrift("path "+prefix, lookup(publishedPaths, prefix).present, lookup(livePaths, prefix).present))
		}
	}

	rules := []struct {
		kind            string
		published, live map[string]DataRule
	}{
		{"collection", published.Collections, live.Collections},
		{"file prefix", published.Files, live.Files},
		{"channel", published.Channels, live.Channels},
	}
	for _, rule := range rules {
		for _, name := range sortedKeys(rule.published, rule.live) {
			if !sameEntry(lookup(rule.published, name), lookup(rule.live, name), sameRule) {
				drift = append(drift, ruleDrift(rule.kind+" "+name, lookup(rule.published, name).present, lookup(rule.live, name).present))
			}
		}
	}
	return drift
}

func sameEntry[V any](a, b entry[V], equal func(V, V) bool) bool {
	if a.present != b.present {
		return false
	}
	return !a.present || equal(a.value, b.value)
}

func sortedKeys[V any](values ...map[string]V) []string {
	var keys []string
	for _, value := range values {
		keys = append(keys, slices.Sorted(maps.Keys(value))...)
	}
	slices.Sort(keys)
	return slices.Compact(keys)
}

func ruleDrift(label string, inPublished, onSite bool) string {
	switch {
	case !onSite:
		return label + ": in hex.json, not on the site"
	case !inPublished:
		return label + ": on the site, not in hex.json"
	default:
		return label + ": differs between hex.json and the site"
	}
}

func generalDescription(general string) string {
	switch general {
	case GeneralView:
		return "everyone can view"
	case GeneralEdit:
		return "everyone can view and edit"
	default:
		return "only people listed"
	}
}

func roleDescription(role string) string {
	if role == "" {
		return "not listed"
	}
	return role
}

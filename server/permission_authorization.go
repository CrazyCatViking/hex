package hex

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
)

var ErrAuthorizationUnsupported = errors.New("database provider does not support guarded mutations or authorization filtering")

// GuardedDatabase executes the callback and mutation under the same lock or
// transaction, including absent IDs. Nil data deletes; an error changes nothing.
// Callbacks must not reenter the database. Every writer must use the same lock.
type GuardedDatabase interface {
	Mutate(context.Context, string, string, string, string, DocumentMutation) (Document, error)
	ListMatching(context.Context, string, string, ListOptions, *RecordCondition) ([]Document, error)
}

type DocumentMutation func(current *Document) (json.RawMessage, error)

func (a siteAuthorization) applicationAdmitted() bool {
	if a.role == roleNone {
		return false
	}
	if a.access.Version != 2 || a.apiOnly || a.identity != nil && (a.identity.Provider == "automation" || a.identity.Provider == "site") {
		return true
	}
	return a.subjectsMatch(a.access.Admission)
}

func (a siteAuthorization) subjectsMatch(subjects Subjects) bool {
	human := a.identity == nil || (a.identity.Provider != "automation" && a.identity.Provider != "site")
	if subjects.Owners && a.role == roleOwner || subjects.SignedIn && a.role != roleNone && human || a.identity.matchesAny(subjects.Principals) {
		return true
	}
	for _, name := range subjects.Roles {
		if a.access.Roles[name].SignedIn && a.role != roleNone && human {
			return true
		}
	}
	for _, binding := range a.access.Bindings {
		if binding.Scope == nil && slices.Contains(subjects.Roles, binding.Role) && a.identity.matchesAny([]string{binding.Principal}) {
			return true
		}
	}
	return false
}

func (a siteAuthorization) applicationRoles() []string {
	var roles []string
	for name, role := range a.access.Roles {
		if role.SignedIn && a.role != roleNone && (a.identity == nil || (a.identity.Provider != "automation" && a.identity.Provider != "site")) {
			roles = appendUnique(roles, name)
		}
	}
	for _, binding := range a.access.Bindings {
		if a.identity.matchesAny([]string{binding.Principal}) {
			roles = appendUnique(roles, binding.Role)
		}
	}
	slices.Sort(roles)
	return roles
}

func permissionSelectorMatches(kind, selector, resource string) bool {
	if selector == "*" {
		return true
	}
	switch kind {
	case "path":
		return strings.HasPrefix(resource, selector) || resource+"/" == selector
	case "file":
		return keyHasPrefix(resource, selector)
	case "integration":
		return resource == selector || strings.HasSuffix(selector, ".*") && strings.HasPrefix(resource, strings.TrimSuffix(selector, "*"))
	default:
		return selector == resource
	}
}

func (a siteAuthorization) matchingGrants(kind, resource, operation string) []PermissionGrant {
	if !a.applicationAdmitted() {
		return nil
	}
	var grants []PermissionGrant
	for _, grant := range a.access.Grants {
		if grant.Kind != kind || !slices.Contains(grant.Operations, operation) || !permissionSelectorMatches(kind, grant.Resource, resource) {
			continue
		}
		excluded := false
		for _, selector := range grant.Except {
			if permissionSelectorMatches(kind, selector, resource) {
				excluded = true
				break
			}
		}
		if !excluded {
			grants = append(grants, grant)
		}
	}
	return grants
}

func (a siteAuthorization) grantCondition(grant PermissionGrant) (*RecordCondition, bool) {
	condition := resolveRecordCondition(grant.Where, a.identity)
	if grant.Scope == nil {
		return condition, a.subjectsMatch(grant.Subjects)
	}
	for _, role := range grant.Subjects.Roles {
		if a.access.Roles[role].SignedIn && a.identity != nil && a.identity.Provider != "automation" {
			return condition, true
		}
	}
	var scopeIDs []string
	for _, binding := range a.access.Bindings {
		if !slices.Contains(grant.Subjects.Roles, binding.Role) || !a.identity.matchesAny([]string{binding.Principal}) {
			continue
		}
		if binding.Scope == nil {
			return condition, true
		}
		if binding.Scope.Kind == grant.Scope.Kind {
			scopeIDs = appendUnique(scopeIDs, binding.Scope.ID)
		}
	}
	if len(scopeIDs) == 0 {
		return nil, false
	}
	value, _ := json.Marshal(scopeIDs)
	scope := &RecordCondition{Field: grant.Scope.Field, Op: "in", Value: value}
	return andConditions(condition, scope), true
}

func (a siteAuthorization) permits(kind, resource, operation string) bool {
	for _, grant := range a.matchingGrants(kind, resource, operation) {
		if _, matches := a.grantCondition(grant); matches {
			return true
		}
	}
	return false
}

func (a siteAuthorization) operationGrant(kind, resource, operation string) grant {
	result := grantNone
	for _, rule := range a.matchingGrants(kind, resource, operation) {
		condition, matches := a.grantCondition(rule)
		if !matches {
			continue
		}
		if condition == nil && (operation == "read" || rule.Check == nil && rule.Fields == nil) {
			return grantAll
		}
		if result != grantConditional && condition != nil && condition.Field == "createdBy" && condition.Op == "eq" && string(condition.Value) == quotedIdentity(a.identity) && rule.Check == nil && rule.Fields == nil {
			result = grantOwn
		} else {
			result = grantConditional
		}
	}
	return result
}

func quotedIdentity(identity *Identity) string {
	if identity == nil {
		return ""
	}
	value, _ := json.Marshal(identity.ID)
	return string(value)
}

func (a siteAuthorization) readFilter(collection string) (*RecordCondition, bool) {
	var any []RecordCondition
	for _, grant := range a.matchingGrants("collection", collection, "read") {
		condition, matches := a.grantCondition(grant)
		if !matches {
			continue
		}
		if condition == nil {
			return nil, true
		}
		any = append(any, *condition)
	}
	if len(any) == 0 {
		return nil, false
	}
	return &RecordCondition{Any: any}, true
}

func (a siteAuthorization) canReadDocument(collection string, document Document) bool {
	filter, allowed := a.readFilter(collection)
	return allowed && filter.Matches(document)
}

func (a siteAuthorization) canMutate(collection, operation string, before *Document, after Document) bool {
	return a.canChangeRecord("collection", collection, operation, before, after)
}

func (a siteAuthorization) canChangeRecord(kind, resource, operation string, before *Document, after Document) bool {
	for _, grant := range a.matchingGrants(kind, resource, operation) {
		condition, matches := a.grantCondition(grant)
		if !matches {
			continue
		}
		old := after
		if before != nil {
			old = *before
		}
		if !condition.Matches(old) || !resolveRecordCondition(grant.Check, a.identity).Matches(after) {
			continue
		}
		// Scoped records may not move out of the caller's authorized scopes.
		if grant.Scope != nil && operation != "delete" {
			scopeGrant := grant
			scopeGrant.Where = nil
			scope, _ := a.grantCondition(scopeGrant)
			if !scope.Matches(after) {
				continue
			}
		}
		var previous json.RawMessage
		if before != nil {
			previous = before.Data
		}
		if operation == "delete" || changedFieldsAllowed(previous, after.Data, grant.Fields) {
			return true
		}
	}
	return false
}

func (s *Server) listAuthorizedDocuments(ctx context.Context, a siteAuthorization, site, collection string, options ListOptions) ([]Document, error) {
	filter, allowed := a.readFilter(collection)
	if !allowed {
		return nil, ErrForbidden
	}
	if filter == nil {
		return s.config.Database.List(ctx, site, collection, options)
	}
	database, supported := s.config.Database.(GuardedDatabase)
	if !supported {
		return nil, ErrAuthorizationUnsupported
	}
	return database.ListMatching(ctx, site, collection, options, filter)
}

func (s *Server) mutateAuthorizedDocument(ctx context.Context, a siteAuthorization, site, collection, id string, data json.RawMessage, deleteDocument bool) (Document, error) {
	database, supported := s.config.Database.(GuardedDatabase)
	if !supported {
		return Document{}, ErrAuthorizationUnsupported
	}
	return database.Mutate(ctx, site, collection, id, a.creator(), func(before *Document) (json.RawMessage, error) {
		operation := "create"
		after := Document{ID: id, Data: data, CreatedBy: a.creator()}
		if before != nil {
			operation = "update"
			after.CreatedBy = before.CreatedBy
		}
		if deleteDocument {
			operation = "delete"
			if before == nil {
				return nil, ErrNotFound
			}
			after = *before
		}
		if !a.canMutate(collection, operation, before, after) {
			return nil, ErrForbidden
		}
		return data, nil
	})
}

package hex

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
)

// Subjects selects callers, not records. Owners is explicit: site management
// authority alone never bypasses a version 2 application's data permissions.
type Subjects struct {
	SignedIn   bool     `json:"signedIn,omitempty"`
	Owners     bool     `json:"owners,omitempty"`
	Principals []string `json:"principals,omitempty"`
	Roles      []string `json:"roles,omitempty"`
}

type ApplicationRole struct {
	Description string `json:"description,omitempty"`
	SignedIn    bool   `json:"signedIn,omitempty"`
}

type RoleScope struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
}

// A binding has a stable map key and one principal, so a concurrent revocation
// and role/scope change conflict instead of resurrecting access through a union.
type RoleBinding struct {
	Principal string     `json:"principal"`
	Role      string     `json:"role"`
	Scope     *RoleScope `json:"scope,omitempty"`
}

type PermissionScope struct {
	Kind  string `json:"kind"`
	Field string `json:"field"`
}

// PermissionGrant authorizes a complete operation. Except makes fallback
// selectors explicit, including lossless migration of legacy wildcard rules.
type PermissionGrant struct {
	Kind       string           `json:"kind"`
	Resource   string           `json:"resource"`
	Except     []string         `json:"except,omitempty"`
	Operations []string         `json:"operations"`
	Subjects   Subjects         `json:"subjects"`
	Scope      *PermissionScope `json:"scope,omitempty"`
	Where      *RecordCondition `json:"where,omitempty"`
	Check      *RecordCondition `json:"check,omitempty"`
	Fields     []string         `json:"fields,omitzero"`
}

const legacyPolicyError = "legacy permission configuration: Hex requires access.version 2 with application roles and explicit operation grants; the deployed site is unchanged. Run 'hex access show <site>', copy the migrated policy into hex.json, review it, and publish again. Changing only the version number is not sufficient"

// ValidatePublishedAccess is also used by the CLI before making a publication.
func ValidatePublishedAccess(access SiteAccess) error {
	if access.Version != 2 || hasLegacyAccess(access) {
		return errors.New(legacyPolicyError)
	}
	return validatePermissionPolicy(access)
}

func hasLegacyAccess(access SiteAccess) bool {
	return access.legacyFields || len(access.Editors) > 0 || len(access.Viewers) > 0 || len(access.Paths) > 0 || len(access.Collections) > 0 || len(access.Files) > 0 || len(access.Channels) > 0
}

func validatePermissionPolicy(access SiteAccess) error {
	if access.Version != 2 || hasLegacyAccess(access) {
		return errors.New(legacyPolicyError)
	}
	if err := validatePermissionPrincipals("owners", access.Owners); err != nil {
		return err
	}
	if len(access.Roles) > 64 || len(access.Bindings) > 1024 || len(access.Grants) > 2048 {
		return errors.New("at most 64 roles, 1024 bindings and 2048 grants are allowed")
	}
	for name := range access.Roles {
		if !validCollectionName(name) {
			return fmt.Errorf("invalid application role %q", name)
		}
	}
	if err := validateSubjects(access.Admission, access.Roles); err != nil {
		return fmt.Errorf("admission: %w", err)
	}
	seen := map[string]bool{}
	for id, binding := range access.Bindings {
		if !validCollectionName(id) || !principalPattern.MatchString(binding.Principal) {
			return fmt.Errorf("invalid binding %q: use a stable ID and a typed principal", id)
		}
		if _, ok := access.Roles[binding.Role]; !ok {
			return fmt.Errorf("binding %s references unknown role %q", id, binding.Role)
		}
		if binding.Scope != nil && (!validCollectionName(binding.Scope.Kind) || !identityIDPattern.MatchString(binding.Scope.ID)) {
			return fmt.Errorf("binding %s has an invalid scope", id)
		}
		encoded, err := json.Marshal(binding)
		if err != nil {
			return err
		}
		if seen[string(encoded)] {
			return fmt.Errorf("duplicate role assignment in binding %s", id)
		}
		seen[string(encoded)] = true
	}
	for _, id := range slices.Sorted(maps.Keys(access.Grants)) {
		grant := access.Grants[id]
		if !validCollectionName(id) {
			return fmt.Errorf("invalid grant ID %q", id)
		}
		if err := validatePermissionGrant(grant, access.Roles); err != nil {
			return fmt.Errorf("grant %s: %w", id, err)
		}
	}
	return nil
}

func validateSubjects(subjects Subjects, roles map[string]ApplicationRole) error {
	if err := validatePermissionPrincipals("subjects", subjects.Principals); err != nil {
		return err
	}
	if len(subjects.Roles) > 64 {
		return errors.New("at most 64 subject roles are allowed")
	}
	for _, role := range subjects.Roles {
		if _, ok := roles[role]; !ok {
			return fmt.Errorf("unknown application role %q", role)
		}
	}
	return nil
}

func validatePermissionPrincipals(label string, principals []string) error {
	// Legacy untyped principals expand into three typed claim selectors.
	if len(principals) > 256 {
		return fmt.Errorf("at most 256 %s are allowed", label)
	}
	for _, principal := range principals {
		if !principalPattern.MatchString(principal) {
			return fmt.Errorf("invalid principal %q in %s; use user:<id>, group:<id>, role:<value> or automation:<name>", principal, label)
		}
	}
	return nil
}

func validPermissionResource(kind, resource string) bool {
	if resource == "*" {
		return true
	}
	switch kind {
	case "collection", "channel", "action":
		return validCollectionName(resource)
	case "integration":
		integration, endpoint, ok := strings.Cut(resource, ".")
		return ok && validCollectionName(integration) && (endpoint == "*" || validCollectionName(endpoint))
	case "file":
		return validFilePrefix(resource)
	case "path":
		return pathPrefixPattern.MatchString(resource) && !strings.Contains(resource, "..") && !strings.Contains(resource, "//")
	}
	return false
}

func validatePermissionGrant(grant PermissionGrant, roles map[string]ApplicationRole) error {
	operations := map[string][]string{
		"collection": {"read", "create", "update", "delete"},
		"file":       {"read", "upload", "delete"}, "channel": {"read", "send"},
		"path": {"read"}, "action": {"run"},
		"integration": {"read", "write"},
	}
	allowed, ok := operations[grant.Kind]
	if !ok || !validPermissionResource(grant.Kind, grant.Resource) || len(grant.Operations) == 0 || len(grant.Operations) > 4 {
		return errors.New("invalid resource kind, selector or operations")
	}
	for _, operation := range grant.Operations {
		if !slices.Contains(allowed, operation) {
			return fmt.Errorf("operation %q is not valid for %s", operation, grant.Kind)
		}
	}
	if len(grant.Except) > 128 || len(grant.Fields) > 64 {
		return errors.New("too many excluded resources or writable fields")
	}
	for _, resource := range grant.Except {
		if !validPermissionResource(grant.Kind, resource) {
			return fmt.Errorf("invalid excluded resource %q", resource)
		}
	}
	if err := validateSubjects(grant.Subjects, roles); err != nil {
		return err
	}
	if grant.Kind != "collection" && grant.Kind != "action" && (grant.Where != nil || grant.Check != nil || grant.Scope != nil || grant.Fields != nil) {
		return errors.New("record conditions, scopes and writable fields are only available for collections and actions")
	}
	if grant.Scope != nil {
		if !validCollectionName(grant.Scope.Kind) || !validConditionField(grant.Scope.Field) || len(grant.Subjects.Roles) == 0 || grant.Subjects.SignedIn || grant.Subjects.Owners || len(grant.Subjects.Principals) > 0 {
			return errors.New("a scoped grant needs only application-role subjects, a scope kind and a record field")
		}
	}
	for _, field := range grant.Fields {
		if !validCollectionName(field) {
			return fmt.Errorf("invalid writable top-level field %q", field)
		}
	}
	for _, condition := range []*RecordCondition{grant.Where, grant.Check} {
		if err := validateRecordCondition(condition); err != nil {
			return err
		}
	}
	return nil
}

func stablePermissionID(parts ...string) string {
	digest := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return fmt.Sprintf("m-%x", digest[:16])
}

// MigrateSiteAccess translates both live state and the last local publication.
// The published snapshot is deliberately not replaced with the merged policy.
func MigrateSiteAccess(old SiteAccess) SiteAccess {
	if old.Version != 0 {
		return CloneSiteAccess(old)
	}
	old = CloneSiteAccess(old)
	old.Owners = typedLegacyPrincipals(old.Owners)
	old.Editors = typedLegacyPrincipals(old.Editors)
	old.Viewers = typedLegacyPrincipals(old.Viewers)
	for i := range old.Paths {
		old.Paths[i].Viewers.Principals = typedLegacyPrincipals(old.Paths[i].Viewers.Principals)
	}
	for _, rules := range []map[string]DataRule{old.Collections, old.Files, old.Channels} {
		for name, rule := range rules {
			rule.Read.Principals = typedLegacyPrincipals(rule.Read.Principals)
			rule.Write.Principals = typedLegacyPrincipals(rule.Write.Principals)
			rules[name] = rule
		}
	}
	access := SiteAccess{
		Version: 2, Owners: slices.Clone(old.Owners),
		Roles: map[string]ApplicationRole{
			"legacy-editor": {Description: "Migrated default data editor"},
			"legacy-viewer": {Description: "Migrated site member", SignedIn: len(old.Viewers) == 0},
		},
		Bindings: map[string]RoleBinding{}, Grants: map[string]PermissionGrant{},
	}
	viewerPrincipals := slices.Clone(old.Viewers)
	viewerPrincipals = append(viewerPrincipals, old.Editors...)
	access.Admission = Subjects{SignedIn: len(old.Viewers) == 0, Owners: true, Roles: []string{"legacy-viewer"}}
	for _, principal := range viewerPrincipals {
		access.Bindings[stablePermissionID("viewer", principal)] = RoleBinding{Principal: principal, Role: "legacy-viewer"}
	}
	for _, principal := range old.Editors {
		access.Bindings[stablePermissionID("editor", principal)] = RoleBinding{Principal: principal, Role: "legacy-editor"}
	}
	if len(old.Editors) == 0 {
		if len(old.Viewers) == 0 {
			access.Roles["legacy-editor"] = ApplicationRole{Description: "Migrated default data editor", SignedIn: true}
		} else {
			for _, principal := range old.Viewers {
				access.Bindings[stablePermissionID("editor", principal)] = RoleBinding{Principal: principal, Role: "legacy-editor"}
			}
		}
	}
	viewers := Subjects{Roles: []string{"legacy-viewer"}}
	editors := Subjects{Roles: []string{"legacy-editor"}}
	audience := func(value Audience, fallback string) (Subjects, *RecordCondition) {
		if len(value.Principals) > 0 {
			return Subjects{Principals: slices.Clone(value.Principals)}, nil
		}
		level := value.Level
		if level == "" {
			level = fallback
		}
		switch level {
		case LevelViewers:
			return viewers, nil
		case LevelEditors:
			return editors, nil
		case LevelOwners:
			return Subjects{Owners: true}, nil
		case LevelCreator:
			return viewers, &RecordCondition{Field: "createdBy", Op: "eq", Value: json.RawMessage(`"$actor.id"`)}
		}
		return Subjects{}, nil
	}
	add := func(id, kind, resource string, except []string, operations []string, subjects Subjects, where *RecordCondition) {
		access.Grants[id] = PermissionGrant{Kind: kind, Resource: resource, Except: except, Operations: operations, Subjects: subjects, Where: where}
	}
	for _, kind := range []string{"collection", "file", "channel", "path", "action", "integration"} {
		operations := map[string][]string{"collection": {"read", "create", "update", "delete"}, "file": {"read", "upload", "delete"}, "channel": {"read", "send"}, "path": {"read"}, "action": {"run"}, "integration": {"read", "write"}}[kind]
		add(stablePermissionID("owners", kind), kind, "*", nil, operations, Subjects{Owners: true}, nil)
	}
	for kind, rules := range map[string]map[string]DataRule{"collection": old.Collections, "file": old.Files, "channel": old.Channels} {
		rules = maps.Clone(rules)
		if rules == nil {
			rules = map[string]DataRule{}
		}
		if _, exists := rules["*"]; !exists {
			rules["*"] = DataRule{}
		}
		for name, rule := range rules {
			var except []string
			for candidate := range rules {
				if candidate != "*" && candidate != name && (name == "*" || kind == "file" && keyHasPrefix(candidate, name)) {
					except = append(except, candidate)
				}
			}
			slices.Sort(except)
			for _, operation := range []string{"read", "write"} {
				value, fallback, ops := rule.Read, LevelViewers, []string{"read"}
				if operation == "write" {
					value, fallback = rule.Write, LevelEditors
					ops = map[string][]string{"collection": {"create", "update", "delete"}, "file": {"upload", "delete"}, "channel": {"send"}}[kind]
				}
				subjects, where := audience(value, fallback)
				add(stablePermissionID(kind, name, operation), kind, name, except, ops, subjects, where)
			}
		}
	}
	paths := append([]PathRule{{Prefix: "*", Viewers: Audience{Level: LevelViewers}}}, old.Paths...)
	for _, rule := range paths {
		var except []string
		for _, other := range old.Paths {
			if other.Prefix != rule.Prefix && (rule.Prefix == "*" || strings.HasPrefix(other.Prefix, rule.Prefix)) {
				except = append(except, other.Prefix)
			}
		}
		slices.Sort(except)
		subjects, _ := audience(rule.Viewers, LevelViewers)
		add(stablePermissionID("path", rule.Prefix), "path", rule.Prefix, except, []string{"read"}, subjects, nil)
	}
	add(stablePermissionID("actions"), "action", "*", nil, []string{"run"}, editors, nil)
	add(stablePermissionID("integrations-read"), "integration", "*", nil, []string{"read"}, viewers, nil)
	add(stablePermissionID("integrations-write"), "integration", "*", nil, []string{"write"}, editors, nil)
	// Scheduled automations previously had owner data authority. Preserve it
	// explicitly, without granting authority to newly created automations.
	// Runtime migration supplies the existing automation names separately.
	if old.Published != nil {
		published := MigrateSiteAccess(*old.Published)
		access.Published = &published
	}
	return access
}

func typedLegacyPrincipals(principals []string) []string {
	var result []string
	for _, principal := range principals {
		if kind, _, typed := splitPrincipal(principal); typed && kind != "automation" {
			result = appendUnique(result, principal)
			continue
		}
		for _, kind := range []string{"user:", "group:", "role:"} {
			result = appendUnique(result, kind+principal)
		}
	}
	return result
}

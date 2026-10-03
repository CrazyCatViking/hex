package hex

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

const (
	MaxIntegrationInputBytes  = 64 << 10
	MaxIntegrationOutputBytes = 128 << 10
	MaxBundleTools            = 16
)

// ToolLimits bound a single invocation. Output records count all array elements
// recursively (at least one unit), so nesting cannot evade extraction budgets.
type ToolLimits struct {
	InputBytes     int   `json:"inputBytes"`
	OutputBytes    int   `json:"outputBytes"`
	Records        int64 `json:"records"`
	TimeoutSeconds int   `json:"timeoutSeconds"`
}

type IntegrationToolDefinition struct {
	ActionDefinition
	Integration string     `json:"integration"`
	Version     string     `json:"version"`
	ReadOnly    bool       `json:"readOnly"`
	Limits      ToolLimits `json:"limits"`
	Resources   []string   `json:"resources,omitempty"`
}

// ResourceGrant is a literal resource identifier and the principals allowed to
// query it. Neither site ownership nor platform administration bypasses grants.
type ResourceGrant struct {
	Resource   string
	Principals []string
}

type IntegrationTool struct {
	Definition    IntegrationToolDefinition
	Principals    []string
	ResourceField string
	Resources     []ResourceGrant
	Handler       func(context.Context, IntegrationContext, json.RawMessage) (json.RawMessage, error)
}

type IntegrationContext struct {
	Identity Identity
	Bundle   string
	Resource string
	Limits   ToolLimits
}

type ToolBundleDefinition struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Tools       int    `json:"tools"`
}

type ToolBundle struct {
	Name        string
	Description string
	Principals  []string
	Tools       []string
	// RequiredScopes can narrow a bundle for separately issued agent tokens.
	// Claims must come from the trusted identity resolver, never MCP metadata.
	RequiredScopes []string
}

type registeredIntegrationTool struct {
	tool    IntegrationTool
	input   *jsonschema.Schema
	output  *jsonschema.Schema
	enabled bool
}

// IntegrationRegistry is a server-owned, explicitly granted catalogue. Tool
// implementations are trusted deployment code, never uploaded by site publishers.
// The zero value is usable. Registration copies contracts and policy slices.
type IntegrationRegistry struct {
	mu              sync.RWMutex
	tools           map[string]*registeredIntegrationTool
	bundles         map[string]ToolBundle
	disabledBundles map[string]bool
}

func integrationPrincipals(principals []string) error {
	if len(principals) == 0 {
		return errors.New("integration access requires explicit principals")
	}
	return validatePrincipals("integration principals", principals)
}

func (r *IntegrationRegistry) RegisterTool(tool IntegrationTool) error {
	d := &tool.Definition
	if !namePattern.MatchString(d.Name) || !namePattern.MatchString(d.Integration) || d.Version == "" || len(d.Version) > 64 || strings.TrimSpace(d.Description) == "" || len(d.Description) > 500 || tool.Handler == nil {
		return errors.New("a tool requires valid names, version, description and handler")
	}
	if !d.ReadOnly {
		return errors.New("curated integration tools currently support read-only operations only")
	}
	if err := integrationPrincipals(tool.Principals); err != nil {
		return err
	}
	if d.Limits.InputBytes == 0 {
		d.Limits.InputBytes = 16 << 10
	}
	if d.Limits.OutputBytes == 0 {
		d.Limits.OutputBytes = 16 << 10
	}
	if d.Limits.Records == 0 {
		d.Limits.Records = 20
	}
	if d.Limits.TimeoutSeconds == 0 {
		d.Limits.TimeoutSeconds = 10
	}
	if d.Limits.InputBytes < 1 || d.Limits.InputBytes > MaxIntegrationInputBytes || d.Limits.OutputBytes < 1 || d.Limits.OutputBytes > MaxIntegrationOutputBytes || d.Limits.Records < 1 || d.Limits.Records > 1000 || d.Limits.TimeoutSeconds < 1 || d.Limits.TimeoutSeconds > 60 {
		return errors.New("tool limits exceed platform bounds")
	}
	for _, schema := range []json.RawMessage{d.InputSchema, d.OutputSchema} {
		if len(schema) > 16<<10 {
			return errors.New("integration schemas must be at most 16 KiB")
		}
		var root map[string]any
		if err := json.Unmarshal(schema, &root); err != nil {
			return err
		}
		if root["type"] != "object" || root["additionalProperties"] != false {
			return errors.New("tool schemas must describe closed objects with additionalProperties:false")
		}
		if err := closedIntegrationSchema(root); err != nil {
			return err
		}
	}
	input, err := compileActionSchema(d.InputSchema)
	if err != nil {
		return fmt.Errorf("tool input schema: %w", err)
	}
	output, err := compileActionSchema(d.OutputSchema)
	if err != nil {
		return fmt.Errorf("tool output schema: %w", err)
	}
	if tool.ResourceField != "" {
		if len(tool.Resources) > 128 {
			return errors.New("tool resource grants exceed 128 entries")
		}
		if !namePattern.MatchString(tool.ResourceField) || len(tool.Resources) == 0 {
			return errors.New("resource-scoped tools require a valid field and resource grants")
		}
		var root struct {
			Properties map[string]struct {
				Type string `json:"type"`
			} `json:"properties"`
			Required []string `json:"required"`
		}
		if err := json.Unmarshal(d.InputSchema, &root); err != nil {
			return err
		}
		if root.Properties[tool.ResourceField].Type != "string" || !slices.Contains(root.Required, tool.ResourceField) {
			return errors.New("resource field must be a required string property")
		}
	} else if len(tool.Resources) != 0 {
		return errors.New("resource grants require a resource field")
	}
	for _, grant := range tool.Resources {
		if grant.Resource == "" || len(grant.Resource) > 128 {
			return errors.New("resource identifiers must be 1–128 bytes")
		}
		if err := integrationPrincipals(grant.Principals); err != nil {
			return err
		}
	}
	d.InputSchema = slices.Clone(d.InputSchema)
	d.OutputSchema = slices.Clone(d.OutputSchema)
	d.Resources = nil // This is derived per caller, never supplied by registration.
	tool.Principals = slices.Clone(tool.Principals)
	tool.Resources = slices.Clone(tool.Resources)
	for index := range tool.Resources {
		tool.Resources[index].Principals = slices.Clone(tool.Resources[index].Principals)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.tools == nil {
		r.tools = make(map[string]*registeredIntegrationTool)
	}
	if _, exists := r.tools[d.Name]; exists {
		return errors.New("integration tool already registered")
	}
	if len(r.tools) >= 256 {
		return errors.New("integration catalogue exceeds 256 tools")
	}
	r.tools[d.Name] = &registeredIntegrationTool{tool: tool, input: input, output: output, enabled: true}
	return nil
}

func closedIntegrationSchema(schema map[string]any) error {
	object := schema["type"] == "object" || schema["properties"] != nil
	array := schema["type"] == "array"
	if types, ok := schema["type"].([]any); ok {
		for _, value := range types {
			object = object || value == "object"
			array = array || value == "array"
		}
	}
	if schema["type"] == nil && !object && schema["$ref"] == nil && schema["enum"] == nil && schema["const"] == nil && schema["allOf"] == nil && schema["anyOf"] == nil && schema["oneOf"] == nil {
		return errors.New("tool schemas cannot expose unconstrained values")
	}
	if array && schema["items"] == nil && schema["prefixItems"] == nil {
		return errors.New("tool arrays must describe their item shape")
	}
	if object && schema["additionalProperties"] != false {
		return errors.New("nested tool objects must reject additional properties")
	}
	for _, key := range []string{"properties", "$defs", "definitions"} {
		if definitions, ok := schema[key].(map[string]any); ok {
			for _, value := range definitions {
				if value == true {
					return errors.New("tool schemas cannot expose unconstrained properties")
				}
				if child, ok := value.(map[string]any); ok {
					if err := closedIntegrationSchema(child); err != nil {
						return err
					}
				}
			}
		}
	}
	for _, key := range []string{"items", "if", "then", "else", "not"} {
		if schema[key] == true {
			return errors.New("tool schemas cannot expose unconstrained items")
		}
		if child, ok := schema[key].(map[string]any); ok {
			if err := closedIntegrationSchema(child); err != nil {
				return err
			}
		}
	}
	for _, key := range []string{"allOf", "anyOf", "oneOf", "prefixItems"} {
		if children, ok := schema[key].([]any); ok {
			for _, value := range children {
				if child, ok := value.(map[string]any); ok {
					if err := closedIntegrationSchema(child); err != nil {
						return err
					}
				}
			}
		}
	}
	return nil
}

func (r *IntegrationRegistry) RegisterBundle(bundle ToolBundle) error {
	if !namePattern.MatchString(bundle.Name) || strings.TrimSpace(bundle.Description) == "" || len(bundle.Description) > 500 || len(bundle.Tools) == 0 || len(bundle.Tools) > MaxBundleTools {
		return errors.New("bundle requires a valid name, description and 1–16 tools")
	}
	if err := integrationPrincipals(bundle.Principals); err != nil {
		return err
	}
	if len(bundle.RequiredScopes) > 16 {
		return errors.New("bundle exceeds 16 required scopes")
	}
	for _, scope := range bundle.RequiredScopes {
		if scope == "" || len(scope) > 256 || strings.TrimSpace(scope) != scope || len(strings.Fields(scope)) != 1 {
			return errors.New("invalid bundle scope")
		}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.bundles == nil {
		r.bundles = make(map[string]ToolBundle)
		r.disabledBundles = make(map[string]bool)
	}
	if _, exists := r.bundles[bundle.Name]; exists {
		return errors.New("bundle already registered")
	}
	if len(r.bundles) >= 64 {
		return errors.New("catalogue exceeds 64 bundles")
	}
	seen := make(map[string]bool)
	for _, name := range bundle.Tools {
		if r.tools[name] == nil || seen[name] {
			return errors.New("bundle tools must be registered and distinct")
		}
		seen[name] = true
	}
	bundle.Principals = slices.Clone(bundle.Principals)
	bundle.Tools = slices.Clone(bundle.Tools)
	bundle.RequiredScopes = slices.Clone(bundle.RequiredScopes)
	slices.Sort(bundle.Tools)
	r.bundles[bundle.Name] = bundle
	return nil
}

// SetToolEnabled and SetBundleEnabled are deployment/operator kill switches.
// Calls and discovery recheck them; cached client catalogues confer no authority.
func (r *IntegrationRegistry) SetToolEnabled(name string, enabled bool) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	tool := r.tools[name]
	if tool == nil {
		return ErrNotFound
	}
	tool.enabled = enabled
	return nil
}

func (r *IntegrationRegistry) SetBundleEnabled(name string, enabled bool) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.bundles[name]; !exists {
		return ErrNotFound
	}
	r.disabledBundles[name] = !enabled
	return nil
}

func (r *IntegrationRegistry) Bundles(identity *Identity) []ToolBundleDefinition {
	r.mu.RLock()
	defer r.mu.RUnlock()
	result := []ToolBundleDefinition{}
	if identity == nil || identity.ID == "" {
		return result
	}
	for name, bundle := range r.bundles {
		if r.disabledBundles[name] || !bundleAllowed(bundle, identity) {
			continue
		}
		count := 0
		for _, tool := range bundle.Tools {
			if r.toolAllowed(r.tools[tool], identity) {
				count++
			}
		}
		result = append(result, ToolBundleDefinition{Name: name, Description: bundle.Description, Tools: count})
	}
	slices.SortFunc(result, func(a, b ToolBundleDefinition) int { return strings.Compare(a.Name, b.Name) })
	return result
}

func bundleAllowed(bundle ToolBundle, identity *Identity) bool {
	if identity == nil || identity.ID == "" || !identity.matchesAny(bundle.Principals) {
		return false
	}
	for _, scope := range bundle.RequiredScopes {
		if !slices.Contains(identity.Scopes, scope) {
			return false
		}
	}
	return true
}

func (r *IntegrationRegistry) toolAllowed(tool *registeredIntegrationTool, identity *Identity) bool {
	return tool != nil && tool.enabled && identity != nil && identity.ID != "" && identity.matchesAny(tool.tool.Principals) && (tool.tool.ResourceField == "" || len(allowedToolResources(tool.tool, identity)) > 0)
}

func allowedToolResources(tool IntegrationTool, identity *Identity) []string {
	resources := []string{}
	for _, grant := range tool.Resources {
		if identity.matchesAny(grant.Principals) && !slices.Contains(resources, grant.Resource) {
			resources = append(resources, grant.Resource)
		}
	}
	slices.Sort(resources)
	return resources
}

func (r *IntegrationRegistry) authorizedTool(identity *Identity, bundleName, name string) (*registeredIntegrationTool, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	bundle, exists := r.bundles[bundleName]
	if !exists || r.disabledBundles[bundleName] || !bundleAllowed(bundle, identity) {
		return nil, ErrForbidden
	}
	tool := r.tools[name]
	if !slices.Contains(bundle.Tools, name) || !r.toolAllowed(tool, identity) {
		return nil, ErrForbidden
	}
	copy := *tool
	return &copy, nil
}

func (r *IntegrationRegistry) Tools(identity *Identity, bundleName string) ([]IntegrationToolDefinition, error) {
	r.mu.RLock()
	bundle, exists := r.bundles[bundleName]
	if !exists || r.disabledBundles[bundleName] || !bundleAllowed(bundle, identity) {
		r.mu.RUnlock()
		return nil, ErrForbidden
	}
	names := slices.Clone(bundle.Tools)
	r.mu.RUnlock()
	definitions := []IntegrationToolDefinition{}
	for _, name := range names {
		tool, err := r.authorizedTool(identity, bundleName, name)
		if err != nil {
			continue
		}
		definition := tool.tool.Definition
		definition.InputSchema = slices.Clone(definition.InputSchema)
		definition.OutputSchema = slices.Clone(definition.OutputSchema)
		if tool.tool.ResourceField != "" {
			definition.Resources = allowedToolResources(tool.tool, identity)
			var schema map[string]any
			if err := json.Unmarshal(definition.InputSchema, &schema); err != nil {
				return nil, err
			}
			properties := schema["properties"].(map[string]any)
			property := properties[tool.tool.ResourceField].(map[string]any)
			property["enum"] = definition.Resources
			definition.InputSchema, err = json.Marshal(schema)
			if err != nil {
				return nil, err
			}
		}
		definitions = append(definitions, definition)
	}
	return definitions, nil
}

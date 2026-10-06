package hex

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

// Integrations give apps typed, server-side access to third-party systems.
// Each integration registers named endpoints with JSON Schema contracts; the
// server calls the third-party API itself, so apps never see credentials and
// never get a raw proxy. Every call needs a permission granted to the caller
// through Config.IntegrationGrants, and integrations that RequireApproval
// additionally need a platform admin to approve each site that uses them.
//
// Endpoints call with either a platform credential owned by the integration
// (the default) or, when the integration names a Connector, with the caller's
// own account, which they connect once through the platform's OAuth broker.
//
// Integrations with Audit set record every call in Config.IntegrationAudit,
// cached results included, so admins can see who saw which records; such
// calls fail when the record cannot be written.

// Integration describes one third-party system and its endpoints.
type Integration struct {
	Name        string
	Title       string
	Description string
	// RequiresApproval makes sites ask a platform admin before their visitors
	// and automations can call any of the integration's endpoints.
	RequiresApproval bool
	// Connector names a registered Connector; its endpoints then call with
	// the caller's connected account instead of a platform credential.
	Connector string
	// Audit records every call of the integration's endpoints in
	// Config.IntegrationAudit, cached results included: who called, from
	// which site, the input and the records the result showed. Calls fail
	// when the record cannot be written.
	Audit     bool
	Endpoints []IntegrationEndpoint
}

// IntegrationEndpoint is one typed operation of an integration. Both
// schemas are required: results are validated against OutputSchema, and
// apps generate their types from both. Permission
// defaults to "<integration>.<endpoint>"; endpoints can share a permission
// by naming it explicitly. Write endpoints change data in the third-party
// system: they need the site's editor role, are never cached, and are
// skipped in automation dry runs. CacheTTL caches successful read results
// per input, and per caller for connector-backed integrations. Other
// integrations share cached results between callers, so an endpoint whose
// result depends on IntegrationCall.Identity must not set CacheTTL.
type IntegrationEndpoint struct {
	Name         string
	Description  string
	Permission   string
	Write        bool
	InputSchema  json.RawMessage
	OutputSchema json.RawMessage
	CacheTTL     time.Duration
	Handler      func(context.Context, IntegrationCall, json.RawMessage) (any, error)
	// AuditRecords names the third-party records a result shows, such as
	// "ticket:123", for the audit log of integrations with Audit set. It
	// receives the validated JSON output, also for cached results.
	AuditRecords func(output json.RawMessage) []string
}

// IntegrationCall describes who an endpoint runs for. Identity is nil when
// an automation calls; Automation then names it. HTTPClient returns a client
// authorized as the caller's connected account for connector-backed
// integrations.
type IntegrationCall struct {
	Site       string
	Identity   *Identity
	Automation string
	connection func(context.Context) (*http.Client, error)
}

// NewIntegrationCall describes a call for tests and for hosts that invoke
// handlers directly. A non-nil client is what HTTPClient returns.
func NewIntegrationCall(site string, identity *Identity, client *http.Client) IntegrationCall {
	call := IntegrationCall{Site: site, Identity: identity}
	if client != nil {
		call.connection = func(context.Context) (*http.Client, error) { return client, nil }
	}
	return call
}

// HTTPClient returns a client that authorizes requests as the caller's
// connected account, refreshing and storing its token as needed. It fails
// with ErrNotConnected when the caller has not connected the account.
func (c IntegrationCall) HTTPClient(ctx context.Context) (*http.Client, error) {
	if c.connection == nil {
		return nil, errors.New("this integration does not use connected accounts")
	}
	return c.connection(ctx)
}

// IntegrationError reports a failure the caller should see, such as a
// third-party 404 or a rejected input. Status is the HTTP status returned to
// the app; other handler errors become a generic 502.
type IntegrationError struct {
	Status  int
	Message string
}

func (e *IntegrationError) Error() string { return e.Message }

// ErrNotConnected reports that a connector-backed endpoint was called by
// someone who has not connected their account.
var ErrNotConnected = errors.New("account not connected")

// IntegrationGrant gives the principal the permissions. Principal is "*"
// (every signed-in user and every automation), a typed identity principal
// (user:, group:, role:), or "site:<name>" for the site's automations.
// Permissions are exact names, "<prefix>.*" or "*".
type IntegrationGrant struct {
	Principal   string   `json:"principal"`
	Permissions []string `json:"permissions"`
}

var (
	integrationNamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,39}$`)
	permissionPattern      = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]*(\.[a-z0-9][a-z0-9_-]*)*$`)
)

const maxIntegrationDescriptionSize = 500

type registeredIntegration struct {
	integration Integration
	endpoints   map[string]*registeredEndpoint
}

type registeredEndpoint struct {
	integration *registeredIntegration
	endpoint    IntegrationEndpoint
	input       *jsonschema.Schema
	output      *jsonschema.Schema
}

func (e *registeredEndpoint) permission() string {
	if e.endpoint.Permission != "" {
		return e.endpoint.Permission
	}
	return e.integration.integration.Name + "." + e.endpoint.Name
}

func (e *registeredEndpoint) qualifiedName() string {
	return e.integration.integration.Name + "." + e.endpoint.Name
}

// IntegrationRegistry holds the platform's integrations and connectors. Its
// zero value is ready to use; registration validates and compiles contracts
// and is safe alongside requests.
type IntegrationRegistry struct {
	mu           sync.RWMutex
	integrations map[string]*registeredIntegration
	connectors   map[string]Connector
}

// RegisterConnector adds an OAuth connector that integrations can name.
func (r *IntegrationRegistry) RegisterConnector(connector Connector) error {
	if !integrationNamePattern.MatchString(connector.Name) {
		return fmt.Errorf("invalid connector name %q; use lowercase letters, digits and -", connector.Name)
	}
	if strings.TrimSpace(connector.Title) == "" {
		return fmt.Errorf("connector %s needs a title", connector.Name)
	}
	if connector.OAuth2.ClientID == "" || connector.OAuth2.Endpoint.AuthURL == "" || connector.OAuth2.Endpoint.TokenURL == "" {
		return fmt.Errorf("connector %s needs a client ID and OAuth endpoints", connector.Name)
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if r.connectors == nil {
		r.connectors = make(map[string]Connector)
	}
	if _, exists := r.connectors[connector.Name]; exists {
		return fmt.Errorf("connector %s is already registered", connector.Name)
	}
	r.connectors[connector.Name] = connector
	return nil
}

// Register validates an integration and compiles its endpoint contracts.
// A connector it names must be registered first.
func (r *IntegrationRegistry) Register(integration Integration) error {
	if !integrationNamePattern.MatchString(integration.Name) {
		return fmt.Errorf("invalid integration name %q; use lowercase letters, digits and -", integration.Name)
	}
	label := "integration " + integration.Name
	if strings.TrimSpace(integration.Title) == "" {
		return fmt.Errorf("%s needs a title", label)
	}
	if len(integration.Endpoints) == 0 {
		return fmt.Errorf("%s needs at least one endpoint", label)
	}

	registered := &registeredIntegration{
		integration: integration,
		endpoints:   make(map[string]*registeredEndpoint, len(integration.Endpoints)),
	}
	for _, endpoint := range integration.Endpoints {
		compiled, err := compileEndpoint(registered, endpoint)
		if err != nil {
			return fmt.Errorf("%s: %w", label, err)
		}
		if _, exists := registered.endpoints[endpoint.Name]; exists {
			return fmt.Errorf("%s: endpoint %s is registered twice", label, endpoint.Name)
		}
		registered.endpoints[endpoint.Name] = compiled
	}
	registered.integration.Endpoints = slices.Clone(integration.Endpoints)

	r.mu.Lock()
	defer r.mu.Unlock()
	if integration.Connector != "" {
		if _, exists := r.connectors[integration.Connector]; !exists {
			return fmt.Errorf("%s names connector %s, which is not registered", label, integration.Connector)
		}
	}
	if r.integrations == nil {
		r.integrations = make(map[string]*registeredIntegration)
	}
	if _, exists := r.integrations[integration.Name]; exists {
		return fmt.Errorf("%s is already registered", label)
	}
	r.integrations[integration.Name] = registered
	return nil
}

func compileEndpoint(integration *registeredIntegration, endpoint IntegrationEndpoint) (*registeredEndpoint, error) {
	if !integrationNamePattern.MatchString(endpoint.Name) {
		return nil, fmt.Errorf("invalid endpoint name %q; use lowercase letters, digits and -", endpoint.Name)
	}
	label := "endpoint " + endpoint.Name
	description := strings.TrimSpace(endpoint.Description)
	if description == "" || len(description) > maxIntegrationDescriptionSize {
		return nil, fmt.Errorf("%s needs a description of at most %d characters", label, maxIntegrationDescriptionSize)
	}
	if endpoint.Handler == nil {
		return nil, fmt.Errorf("%s needs a handler", label)
	}
	if endpoint.Permission != "" && !permissionPattern.MatchString(endpoint.Permission) {
		return nil, fmt.Errorf("%s: invalid permission %q", label, endpoint.Permission)
	}
	if endpoint.Write && endpoint.CacheTTL > 0 {
		return nil, fmt.Errorf("%s: write endpoints cannot be cached", label)
	}

	input, err := compileActionSchema(endpoint.InputSchema)
	if err != nil {
		return nil, fmt.Errorf("%s input schema: %w", label, err)
	}
	if len(endpoint.InputSchema) == 0 || len(endpoint.OutputSchema) == 0 {
		return nil, fmt.Errorf("%s needs an input and an output schema", label)
	}
	output, err := compileActionSchema(endpoint.OutputSchema)
	if err != nil {
		return nil, fmt.Errorf("%s output schema: %w", label, err)
	}
	return &registeredEndpoint{integration: integration, endpoint: endpoint, input: input, output: output}, nil
}

func (r *IntegrationRegistry) integration(name string) *registeredIntegration {
	if r == nil {
		return nil
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.integrations[name]
}

func (r *IntegrationRegistry) connector(name string) (Connector, bool) {
	if r == nil {
		return Connector{}, false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	connector, exists := r.connectors[name]
	return connector, exists
}

func (r *IntegrationRegistry) endpoint(integrationName, endpointName string) *registeredEndpoint {
	integration := r.integration(integrationName)
	if integration == nil {
		return nil
	}
	return integration.endpoints[endpointName]
}

func (r *IntegrationRegistry) all() []*registeredIntegration {
	if r == nil {
		return nil
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	result := make([]*registeredIntegration, 0, len(r.integrations))
	for _, integration := range r.integrations {
		result = append(result, integration)
	}
	slices.SortFunc(result, func(a, b *registeredIntegration) int {
		return strings.Compare(a.integration.Name, b.integration.Name)
	})
	return result
}

func (r *IntegrationRegistry) allConnectors() []Connector {
	if r == nil {
		return nil
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	result := make([]Connector, 0, len(r.connectors))
	for _, connector := range r.connectors {
		result = append(result, connector)
	}
	slices.SortFunc(result, func(a, b Connector) int { return strings.Compare(a.Name, b.Name) })
	return result
}

func (i *registeredIntegration) sortedEndpoints() []*registeredEndpoint {
	result := make([]*registeredEndpoint, 0, len(i.endpoints))
	for _, endpoint := range i.endpoints {
		result = append(result, endpoint)
	}
	slices.SortFunc(result, func(a, b *registeredEndpoint) int {
		return strings.Compare(a.endpoint.Name, b.endpoint.Name)
	})
	return result
}

// ParseIntegrationGrants reads grants as a JSON object mapping principals to
// permission lists, e.g. {"role:hubspot.deals": ["hubspot.deals"], "*": ["slack.users"]}.
func ParseIntegrationGrants(value string) ([]IntegrationGrant, error) {
	if strings.TrimSpace(value) == "" {
		return nil, nil
	}
	var entries map[string][]string
	if err := json.Unmarshal([]byte(value), &entries); err != nil {
		return nil, fmt.Errorf("expected a JSON object of principals and permission lists: %w", err)
	}
	grants := make([]IntegrationGrant, 0, len(entries))
	for principal, permissions := range entries {
		grants = append(grants, IntegrationGrant{Principal: principal, Permissions: permissions})
	}
	slices.SortFunc(grants, func(a, b IntegrationGrant) int { return strings.Compare(a.Principal, b.Principal) })
	if err := ValidateIntegrationGrants(grants); err != nil {
		return nil, err
	}
	return grants, nil
}

// ValidateIntegrationGrants checks principals and permission patterns.
func ValidateIntegrationGrants(grants []IntegrationGrant) error {
	for _, grant := range grants {
		if err := validateGrantPrincipal(grant.Principal); err != nil {
			return err
		}
		for _, permission := range grant.Permissions {
			if !validPermissionPattern(permission) {
				return fmt.Errorf("grant for %s: invalid permission %q; use names such as hubspot.deals, hubspot.* or *", grant.Principal, permission)
			}
		}
	}
	return nil
}

func validateGrantPrincipal(principal string) error {
	if principal == "*" {
		return nil
	}
	kind, value, typed := strings.Cut(principal, ":")
	if !typed || value == "" {
		return fmt.Errorf("invalid grant principal %q; use *, user:, group:, role: or site:", principal)
	}
	switch kind {
	case "user", "group", "role":
		if !principalPattern.MatchString(principal) {
			return fmt.Errorf("invalid grant principal %q", principal)
		}
	case "site":
		if !siteNamePattern.MatchString(value) {
			return fmt.Errorf("invalid grant principal %q", principal)
		}
	default:
		return fmt.Errorf("invalid grant principal %q; use *, user:, group:, role: or site:", principal)
	}
	return nil
}

func validPermissionPattern(pattern string) bool {
	if pattern == "*" {
		return true
	}
	if prefix, wildcard := strings.CutSuffix(pattern, ".*"); wildcard {
		return permissionPattern.MatchString(prefix)
	}
	return permissionPattern.MatchString(pattern)
}

func permissionMatches(pattern, permission string) bool {
	if pattern == "*" || pattern == permission {
		return true
	}
	prefix, wildcard := strings.CutSuffix(pattern, "*")
	return wildcard && strings.HasSuffix(prefix, ".") && strings.HasPrefix(permission, prefix)
}

// integrationCaller is who an integration, AI or automation request runs
// for: a signed-in visitor of the site, or one of the site's automations.
type integrationCaller struct {
	site       string
	identity   *Identity
	role       siteRole
	automation string
	dryRun     bool
}

func (c integrationCaller) isAutomation() bool {
	return c.automation != ""
}

// usageKey identifies the caller for budgets by stable ID.
func (c integrationCaller) usageKey() string {
	if c.isAutomation() {
		return "automation:" + c.site + "/" + c.automation
	}
	if c.identity == nil {
		return ""
	}
	return "user:" + c.identity.ID
}

func (c integrationCaller) label() string {
	if c.isAutomation() {
		return "automation:" + c.site + "/" + c.automation
	}
	return identityName(c.identity)
}

// granted reports whether the configured grants give the caller the
// permission. Automations match "*" and their site's "site:" principal;
// people match "*" and their identity's principals.
func (s *Server) granted(caller integrationCaller, permission string) bool {
	for _, grant := range s.config.IntegrationGrants {
		if !s.grantApplies(grant.Principal, caller) {
			continue
		}
		for _, pattern := range grant.Permissions {
			if permissionMatches(pattern, permission) {
				return true
			}
		}
	}
	return false
}

func (s *Server) grantApplies(principal string, caller integrationCaller) bool {
	if principal == "*" {
		return caller.isAutomation() || caller.identity != nil
	}
	if site, isSite := strings.CutPrefix(principal, "site:"); isSite {
		return caller.isAutomation() && site == caller.site
	}
	if caller.isAutomation() {
		return false
	}
	return caller.identity.matches(principal)
}

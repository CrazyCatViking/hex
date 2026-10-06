package hex

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sync"
	"time"
)

const integrationCallTimeout = 60 * time.Second

func (s *Server) integrationsEnabled() bool {
	return len(s.config.Integrations.all()) > 0
}

func (s *Server) registerIntegrationRoutes() {
	if !s.integrationsEnabled() {
		return
	}
	s.mux.HandleFunc("GET /api/sites/{site}/integrations", s.siteScoped(s.listSiteIntegrations))
	s.mux.HandleFunc("POST /api/sites/{site}/integrations/{integration}/{endpoint}", s.siteScoped(s.callIntegrationEndpoint))
	s.mux.HandleFunc("GET /api/hex/integrations", s.integrationCatalog)

	if s.config.Identity != nil && s.config.IntegrationStore != nil {
		s.mux.HandleFunc("GET /api/hex/integration-approvals", s.listIntegrationApprovals)
		s.mux.HandleFunc("POST /api/hex/sites/{site}/integrations/{integration}/approval", s.requestIntegrationApproval)
		s.mux.HandleFunc("PUT /api/hex/sites/{site}/integrations/{integration}/approval", s.approveIntegration)
		s.mux.HandleFunc("DELETE /api/hex/sites/{site}/integrations/{integration}/approval", s.deleteIntegrationApproval)
	}
	s.registerConnectionRoutes()
}

// SiteIntegration is what a caller sees of one integration on a site. The
// endpoint list includes endpoints the caller may not use, marked as such,
// so apps can explain what access to ask for.
type SiteIntegration struct {
	Name             string                    `json:"name"`
	Title            string                    `json:"title"`
	Description      string                    `json:"description,omitempty"`
	RequiresApproval bool                      `json:"requiresApproval"`
	Approval         string                    `json:"approval,omitempty"`
	Connection       *connectionStatus         `json:"connection,omitempty"`
	Endpoints        []IntegrationEndpointInfo `json:"endpoints"`
}

// IntegrationEndpointInfo is an endpoint's discoverable contract.
type IntegrationEndpointInfo struct {
	Name         string          `json:"name"`
	Description  string          `json:"description"`
	Permission   string          `json:"permission"`
	Write        bool            `json:"write"`
	Allowed      bool            `json:"allowed"`
	InputSchema  json.RawMessage `json:"inputSchema,omitempty"`
	OutputSchema json.RawMessage `json:"outputSchema,omitempty"`
}

func (s *Server) listSiteIntegrations(w http.ResponseWriter, r *http.Request) {
	authorization := requestAuthorization(r)
	caller := integrationCaller{site: r.PathValue("site"), identity: authorization.identity, role: authorization.role}
	result, err := s.siteIntegrations(r.Context(), caller)
	if err != nil {
		writeServerError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) siteIntegrations(ctx context.Context, caller integrationCaller) ([]SiteIntegration, error) {
	result := make([]SiteIntegration, 0)
	for _, integration := range s.config.Integrations.all() {
		described := SiteIntegration{
			Name: integration.integration.Name, Title: integration.integration.Title,
			Description:      integration.integration.Description,
			RequiresApproval: integration.integration.RequiresApproval,
			Endpoints:        make([]IntegrationEndpointInfo, 0, len(integration.endpoints)),
		}
		approved := true
		if integration.integration.RequiresApproval {
			status, err := s.approvalStatus(ctx, caller.site, integration.integration.Name)
			if err != nil {
				return nil, err
			}
			described.Approval = status
			approved = status == ApprovalApproved
		}
		if name := integration.integration.Connector; name != "" && caller.identity != nil && s.connectionsEnabled() {
			connector, _ := s.config.Integrations.connector(name)
			status, err := s.connectionStatus(ctx, caller.identity, connector)
			if err != nil {
				return nil, err
			}
			described.Connection = &status
		}
		for _, endpoint := range integration.sortedEndpoints() {
			allowed := approved && s.integrationAccess(caller, endpoint) == nil
			described.Endpoints = append(described.Endpoints, endpointInfo(endpoint, allowed))
		}
		result = append(result, described)
	}
	return result, nil
}

func endpointInfo(endpoint *registeredEndpoint, allowed bool) IntegrationEndpointInfo {
	return IntegrationEndpointInfo{
		Name: endpoint.endpoint.Name, Description: endpoint.endpoint.Description,
		Permission: endpoint.permission(), Write: endpoint.endpoint.Write, Allowed: allowed,
		InputSchema: endpoint.endpoint.InputSchema, OutputSchema: endpoint.endpoint.OutputSchema,
	}
}

// integrationCatalog lists every integration with its endpoints and
// permissions, for administrators writing grants and owners planning apps.
func (s *Server) integrationCatalog(w http.ResponseWriter, r *http.Request) {
	identity := s.requestIdentity(r)
	if s.config.Identity != nil && identity == nil {
		writeError(w, http.StatusUnauthorized, "not authenticated")
		return
	}
	type catalogEntry struct {
		Name             string                    `json:"name"`
		Title            string                    `json:"title"`
		Description      string                    `json:"description,omitempty"`
		RequiresApproval bool                      `json:"requiresApproval"`
		Connector        string                    `json:"connector,omitempty"`
		Endpoints        []IntegrationEndpointInfo `json:"endpoints"`
	}
	caller := integrationCaller{identity: identity, role: roleEditor}
	result := make([]catalogEntry, 0)
	for _, integration := range s.config.Integrations.all() {
		entry := catalogEntry{
			Name: integration.integration.Name, Title: integration.integration.Title,
			Description:      integration.integration.Description,
			RequiresApproval: integration.integration.RequiresApproval,
			Connector:        integration.integration.Connector,
		}
		for _, endpoint := range integration.sortedEndpoints() {
			entry.Endpoints = append(entry.Endpoints, endpointInfo(endpoint, s.granted(caller, endpoint.permission())))
		}
		result = append(result, entry)
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) callIntegrationEndpoint(w http.ResponseWriter, r *http.Request) {
	endpoint := s.config.Integrations.endpoint(r.PathValue("integration"), r.PathValue("endpoint"))
	if endpoint == nil {
		writeError(w, http.StatusNotFound, "integration endpoint not found")
		return
	}
	input, err := io.ReadAll(http.MaxBytesReader(w, r.Body, MaxActionInputBytes))
	if err != nil {
		writeError(w, http.StatusBadRequest, "expected integration input of at most 1 MiB")
		return
	}
	if len(bytes.TrimSpace(input)) == 0 {
		input = []byte("{}")
	}

	authorization := requestAuthorization(r)
	caller := integrationCaller{site: r.PathValue("site"), identity: authorization.identity, role: authorization.role}
	output, cached, err := s.callIntegration(r.Context(), caller, endpoint, input)
	if err != nil {
		s.writeIntegrationError(w, endpoint, err)
		return
	}
	if cached {
		w.Header().Set("X-Hex-Cache", "hit")
	}
	writeJSON(w, http.StatusOK, output)
}

func (s *Server) writeIntegrationError(w http.ResponseWriter, endpoint *registeredEndpoint, err error) {
	var integrationError *IntegrationError
	switch {
	case errors.Is(err, ErrNotConnected):
		connector, _ := s.config.Integrations.connector(endpoint.integration.integration.Connector)
		writeJSON(w, http.StatusConflict, map[string]any{
			"error": "connect your " + connector.Title + " account to use this",
			"connect": map[string]string{
				"connector": connector.Name, "title": connector.Title, "url": s.connectURL(connector.Name),
			},
		})
	case errors.As(err, &integrationError):
		writeError(w, integrationError.Status, integrationError.Message)
	case errors.Is(err, context.DeadlineExceeded):
		writeError(w, http.StatusGatewayTimeout, endpoint.integration.integration.Title+" did not answer in time")
	default:
		slog.Error("integration call failed", "endpoint", endpoint.qualifiedName(), "error", err)
		writeError(w, http.StatusBadGateway, endpoint.integration.integration.Title+" request failed")
	}
}

// integrationAccess checks the caller's grant and site role for an
// endpoint. Site approval is checked separately because it needs the store.
func (s *Server) integrationAccess(caller integrationCaller, endpoint *registeredEndpoint) error {
	if !s.granted(caller, endpoint.permission()) {
		return &IntegrationError{Status: http.StatusForbidden, Message: "you do not have the " + endpoint.permission() + " permission"}
	}
	if endpoint.endpoint.Write && !caller.isAutomation() && caller.role < roleEditor {
		return &IntegrationError{Status: http.StatusForbidden, Message: "changing data through integrations needs the site's editor role"}
	}
	if endpoint.integration.integration.Connector != "" && caller.isAutomation() {
		return &IntegrationError{Status: http.StatusForbidden, Message: "automations cannot use integrations that call with a person's connected account"}
	}
	return nil
}

func (s *Server) approvalStatus(ctx context.Context, site, integration string) (string, error) {
	if s.config.IntegrationStore == nil {
		return "", nil
	}
	approval, err := s.config.IntegrationStore.GetIntegrationApproval(ctx, site, integration)
	if errors.Is(err, ErrNotFound) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return approval.Status, nil
}

// callIntegration authorizes and performs one endpoint call. It is shared
// by the HTTP API, AI tool use and automations, so all three apply the same
// grants, approvals and contracts.
func (s *Server) callIntegration(ctx context.Context, caller integrationCaller, endpoint *registeredEndpoint, input json.RawMessage) (json.RawMessage, bool, error) {
	if err := s.integrationAccess(caller, endpoint); err != nil {
		return nil, false, err
	}
	integration := endpoint.integration.integration
	if integration.RequiresApproval {
		status, err := s.approvalStatus(ctx, caller.site, integration.Name)
		if err != nil {
			return nil, false, err
		}
		if status != ApprovalApproved {
			return nil, false, &IntegrationError{
				Status:  http.StatusForbidden,
				Message: fmt.Sprintf("%s needs a platform admin to approve %s for this site", integration.Title, caller.site),
			}
		}
	}
	if err := validateActionJSON(endpoint.input, input); err != nil {
		return nil, false, &IntegrationError{Status: http.StatusBadRequest, Message: "invalid input: " + err.Error()}
	}
	if integration.Audit && s.config.IntegrationAudit == nil {
		slog.Error("audited integration called without an integration audit store", "endpoint", endpoint.qualifiedName())
		return nil, false, unauditedError(integration)
	}

	call := IntegrationCall{Site: caller.site, Identity: caller.identity, Automation: caller.automation}
	cacheOwner := ""
	if integration.Connector != "" {
		if caller.identity == nil {
			return nil, false, &IntegrationError{Status: http.StatusUnauthorized, Message: "sign in to use " + integration.Title}
		}
		owner := caller.identity.ID
		cacheOwner = owner
		call.connection = func(ctx context.Context) (*http.Client, error) {
			return s.connectionClient(ctx, integration.Connector, owner)
		}
	}
	cacheKey := integrationCacheKey(endpoint, cacheOwner, input)
	if endpoint.endpoint.CacheTTL > 0 {
		if output, ok := s.integrationCache.get(cacheKey); ok {
			outcome := integrationOutcome{output: output, cached: true}
			if err := s.finishIntegrationCall(ctx, caller, endpoint, input, outcome); err != nil {
				return nil, false, err
			}
			return output, true, nil
		}
	}

	started := time.Now()
	output, err := s.runIntegrationHandler(ctx, call, endpoint, input)
	outcome := integrationOutcome{output: output, failed: err != nil, duration: time.Since(started)}
	if auditErr := s.finishIntegrationCall(ctx, caller, endpoint, input, outcome); auditErr != nil {
		return nil, false, auditErr
	}
	if err != nil {
		return nil, false, err
	}
	if endpoint.endpoint.CacheTTL > 0 {
		s.integrationCache.put(cacheKey, output, endpoint.endpoint.CacheTTL)
	}
	return output, false, nil
}

// runIntegrationHandler calls the endpoint's handler and checks its result
// against the output contract.
func (s *Server) runIntegrationHandler(ctx context.Context, call IntegrationCall, endpoint *registeredEndpoint, input json.RawMessage) (json.RawMessage, error) {
	callContext, cancel := context.WithTimeout(ctx, integrationCallTimeout)
	defer cancel()
	result, err := endpoint.endpoint.Handler(callContext, call, input)
	if err != nil {
		return nil, err
	}

	output, err := json.Marshal(result)
	if err != nil {
		return nil, fmt.Errorf("encode %s result: %w", endpoint.qualifiedName(), err)
	}
	if err := validateActionJSON(endpoint.output, output); err != nil {
		return nil, fmt.Errorf("%s returned an invalid result: %w", endpoint.qualifiedName(), err)
	}
	return output, nil
}

func integrationCacheKey(endpoint *registeredEndpoint, owner string, input json.RawMessage) string {
	digest := sha256.Sum256(compactJSON(input))
	return endpoint.qualifiedName() + "\x00" + owner + "\x00" + hex.EncodeToString(digest[:])
}

// responseCache is a small in-process TTL cache for read results. When full
// it drops expired entries first and then arbitrary ones.
type responseCache struct {
	mu      sync.Mutex
	entries map[string]cacheEntry
}

type cacheEntry struct {
	value   json.RawMessage
	expires time.Time
}

const maxCacheEntries = 2048

func (c *responseCache) get(key string) (json.RawMessage, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, exists := c.entries[key]
	if !exists || time.Now().After(entry.expires) {
		return nil, false
	}
	return entry.value, true
}

func (c *responseCache) put(key string, value json.RawMessage, ttl time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.entries == nil {
		c.entries = make(map[string]cacheEntry)
	}
	if len(c.entries) >= maxCacheEntries {
		now := time.Now()
		for existing, entry := range c.entries {
			if now.After(entry.expires) {
				delete(c.entries, existing)
			}
		}
		for existing := range c.entries {
			if len(c.entries) < maxCacheEntries {
				break
			}
			delete(c.entries, existing)
		}
	}
	c.entries[key] = cacheEntry{value: value, expires: time.Now().Add(ttl)}
}

func (s *Server) requestApprovalTarget(w http.ResponseWriter, r *http.Request) (*Identity, *registeredIntegration, bool) {
	identity, ok := s.accessCaller(w, r)
	if !ok {
		return nil, nil, false
	}
	if !siteNamePattern.MatchString(r.PathValue("site")) {
		writeError(w, http.StatusBadRequest, "invalid site")
		return nil, nil, false
	}
	integration := s.config.Integrations.integration(r.PathValue("integration"))
	if integration == nil {
		writeError(w, http.StatusNotFound, "integration not found")
		return nil, nil, false
	}
	if !integration.integration.RequiresApproval {
		writeError(w, http.StatusBadRequest, integration.integration.Title+" does not require approval")
		return nil, nil, false
	}
	return identity, integration, true
}

func (s *Server) isSiteOwner(ctx context.Context, identity *Identity, site string) (bool, error) {
	access, exists, err := s.sitePolicy(ctx, site)
	if err != nil {
		return false, err
	}
	return s.siteRole(identity, access, exists) == roleOwner, nil
}

// requestIntegrationApproval records a site owner's request. Admins who
// request approval for a site approve it at once.
func (s *Server) requestIntegrationApproval(w http.ResponseWriter, r *http.Request) {
	identity, integration, ok := s.requestApprovalTarget(w, r)
	if !ok {
		return
	}
	site := r.PathValue("site")
	owner, err := s.isSiteOwner(r.Context(), identity, site)
	if err != nil {
		writeServerError(w, err)
		return
	}
	if !owner {
		writeError(w, http.StatusForbidden, "only the site's owners can request integrations for it")
		return
	}
	var body struct {
		Reason string `json:"reason"`
	}
	data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 8<<10))
	if err != nil {
		writeError(w, http.StatusBadRequest, "expected a request of at most 8 KiB")
		return
	}
	if len(bytes.TrimSpace(data)) > 0 {
		if err := json.Unmarshal(data, &body); err != nil {
			writeError(w, http.StatusBadRequest, "expected {\"reason\": \"...\"}")
			return
		}
	}

	existing, err := s.config.IntegrationStore.GetIntegrationApproval(r.Context(), site, integration.integration.Name)
	if err == nil && existing.Status == ApprovalApproved {
		writeJSON(w, http.StatusOK, existing)
		return
	}
	if err != nil && !errors.Is(err, ErrNotFound) {
		writeServerError(w, err)
		return
	}

	now := time.Now().UTC()
	approval := IntegrationApproval{
		Site: site, Integration: integration.integration.Name, Status: ApprovalRequested,
		Reason: body.Reason, RequestedBy: personOf(identity), RequestedAt: now,
	}
	if s.isAdmin(identity) {
		approval.Status = ApprovalApproved
		approval.DecidedBy = personOf(identity)
		approval.DecidedAt = now
	}
	if err := s.config.IntegrationStore.PutIntegrationApproval(r.Context(), approval); err != nil {
		writeServerError(w, err)
		return
	}
	slog.Info("integration approval requested", "site", site, "integration", approval.Integration, "status", approval.Status, "by", identityName(identity))
	writeJSON(w, http.StatusOK, approval)
}

func (s *Server) approveIntegration(w http.ResponseWriter, r *http.Request) {
	identity, integration, ok := s.requestApprovalTarget(w, r)
	if !ok {
		return
	}
	if !s.isAdmin(identity) {
		writeError(w, http.StatusForbidden, "only platform admins approve integrations")
		return
	}
	site := r.PathValue("site")
	approval, err := s.config.IntegrationStore.GetIntegrationApproval(r.Context(), site, integration.integration.Name)
	if errors.Is(err, ErrNotFound) {
		approval = IntegrationApproval{Site: site, Integration: integration.integration.Name}
	} else if err != nil {
		writeServerError(w, err)
		return
	}
	approval.Status = ApprovalApproved
	approval.DecidedBy = personOf(identity)
	approval.DecidedAt = time.Now().UTC()
	if err := s.config.IntegrationStore.PutIntegrationApproval(r.Context(), approval); err != nil {
		writeServerError(w, err)
		return
	}
	slog.Info("integration approved", "site", site, "integration", approval.Integration, "by", identityName(identity))
	writeJSON(w, http.StatusOK, approval)
}

// deleteIntegrationApproval lets admins revoke an approval and owners
// withdraw a pending request.
func (s *Server) deleteIntegrationApproval(w http.ResponseWriter, r *http.Request) {
	identity, integration, ok := s.requestApprovalTarget(w, r)
	if !ok {
		return
	}
	site := r.PathValue("site")
	approval, err := s.config.IntegrationStore.GetIntegrationApproval(r.Context(), site, integration.integration.Name)
	if errors.Is(err, ErrNotFound) {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if err != nil {
		writeServerError(w, err)
		return
	}
	allowed := s.isAdmin(identity)
	if !allowed && approval.Status == ApprovalRequested {
		allowed, err = s.isSiteOwner(r.Context(), identity, site)
		if err != nil {
			writeServerError(w, err)
			return
		}
	}
	if !allowed {
		writeError(w, http.StatusForbidden, "only platform admins revoke approvals")
		return
	}
	if err := s.config.IntegrationStore.DeleteIntegrationApproval(r.Context(), site, integration.integration.Name); err != nil && !errors.Is(err, ErrNotFound) {
		writeServerError(w, err)
		return
	}
	slog.Info("integration approval removed", "site", site, "integration", integration.integration.Name, "by", identityName(identity))
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) listIntegrationApprovals(w http.ResponseWriter, r *http.Request) {
	identity, ok := s.accessCaller(w, r)
	if !ok {
		return
	}
	if !s.isAdmin(identity) {
		writeError(w, http.StatusForbidden, "only platform admins list approvals")
		return
	}
	approvals, err := s.config.IntegrationStore.ListIntegrationApprovals(r.Context())
	if err != nil {
		writeServerError(w, err)
		return
	}
	status := r.URL.Query().Get("status")
	result := make([]IntegrationApproval, 0, len(approvals))
	for _, approval := range approvals {
		if status == "" || approval.Status == status {
			result = append(result, approval)
		}
	}
	writeJSON(w, http.StatusOK, result)
}

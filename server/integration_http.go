package hex

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"time"
)

func (s *Server) registerIntegrationRoutes() {
	if s.config.Integrations == nil {
		return
	}
	s.mux.HandleFunc("GET /api/hex/integrations", s.listIntegrationBundles)
	s.mux.HandleFunc("GET /api/hex/integrations/bundles/{bundle}/tools", s.listIntegrationTools)
	s.mux.HandleFunc("GET /api/hex/integrations/bundles/{bundle}/tools/{tool}", s.describeIntegrationTool)
	s.mux.HandleFunc("POST /api/hex/integrations/bundles/{bundle}/tools/{tool}", s.runIntegrationTool)
	s.mux.HandleFunc("GET /api/hex/integrations/audit", s.integrationAudit)
	if s.config.IntegrationMCP != nil {
		s.mux.Handle("/mcp/{bundle}", s.integrationMCPHandler())
		s.mux.HandleFunc("GET /.well-known/oauth-protected-resource", s.integrationMCPMetadata)
	}
}

func (s *Server) integrationCaller(w http.ResponseWriter, r *http.Request) (*Identity, bool) {
	if !s.platformHost(r.Host) {
		http.NotFound(w, r)
		return nil, false
	}
	identity := s.requestIdentity(r)
	if identity == nil || identity.ID == "" {
		writeError(w, http.StatusUnauthorized, "authentication required")
		return nil, false
	}
	return identity, true
}

func writeIntegrationError(w http.ResponseWriter, err error) {
	var integrationError *IntegrationError
	if errors.As(err, &integrationError) {
		writeJSON(w, integrationError.Status, map[string]string{"error": integrationError.Message, "code": integrationError.Code})
		return
	}
	if errors.Is(err, ErrForbidden) {
		writeError(w, http.StatusForbidden, "integration permission required")
		return
	}
	writeServerError(w, err)
}

func (s *Server) listIntegrationBundles(w http.ResponseWriter, r *http.Request) {
	identity, ok := s.integrationCaller(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, s.config.Integrations.Bundles(identity))
}

func (s *Server) listIntegrationTools(w http.ResponseWriter, r *http.Request) {
	identity, ok := s.integrationCaller(w, r)
	if !ok {
		return
	}
	tools, err := s.config.Integrations.Tools(identity, r.PathValue("bundle"))
	if err != nil {
		writeIntegrationError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, tools)
}

func (s *Server) describeIntegrationTool(w http.ResponseWriter, r *http.Request) {
	identity, ok := s.integrationCaller(w, r)
	if !ok {
		return
	}
	tools, err := s.config.Integrations.Tools(identity, r.PathValue("bundle"))
	if err != nil {
		writeIntegrationError(w, err)
		return
	}
	for _, tool := range tools {
		if tool.Name == r.PathValue("tool") {
			writeJSON(w, http.StatusOK, tool)
			return
		}
	}
	writeIntegrationError(w, ErrForbidden)
}

func (s *Server) runIntegrationTool(w http.ResponseWriter, r *http.Request) {
	identity, ok := s.integrationCaller(w, r)
	if !ok {
		return
	}
	if !namePattern.MatchString(r.PathValue("bundle")) || !namePattern.MatchString(r.PathValue("tool")) {
		writeError(w, 400, "invalid tool or bundle name")
		return
	}
	input, err := io.ReadAll(http.MaxBytesReader(w, r.Body, MaxIntegrationInputBytes))
	if err != nil {
		writeError(w, http.StatusRequestEntityTooLarge, "tool input exceeds 64 KiB")
		return
	}
	output, err := s.config.Integrations.Invoke(r.Context(), identity, r.PathValue("bundle"), r.PathValue("tool"), json.RawMessage(input), "http")
	if err != nil {
		writeIntegrationError(w, err)
		return
	}
	// Preserve the validated representation so HTML escaping cannot inflate
	// approved output beyond its charged byte budget.
	w.Header().Set("Content-Type", "application/json")
	if _, err := w.Write(output); err != nil {
		slog.Error("write integration result", "error", err)
	}
}

func (s *Server) integrationAudit(w http.ResponseWriter, r *http.Request) {
	identity, ok := s.integrationCaller(w, r)
	if !ok {
		return
	}
	query := ToolAuditQuery{UserID: r.URL.Query().Get("user"), Tool: r.URL.Query().Get("tool"), Limit: 50}
	if value := r.URL.Query().Get("limit"); value != "" {
		limit, err := strconv.Atoi(value)
		if err != nil || limit < 1 || limit > 100 {
			writeError(w, 400, "limit must be 1–100")
			return
		}
		query.Limit = limit
	}
	if value := r.URL.Query().Get("before"); value != "" {
		at, err := time.Parse(time.RFC3339Nano, value)
		if err != nil {
			writeError(w, 400, "before must be an RFC3339 timestamp")
			return
		}
		query.Before = at
	}
	records, err := s.config.Integrations.Audit(r.Context(), identity, query)
	if err != nil {
		writeIntegrationError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, records)
}

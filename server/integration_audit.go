package hex

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"time"
)

// IntegrationAuditRecord is one call of an audited integration's endpoint.
// Caller is the stable key of who it ran for ("user:<id>" or
// "automation:<site>/<name>"); Records are the IDs the endpoint's
// AuditRecords reported for the result.
type IntegrationAuditRecord struct {
	ID          string          `json:"id"`
	At          time.Time       `json:"at"`
	Site        string          `json:"site"`
	Integration string          `json:"integration"`
	Endpoint    string          `json:"endpoint"`
	Caller      string          `json:"caller"`
	CallerName  string          `json:"callerName,omitempty"`
	Input       json.RawMessage `json:"input"`
	Records     []string        `json:"records"`
	Cached      bool            `json:"cached"`
	Failed      bool            `json:"failed"`
}

// IntegrationAuditFilter selects records from Since (inclusive) to Until
// (exclusive), newest first, at most Limit of them. Endpoint is
// "<integration>.<endpoint>" or "<integration>.*"; Record matches one
// record ID exactly. Empty fields match everything.
type IntegrationAuditFilter struct {
	Since    time.Time
	Until    time.Time
	Site     string
	Caller   string
	Endpoint string
	Record   string
	Limit    int
}

// IntegrationAuditStore keeps the integration audit log.
type IntegrationAuditStore interface {
	RecordIntegrationAudit(ctx context.Context, record IntegrationAuditRecord) error
	ListIntegrationAudit(ctx context.Context, filter IntegrationAuditFilter) ([]IntegrationAuditRecord, error)
	DeleteIntegrationAuditBefore(ctx context.Context, cutoff time.Time) (int, error)
}

const (
	defaultIntegrationAuditRetention = 365 * 24 * time.Hour
	integrationAuditCleanupInterval  = time.Hour
	maxAuditedRecords                = 1000
	integrationAuditWriteTimeout     = 10 * time.Second
)

// integrationOutcome is how one endpoint call ended, for its log line and
// audit record. Output is empty for failed calls.
type integrationOutcome struct {
	output   json.RawMessage
	cached   bool
	failed   bool
	duration time.Duration
}

// auditedIntegrations names the integrations with Audit set.
func (s *Server) auditedIntegrations() []string {
	var names []string
	for _, integration := range s.config.Integrations.all() {
		if integration.integration.Audit {
			names = append(names, integration.integration.Name)
		}
	}
	return names
}

func (s *Server) integrationAuditEnabled() bool {
	return s.config.Identity != nil && s.config.IntegrationAudit != nil
}

// warnAboutUnauditedIntegrations reports audited integrations that will
// refuse every call because no audit store is configured.
func (s *Server) warnAboutUnauditedIntegrations() {
	if s.config.IntegrationAudit != nil {
		return
	}
	if names := s.auditedIntegrations(); len(names) > 0 {
		slog.Error("audited integrations refuse every call: configure an integration audit store", "integrations", names)
	}
}

func unauditedError(integration Integration) error {
	return &IntegrationError{
		Status:  http.StatusServiceUnavailable,
		Message: integration.Title + " is unavailable because its calls cannot be audited",
	}
}

// finishIntegrationCall logs a call that reached the endpoint and, for
// audited integrations, records it. A record that cannot be written fails
// the call, so audited data is never returned unrecorded.
func (s *Server) finishIntegrationCall(ctx context.Context, caller integrationCaller, endpoint *registeredEndpoint, input json.RawMessage, outcome integrationOutcome) error {
	slog.Info("integration call", "site", caller.site, "endpoint", endpoint.qualifiedName(),
		"caller", caller.label(), "duration", outcome.duration.Round(time.Millisecond),
		"cached", outcome.cached, "failed", outcome.failed)

	integration := endpoint.integration.integration
	if !integration.Audit {
		return nil
	}
	id, err := newID()
	if err != nil {
		slog.Error("record integration audit", "endpoint", endpoint.qualifiedName(), "error", err)
		return unauditedError(integration)
	}
	record := IntegrationAuditRecord{
		ID: id, At: time.Now().UTC(), Site: caller.site, Integration: integration.Name,
		Endpoint: endpoint.endpoint.Name, Caller: caller.usageKey(), CallerName: caller.label(),
		Input: compactJSON(input), Records: []string{}, Cached: outcome.cached, Failed: outcome.failed,
	}
	if !outcome.failed && endpoint.endpoint.AuditRecords != nil {
		record.Records = auditedRecords(endpoint.endpoint.AuditRecords(outcome.output))
	}
	auditContext, cancel := context.WithTimeout(context.WithoutCancel(ctx), integrationAuditWriteTimeout)
	defer cancel()
	if err := s.config.IntegrationAudit.RecordIntegrationAudit(auditContext, record); err != nil {
		slog.Error("record integration audit", "endpoint", endpoint.qualifiedName(), "caller", record.Caller, "error", err)
		return unauditedError(integration)
	}
	return nil
}

// auditedRecords drops empty and repeated IDs, keeping the first
// maxAuditedRecords in order.
func auditedRecords(ids []string) []string {
	result := make([]string, 0, min(len(ids), maxAuditedRecords))
	seen := make(map[string]bool, len(ids))
	for _, id := range ids {
		if len(result) == maxAuditedRecords {
			break
		}
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		result = append(result, id)
	}
	return result
}

func compactJSON(data json.RawMessage) json.RawMessage {
	var compact bytes.Buffer
	if err := json.Compact(&compact, data); err != nil {
		return data
	}
	return compact.Bytes()
}

// cleanUpIntegrationAudit removes audit records older than the retention
// until ctx ends.
func (s *Server) cleanUpIntegrationAudit(ctx context.Context) {
	ticker := time.NewTicker(integrationAuditCleanupInterval)
	defer ticker.Stop()
	for {
		cutoff := time.Now().UTC().Add(-s.config.IntegrationAuditRetention)
		removed, err := s.config.IntegrationAudit.DeleteIntegrationAuditBefore(ctx, cutoff)
		if err != nil && ctx.Err() == nil {
			slog.Error("remove expired integration audit records", "error", err)
		}
		if removed > 0 {
			slog.Info("removed expired integration audit records", "count", removed)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

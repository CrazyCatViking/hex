package hex

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"time"
)

type IntegrationError struct {
	Status  int
	Code    string
	Message string
}

func (e *IntegrationError) Error() string { return e.Message }

// IntegrationRuntime is the common enforcement path for HTTP, CLI and MCP.
// The host owns state-store connections and adapter credentials/lifecycle.
type IntegrationRuntime struct {
	registry        *IntegrationRegistry
	store           IntegrationStateStore
	budget          IntegrationBudget
	auditPrincipals []string
}

func NewIntegrationRuntime(registry *IntegrationRegistry, store IntegrationStateStore, budget IntegrationBudget, auditPrincipals []string) (*IntegrationRuntime, error) {
	if registry == nil || store == nil {
		return nil, errors.New("integration runtime requires a registry and state store")
	}
	if budget == (IntegrationBudget{}) {
		budget = DefaultIntegrationBudget()
	}
	if budget.CallsPerHour < 1 || budget.OutputBytesPerHour < 1 || budget.RecordsPerHour < 1 || budget.ConcurrentCalls < 1 || budget.ConcurrentCalls > 100 {
		return nil, errors.New("integration budgets must be positive; concurrency must be 1–100")
	}
	if len(auditPrincipals) > 0 {
		if err := integrationPrincipals(auditPrincipals); err != nil {
			return nil, err
		}
	}
	return &IntegrationRuntime{registry: registry, store: store, budget: budget, auditPrincipals: slices.Clone(auditPrincipals)}, nil
}

func (r *IntegrationRuntime) Bundles(identity *Identity) []ToolBundleDefinition {
	return r.registry.Bundles(identity)
}
func (r *IntegrationRuntime) Tools(identity *Identity, bundle string) ([]IntegrationToolDefinition, error) {
	return r.registry.Tools(identity, bundle)
}

func (r *IntegrationRuntime) Audit(ctx context.Context, identity *Identity, query ToolAuditQuery) ([]ToolCallAudit, error) {
	if identity == nil || identity.ID == "" || !identity.matchesAny(r.auditPrincipals) {
		return nil, ErrForbidden
	}
	return r.store.ListToolAudit(ctx, query)
}

func (r *IntegrationRuntime) Invoke(ctx context.Context, identity *Identity, bundle, name string, input json.RawMessage, transport string) (json.RawMessage, error) {
	at := time.Now().UTC()
	digest := sha256.Sum256(input)
	audit := ToolCallAudit{ID: rand.Text(), Bundle: bundle, Tool: name, Transport: transport, StartedAt: at, InputHash: fmt.Sprintf("%x", digest)}
	if identity != nil {
		audit.UserID = identity.ID
	}
	deny := func(status int, code, message string) (json.RawMessage, error) {
		audit.Status = code
		if err := r.recordAudit(ctx, audit); err != nil {
			logIntegrationStateFailure("record denial", audit, err)
			return nil, integrationUnavailable()
		}
		return nil, &IntegrationError{Status: status, Code: code, Message: message}
	}
	if !namePattern.MatchString(name) || !namePattern.MatchString(bundle) {
		audit.Tool, audit.Bundle = "", ""
		return deny(400, "invalid_name", "invalid tool or bundle name")
	}
	tool, err := r.registry.authorizedTool(identity, bundle, name)
	if err != nil {
		return deny(403, "denied", "tool is not permitted in this bundle")
	}
	audit.Integration, audit.Version = tool.tool.Definition.Integration, tool.tool.Definition.Version
	limits := tool.tool.Definition.Limits
	if len(input) > limits.InputBytes || validateActionJSON(tool.input, input) != nil {
		return deny(400, "invalid_input", "input violates the tool contract or size limit")
	}
	resource := ""
	if tool.tool.ResourceField != "" {
		var value map[string]json.RawMessage
		if err := json.Unmarshal(input, &value); err != nil {
			return deny(400, "invalid_input", "invalid tool input")
		}
		if err := json.Unmarshal(value[tool.tool.ResourceField], &resource); err != nil || !slices.Contains(allowedToolResources(tool.tool, identity), resource) {
			return deny(403, "denied_resource", "resource is not permitted")
		}
	}
	audit.Resource = resource
	audit.LeaseUntil = at.Add(time.Duration(limits.TimeoutSeconds)*time.Second + time.Minute)
	audit.Status = "pending"
	reservation := ToolCallReservation{Audit: audit, Budget: r.budget, OutputBytes: int64(limits.OutputBytes), Records: limits.Records}
	if err := r.store.ReserveToolCall(ctx, reservation); err != nil {
		if errors.Is(err, ErrIntegrationBudget) {
			return deny(429, "budget_exhausted", "integration budget exhausted; try again later")
		}
		logIntegrationStateFailure("reserve invocation", audit, err)
		return nil, integrationUnavailable()
	}
	caller := IntegrationContext{Identity: *identity, Bundle: bundle, Resource: resource, Limits: limits}
	caller.Identity.Groups = slices.Clone(identity.Groups)
	caller.Identity.Roles = slices.Clone(identity.Roles)
	caller.Identity.Scopes = slices.Clone(identity.Scopes)
	caller.Identity.Audiences = slices.Clone(identity.Audiences)
	callContext, cancel := context.WithTimeout(ctx, time.Duration(limits.TimeoutSeconds)*time.Second)
	output, handlerError := invokeIntegrationHandler(callContext, tool.tool.Handler, caller, input)
	if callContext.Err() != nil {
		handlerError = callContext.Err()
	}
	cancel()
	resultError := (*IntegrationError)(nil)
	switch {
	case errors.Is(handlerError, context.DeadlineExceeded), errors.Is(handlerError, context.Canceled):
		resultError = &IntegrationError{Status: 504, Code: "timeout", Message: "tool deadline exceeded or request cancelled"}
	case errors.Is(handlerError, ErrForbidden):
		resultError = &IntegrationError{Status: 403, Code: "denied", Message: "integration operation denied"}
	case errors.Is(handlerError, ErrNotFound):
		resultError = &IntegrationError{Status: 404, Code: "not_found", Message: "integration resource not found"}
	case handlerError != nil:
		// Upstream errors can contain credentials and customer payloads.
		resultError = &IntegrationError{Status: 502, Code: "upstream_failed", Message: "integration operation failed"}
	case len(output) > limits.OutputBytes:
		resultError = &IntegrationError{Status: 502, Code: "output_limit", Message: "tool result exceeds its output limit"}
	case validateActionJSON(tool.output, output) != nil:
		resultError = &IntegrationError{Status: 502, Code: "invalid_output", Message: "tool returned an invalid result"}
	default:
		var value any
		if err := json.Unmarshal(output, &value); err != nil {
			resultError = &IntegrationError{Status: 502, Code: "invalid_output", Message: "tool returned invalid JSON"}
		} else {
			records := max(int64(1), integrationRecordUnits(value))
			if records > limits.Records {
				resultError = &IntegrationError{Status: 502, Code: "record_limit", Message: "tool returned too many records"}
			} else {
				audit.Records = records
				audit.OutputBytes = int64(len(output))
			}
		}
	}
	audit.Status = "succeeded"
	if resultError != nil {
		audit.Status = resultError.Code
		audit.Records, audit.OutputBytes = 0, 0
	}
	audit.DurationMillis = time.Since(at).Milliseconds()
	finishContext, stop := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	err = r.store.FinishToolCall(finishContext, audit)
	stop()
	if err != nil {
		logIntegrationStateFailure("finish invocation", audit, err)
		return nil, integrationUnavailable()
	}
	if resultError != nil {
		return nil, resultError
	}
	return output, nil
}

func logIntegrationStateFailure(operation string, audit ToolCallAudit, err error) {
	// Do not log provider error strings: they may contain connection secrets.
	slog.Error("integration state operation failed", "operation", operation, "call", audit.ID, "tool", audit.Tool, "errorType", fmt.Sprintf("%T", err))
}

func invokeIntegrationHandler(ctx context.Context, handler func(context.Context, IntegrationContext, json.RawMessage) (json.RawMessage, error), caller IntegrationContext, input json.RawMessage) (output json.RawMessage, err error) {
	defer func() {
		if recover() != nil {
			output = nil
			err = errors.New("integration handler panicked")
		}
	}()
	return handler(ctx, caller, input)
}

func integrationRecordUnits(value any) int64 {
	var count int64
	switch value := value.(type) {
	case []any:
		count += int64(len(value))
		for _, item := range value {
			count += integrationRecordUnits(item)
		}
	case map[string]any:
		for _, item := range value {
			count += integrationRecordUnits(item)
		}
	}
	return count
}

func integrationUnavailable() *IntegrationError {
	return &IntegrationError{Status: 503, Code: "unavailable", Message: "integration state or audit storage unavailable"}
}

func (r *IntegrationRuntime) recordAudit(ctx context.Context, audit ToolCallAudit) error {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	return r.store.RecordToolAudit(ctx, audit)
}

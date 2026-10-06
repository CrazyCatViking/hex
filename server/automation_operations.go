package hex

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"reflect"
	"slices"
	"strings"
)

func (r *automationRunner) callEndpoint(ctx context.Context, name string, input json.RawMessage) (any, string, error) {
	integration, endpointName, _ := strings.Cut(name, ".")
	endpoint := r.server.config.Integrations.endpoint(integration, endpointName)
	if endpoint == nil {
		return nil, "", fmt.Errorf("there is no integration endpoint %s", name)
	}
	output, _, err := r.server.callIntegration(ctx, r.caller, endpoint, input)
	if err != nil {
		return nil, "", automationError(err)
	}
	if endpoint.endpoint.Write && r.run.DryRun {
		return json.RawMessage(output), OperationDryRun, nil
	}
	return json.RawMessage(output), "", nil
}

func (r *automationRunner) runAction(ctx context.Context, name string, input json.RawMessage) (any, string, error) {
	actions, err := r.server.siteActions(ctx, r.site)
	if err != nil {
		return nil, "", automationError(err)
	}
	index := slices.IndexFunc(actions, func(action *registeredAction) bool { return action.definition.Name == name })
	if index < 0 {
		return nil, "", fmt.Errorf("the site has no action %s", name)
	}
	action := actions[index]
	if r.run.DryRun {
		if err := action.validateInput(input); err != nil {
			return nil, "", fmt.Errorf("invalid input for action %s: %w", name, err)
		}
		return map[string]any{"wouldRun": name, "input": json.RawMessage(input)}, OperationDryRun, nil
	}
	authorization, err := r.server.automationAuthorization(ctx, r.site, r.automation.Name)
	if err != nil {
		return nil, "", automationError(err)
	}
	caller := ActionContext{Site: r.site, Identity: authorization.identity, Role: roleNames[roleOwner], authorization: authorization}
	output, err := action.execute(ctx, caller, input)
	if err != nil {
		var inputError *actionInputError
		if errors.As(err, &inputError) {
			return nil, "", fmt.Errorf("invalid input for action %s: %w", name, err)
		}
		return nil, "", automationError(err)
	}
	return output, "", nil
}

func (s *Server) automationAuthorization(ctx context.Context, site, automation string) (siteAuthorization, error) {
	access, _, err := s.sitePolicy(ctx, site)
	if err != nil {
		return siteAuthorization{}, err
	}
	identity := &Identity{Provider: "automation", ID: "automation:" + automation, Name: automation}
	return siteAuthorization{identity: identity, access: access, role: roleOwner}, nil
}

func (r *automationRunner) askAI(ctx context.Context, input *automationAIRequest) (any, error) {
	if !r.server.aiEnabled() {
		return nil, errors.New("this platform has no AI provider")
	}
	request := SiteAIRequest{
		AIRequest: AIRequest{
			Model: input.Model, System: input.System, MaxTokens: input.MaxTokens, Thinking: input.Thinking,
			Messages: []AIMessage{{Role: RoleUser, Content: []AIContent{{Type: ContentText, Text: input.Prompt}}}},
		},
		IntegrationTools: input.Tools,
	}
	prepared, tools, err := r.server.prepareAIRequest(ctx, r.caller, request)
	if err != nil {
		return nil, err
	}
	completion, err := r.server.completeConversation(ctx, r.caller, prepared, tools)
	if err != nil {
		return nil, err
	}
	return map[string]any{"text": completion.Text, "stopReason": completion.StopReason, "usage": completion.Usage}, nil
}

func (r *automationRunner) queryDocuments(ctx context.Context, collection string, where map[string]any, limit int) (any, error) {
	if r.server.config.Database == nil {
		return nil, errors.New("this platform has no database")
	}
	if limit == 0 {
		limit = 100
	}
	matches := make([]any, 0)
	matchedBytes := 2
	after := ""
	for len(matches) < limit {
		if err := ctx.Err(); err != nil {
			return nil, automationError(err)
		}
		if r.scannedDocuments >= maxScannedDocuments || r.scannedBytes >= maxScannedBytes {
			r.hostBudgetError = errAutomationHostBudget
			return nil, r.hostBudgetError
		}
		// A single bounded row keeps provider and decoder allocations bounded,
		// including for nonmatching documents. Budgets are shared across queries.
		page, err := r.server.config.Database.List(ctx, r.site, collection, ListOptions{After: after, Limit: 1, MaxDocumentBytes: min(maxAutomationOutput, maxScannedBytes-r.scannedBytes)})
		if err != nil {
			if errors.Is(err, ErrDocumentReadLimit) {
				r.hostBudgetError = errAutomationHostBudget
				return nil, r.hostBudgetError
			}
			return nil, automationError(err)
		}
		if len(page) > 1 {
			r.hostBudgetError = errAutomationHostBudget
			return nil, r.hostBudgetError
		}
		for _, document := range page {
			r.scannedDocuments++
			r.scannedBytes += len(document.Data)
			if len(document.Data) > maxAutomationOutput || r.scannedBytes > maxScannedBytes {
				r.hostBudgetError = errAutomationHostBudget
				return nil, r.hostBudgetError
			}
			var fields map[string]any
			if err := json.Unmarshal(document.Data, &fields); err != nil {
				continue
			}
			if documentMatches(fields, where) && len(matches) < limit {
				matchedBytes += len(document.Data) + len(document.ID) + 32
				if matchedBytes > maxAutomationOutput {
					return nil, errors.New("query output exceeds 1 MiB; narrow the query or reduce its limit")
				}
				matches = append(matches, map[string]any{"id": document.ID, "data": fields})
			}
		}
		if len(page) == 0 {
			break
		}
		after = page[len(page)-1].ID
	}
	return matches, nil
}

func documentMatches(fields, where map[string]any) bool {
	for field, wanted := range where {
		value, exists := fields[field]
		if !exists || !reflect.DeepEqual(value, wanted) {
			return false
		}
	}
	return true
}

func (r *automationRunner) saveDocument(ctx context.Context, collection, id string, document json.RawMessage) (any, string, error) {
	if r.server.config.Database == nil {
		return nil, "", errors.New("this platform has no database")
	}
	var fields map[string]any
	if err := json.Unmarshal(document, &fields); err != nil || fields == nil {
		return nil, "", errors.New("save data must be a JSON object")
	}
	if len(document) > MaxActionInputBytes {
		return nil, "", errors.New("the document would exceed 1 MiB")
	}
	if id == "" {
		generated, err := newID()
		if err != nil {
			return nil, "", err
		}
		id = generated
	}
	if !namePattern.MatchString(id) {
		return nil, "", fmt.Errorf("invalid document ID %q", id)
	}
	if r.run.DryRun {
		return map[string]any{"wouldSave": collection, "id": id, "data": json.RawMessage(document)}, OperationDryRun, nil
	}
	stored, err := r.server.config.Database.Put(ctx, r.site, collection, id, document, WriteOptions{Creator: "automation:" + r.automation.Name})
	if err != nil {
		return nil, "", automationError(err)
	}
	return map[string]any{"id": stored.ID, "collection": collection}, "", nil
}

func automationError(err error) error {
	var integrationError *IntegrationError
	var actionError *ActionError
	switch {
	case errors.As(err, &integrationError):
		return errors.New(integrationError.Message)
	case errors.As(err, &actionError):
		return errors.New(actionError.Message)
	case errors.Is(err, ErrForbidden):
		return errors.New("not permitted")
	case errors.Is(err, context.DeadlineExceeded):
		return errors.New("automation host operation timed out")
	case errors.Is(err, context.Canceled):
		return errors.New("automation operation cancelled")
	default:
		slog.Error("automation operation", "error", err)
		return errors.New("the request failed; see the platform log")
	}
}

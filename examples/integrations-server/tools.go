package main

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	hex "github.com/crazycatviking/hex/server"
)

// These are deterministic fixtures, not live Sentry/BigQuery connectors. Real
// adapters keep their credentials server-side, constrain upstream queries and
// map approved fields into the same closed result contracts.
func exampleIntegrations() (*hex.IntegrationRegistry, error) {
	registry := new(hex.IntegrationRegistry)
	engineering := []string{"user:local-dev", "group:engineering"}
	product := []string{"user:local-dev", "group:product"}
	issues := hex.IntegrationTool{
		Definition: hex.IntegrationToolDefinition{ActionDefinition: hex.ActionDefinition{
			Name: "sentry_top_issues", Description: "Example: top issue summaries for an approved project, up to 14 days and 20 results.",
			InputSchema:  json.RawMessage(`{"type":"object","properties":{"project":{"type":"string","maxLength":128},"days":{"type":"integer","minimum":1,"maximum":14},"limit":{"type":"integer","minimum":1,"maximum":20}},"required":["project","days","limit"],"additionalProperties":false}`),
			OutputSchema: json.RawMessage(`{"type":"object","properties":{"items":{"type":"array","maxItems":20,"items":{"type":"object","properties":{"id":{"type":"string","maxLength":100},"title":{"type":"string","maxLength":200},"count":{"type":"integer"}},"required":["id","title","count"],"additionalProperties":false}},"freshness":{"type":"string","maxLength":100}},"required":["items","freshness"],"additionalProperties":false}`),
		}, Integration: "sentry-example", Version: "fixture-v1", ReadOnly: true, Limits: hex.ToolLimits{OutputBytes: 8 << 10, Records: 20}},
		Principals: engineering, ResourceField: "project", Resources: []hex.ResourceGrant{{Resource: "web", Principals: engineering}, {Resource: "api", Principals: engineering}},
		Handler: func(ctx context.Context, caller hex.IntegrationContext, input json.RawMessage) (json.RawMessage, error) {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			return json.Marshal(map[string]any{"items": []map[string]any{{"id": caller.Resource + "-001", "title": "Example timeout", "count": 12}}, "freshness": "fixture data, not production"})
		},
	}
	if err := registry.RegisterTool(issues); err != nil {
		return nil, err
	}
	usage := hex.IntegrationTool{
		Definition: hex.IntegrationToolDefinition{ActionDefinition: hex.ActionDefinition{
			Name: "production_usage_summary", Description: "Example: approved usage aggregates, never arbitrary SQL or raw customer records.",
			InputSchema:  json.RawMessage(`{"type":"object","properties":{"days":{"type":"integer","minimum":1,"maximum":30}},"required":["days"],"additionalProperties":false}`),
			OutputSchema: json.RawMessage(`{"type":"object","properties":{"activeCustomers":{"type":"integer"},"periodDays":{"type":"integer"},"asOf":{"type":"string","format":"date-time"},"source":{"type":"string","enum":["fixture"]}},"required":["activeCustomers","periodDays","asOf","source"],"additionalProperties":false}`),
		}, Integration: "production-example", Version: "fixture-v1", ReadOnly: true, Limits: hex.ToolLimits{OutputBytes: 1024, Records: 1}},
		Principals: append(append([]string{}, engineering...), product...),
		Handler: func(ctx context.Context, caller hex.IntegrationContext, input json.RawMessage) (json.RawMessage, error) {
			var request struct {
				Days int `json:"days"`
			}
			if err := json.Unmarshal(input, &request); err != nil {
				return nil, fmt.Errorf("decode usage request: %w", err)
			}
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			return json.Marshal(map[string]any{"activeCustomers": 42, "periodDays": request.Days, "asOf": time.Now().UTC().Format(time.RFC3339), "source": "fixture"})
		},
	}
	if err := registry.RegisterTool(usage); err != nil {
		return nil, err
	}
	if err := registry.RegisterBundle(hex.ToolBundle{Name: "engineering", Description: "Issue and production summaries for engineering.", Principals: engineering, Tools: []string{issues.Definition.Name, usage.Definition.Name}}); err != nil {
		return nil, err
	}
	if err := registry.RegisterBundle(hex.ToolBundle{Name: "product", Description: "Curated product statistics.", Principals: product, Tools: []string{usage.Definition.Name}}); err != nil {
		return nil, err
	}
	return registry, nil
}

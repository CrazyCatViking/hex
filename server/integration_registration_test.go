package hex_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	hex "github.com/crazycatviking/hex/server"
	"github.com/crazycatviking/hex/server/providers/memory"
)

func TestIntegrationRegistrationRejectsUnsafeContractsAndCopiesPolicies(t *testing.T) {
	for _, mutate := range []func(*hex.IntegrationTool){
		func(tool *hex.IntegrationTool) { tool.Principals = nil },
		func(tool *hex.IntegrationTool) { tool.Definition.ReadOnly = false },
		func(tool *hex.IntegrationTool) {
			tool.Definition.InputSchema = json.RawMessage(`{"type":"object","additionalProperties":true}`)
		},
		func(tool *hex.IntegrationTool) {
			tool.Definition.OutputSchema = json.RawMessage(`{"type":"object","additionalProperties":false,"properties":{"data":{"type":["object","null"]}}}`)
		},
		func(tool *hex.IntegrationTool) {
			tool.Definition.OutputSchema = json.RawMessage(`{"type":"object","additionalProperties":false,"properties":{"data":true}}`)
		},
		func(tool *hex.IntegrationTool) {
			tool.Definition.OutputSchema = json.RawMessage(`{"type":"object","additionalProperties":false,"properties":{"data":{}}}`)
		},
		func(tool *hex.IntegrationTool) {
			tool.Definition.OutputSchema = json.RawMessage(`{"type":"object","additionalProperties":false,"properties":{"data":{"type":"array"}}}`)
		},
		func(tool *hex.IntegrationTool) {
			tool.Definition.InputSchema = json.RawMessage(`{"type":"object","additionalProperties":false,"$ref":"https://outside.example/schema"}`)
		},
	} {
		registry := new(hex.IntegrationRegistry)
		tool := hex.IntegrationTool{Definition: integrationDefinition(), Principals: []string{"user:alice"}, Handler: func(context.Context, hex.IntegrationContext, json.RawMessage) (json.RawMessage, error) {
			return json.RawMessage(`{"items":[]}`), nil
		}}
		mutate(&tool)
		if err := registry.RegisterTool(tool); err == nil {
			t.Fatal("unsafe contract was registered")
		}
	}
	registry := new(hex.IntegrationRegistry)
	principals := []string{"user:alice"}
	tool := hex.IntegrationTool{Definition: integrationDefinition(), Principals: principals, Handler: func(context.Context, hex.IntegrationContext, json.RawMessage) (json.RawMessage, error) {
		return json.RawMessage(`{"items":[]}`), nil
	}}
	if err := registry.RegisterTool(tool); err != nil {
		t.Fatal(err)
	}
	if err := registry.RegisterBundle(hex.ToolBundle{Name: "scoped", Description: "Scoped agent reads", Tools: []string{"issues_summary"}, Principals: principals, RequiredScopes: []string{"engineering.read"}}); err != nil {
		t.Fatal(err)
	}
	principals[0] = "user:mallory"
	identity := &hex.Identity{ID: "alice"}
	if _, err := registry.Tools(identity, "scoped"); err == nil {
		t.Fatal("bundle accepted missing token scope")
	}
	identity.Scopes = []string{"engineering.read"}
	definitions, err := registry.Tools(identity, "scoped")
	if err != nil || len(definitions) != 1 {
		t.Fatal("policy slices were not copied")
	}
	definitions[0].InputSchema[0] = '!'
	if _, err := registry.Tools(identity, "scoped"); err != nil {
		t.Fatal("caller could mutate registered schemas")
	}
	runtime := integrationRuntime(t, registry, memory.NewIntegrationState(), hex.IntegrationBudget{})
	if output, err := runtime.Invoke(context.Background(), identity, "scoped", "issues_summary", json.RawMessage(`{"project":"web"}`), "http"); err != nil || !strings.Contains(string(output), "items") {
		t.Fatalf("scoped call failed: %s %v", output, err)
	}
}

package hex_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	hex "github.com/crazycatviking/hex/server"
	"github.com/crazycatviking/hex/server/providers/easyauth"
	"github.com/crazycatviking/hex/server/providers/memory"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func integrationDefinition() hex.IntegrationToolDefinition {
	return hex.IntegrationToolDefinition{ActionDefinition: hex.ActionDefinition{
		Name: "issues_summary", Description: "Read bounded issue summaries for an approved project.",
		InputSchema:  json.RawMessage(`{"type":"object","properties":{"project":{"type":"string"},"limit":{"type":"integer","minimum":1,"maximum":3}},"required":["project"],"additionalProperties":false}`),
		OutputSchema: json.RawMessage(`{"type":"object","properties":{"items":{"type":"array","items":{"type":"object","properties":{"id":{"type":"string","maxLength":50}},"required":["id"],"additionalProperties":false}}},"required":["items"],"additionalProperties":false}`),
	}, Integration: "sentry", Version: "v1", ReadOnly: true, Limits: hex.ToolLimits{Records: 3}}
}

func integrationRegistry(t *testing.T, handler func(context.Context, hex.IntegrationContext, json.RawMessage) (json.RawMessage, error)) *hex.IntegrationRegistry {
	t.Helper()
	registry := new(hex.IntegrationRegistry)
	tool := hex.IntegrationTool{Definition: integrationDefinition(), Principals: []string{"group:engineering"}, ResourceField: "project", Resources: []hex.ResourceGrant{{Resource: "web", Principals: []string{"user:alice"}}, {Resource: "private", Principals: []string{"user:bob"}}}, Handler: handler}
	if err := registry.RegisterTool(tool); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"engineering", "other"} {
		if err := registry.RegisterBundle(hex.ToolBundle{Name: name, Description: "Approved engineering reads.", Principals: []string{"group:engineering"}, Tools: []string{tool.Definition.Name}}); err != nil {
			t.Fatal(err)
		}
	}
	return registry
}

func integrationRuntime(t *testing.T, registry *hex.IntegrationRegistry, store hex.IntegrationStateStore, budget hex.IntegrationBudget) *hex.IntegrationRuntime {
	t.Helper()
	runtime, err := hex.NewIntegrationRuntime(registry, store, budget, []string{"role:integration-auditor"})
	if err != nil {
		t.Fatal(err)
	}
	return runtime
}

func TestCuratedIntegrationsPermissionsLimitsAndAudit(t *testing.T) {
	var calls atomic.Int32
	registry := integrationRegistry(t, func(ctx context.Context, caller hex.IntegrationContext, input json.RawMessage) (json.RawMessage, error) {
		calls.Add(1)
		if caller.Identity.ID != "alice" || caller.Resource != "web" {
			t.Error("handler did not receive verified identity/resource")
		}
		return json.RawMessage(`{"items":[{"id":"issue-1"}]}`), nil
	})
	store := memory.NewIntegrationState()
	runtime := integrationRuntime(t, registry, store, hex.IntegrationBudget{CallsPerHour: 2, OutputBytesPerHour: 1 << 20, RecordsPerHour: 100, ConcurrentCalls: 1})
	server := hex.New(hex.Config{Integrations: runtime, Identity: easyauth.Resolver{}, SiteBaseURL: "http://example.com", AdminGroups: []string{"admins"}})
	alice := principalHeaders("alice", "engineering")
	admin := principalHeaders("site-admin", "admins")
	base := "/api/hex/integrations/bundles/engineering/tools"
	requestAs(t, server, nil, "GET", "/api/hex/integrations", nil, 401)
	requestAs(t, server, admin, "GET", base, nil, 403)
	definitions := requestAs(t, server, alice, "GET", base, nil, 200).Body.String()
	if !strings.Contains(definitions, `"web"`) || strings.Contains(definitions, `"private"`) {
		t.Fatalf("discovery disclosed unauthorized resources: %s", definitions)
	}
	for _, input := range []string{`{"project":"web","userId":"bob"}`, `{"project":"web","limit":1000}`, `{}`, `{"project":"web"} trailing`} {
		requestAs(t, server, alice, "POST", base+"/issues_summary", []byte(input), 400)
	}
	requestAs(t, server, alice, "POST", base+"/issues_summary", []byte(`{"project":"private"}`), 403)
	requestAs(t, server, alice, "POST", base+"/unknown", []byte(`{}`), 403)
	if calls.Load() != 0 {
		t.Fatal("unauthorized/invalid calls reached the adapter")
	}
	requestAs(t, server, alice, "POST", base+"/issues_summary", []byte(`{"project":"web"}`), 200)
	// Changing bundles or transports cannot reset the per-user budget.
	identity := &hex.Identity{ID: "alice", Groups: []string{"engineering"}}
	if _, err := runtime.Invoke(context.Background(), identity, "other", "issues_summary", json.RawMessage(`{"project":"web"}`), "mcp-http"); err != nil {
		t.Fatal(err)
	}
	requestAs(t, server, alice, "POST", base+"/issues_summary", []byte(`{"project":"web"}`), 429)
	if err := registry.SetToolEnabled("issues_summary", false); err != nil {
		t.Fatal(err)
	}
	requestAs(t, server, alice, "POST", base+"/issues_summary", []byte(`{"project":"web"}`), 403)
	requestAs(t, server, alice, "GET", "/api/hex/integrations/audit", nil, 403)
	requestAs(t, server, admin, "GET", "/api/hex/integrations/audit", nil, 403)
	auditor := principalHeaders("auditor")
	// The audit role is independent of platform-admin groups.
	principal := map[string]any{"auth_typ": "aad", "claims": []map[string]string{{"typ": "roles", "val": "integration-auditor"}}}
	encoded, err := json.Marshal(principal)
	if err != nil {
		t.Fatal(err)
	}
	auditor.Set("X-Ms-Client-Principal", base64.StdEncoding.EncodeToString(encoded))
	records := requestAs(t, server, auditor, "GET", "/api/hex/integrations/audit?user=alice", nil, 200).Body.String()
	if strings.Contains(records, `"project"`) || strings.Contains(records, "issue-1") || !strings.Contains(records, "inputHash") || !strings.Contains(records, "budget_exhausted") {
		t.Fatalf("audit contains payloads or is incomplete: %s", records)
	}
	requestAs(t, server, alice, "GET", "http://demo.example.com"+base, nil, 403)
}

func TestIntegrationOutputAndDeadlineEnforcement(t *testing.T) {
	for _, test := range []struct {
		name   string
		output json.RawMessage
		err    error
		code   string
	}{
		{"unexpected field", json.RawMessage(`{"items":[],"secret":"must-not-leak"}`), nil, "invalid_output"},
		{"too many records", json.RawMessage(`{"items":[{"id":"1"},{"id":"2"},{"id":"3"},{"id":"4"}]}`), nil, "record_limit"},
		{"too many bytes", json.RawMessage(strings.Repeat("x", 20<<10)), nil, "output_limit"},
		{"upstream secret", nil, errors.New("Authorization: Bearer must-not-leak"), "upstream_failed"},
		{"cancelled", nil, context.DeadlineExceeded, "timeout"},
	} {
		t.Run(test.name, func(t *testing.T) {
			registry := integrationRegistry(t, func(context.Context, hex.IntegrationContext, json.RawMessage) (json.RawMessage, error) {
				return test.output, test.err
			})
			runtime := integrationRuntime(t, registry, memory.NewIntegrationState(), hex.IntegrationBudget{})
			output, err := runtime.Invoke(context.Background(), &hex.Identity{ID: "alice", Groups: []string{"engineering"}}, "engineering", "issues_summary", json.RawMessage(`{"project":"web"}`), "http")
			var failure *hex.IntegrationError
			if output != nil || !errors.As(err, &failure) || failure.Code != test.code || strings.Contains(err.Error(), "must-not-leak") {
				t.Fatalf("invalid output escaped: %s %v", output, err)
			}
		})
	}
}

type failingIntegrationAudit struct{ *memory.IntegrationState }

func (s failingIntegrationAudit) FinishToolCall(context.Context, hex.ToolCallAudit) error {
	return errors.New("audit database unavailable")
}

func TestIntegrationConcurrentReservationAndFailClosedAudit(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	registry := integrationRegistry(t, func(ctx context.Context, caller hex.IntegrationContext, input json.RawMessage) (json.RawMessage, error) {
		close(entered)
		<-release
		return json.RawMessage(`{"items":[]}`), nil
	})
	state := memory.NewIntegrationState()
	runtime := integrationRuntime(t, registry, state, hex.IntegrationBudget{CallsPerHour: 10, OutputBytesPerHour: 1 << 20, RecordsPerHour: 100, ConcurrentCalls: 1})
	identity := &hex.Identity{ID: "alice", Groups: []string{"engineering"}}
	done := make(chan error, 1)
	go func() {
		_, err := runtime.Invoke(context.Background(), identity, "engineering", "issues_summary", json.RawMessage(`{"project":"web"}`), "http")
		done <- err
	}()
	<-entered
	_, err := runtime.Invoke(context.Background(), identity, "other", "issues_summary", json.RawMessage(`{"project":"web"}`), "mcp-http")
	var failure *hex.IntegrationError
	if !errors.As(err, &failure) || failure.Status != 429 {
		t.Errorf("concurrency budget did not apply across bundles: %v", err)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	registry = integrationRegistry(t, func(context.Context, hex.IntegrationContext, json.RawMessage) (json.RawMessage, error) {
		return json.RawMessage(`{"items":[{"id":"secret"}]}`), nil
	})
	runtime = integrationRuntime(t, registry, failingIntegrationAudit{memory.NewIntegrationState()}, hex.IntegrationBudget{})
	output, err := runtime.Invoke(context.Background(), identity, "engineering", "issues_summary", json.RawMessage(`{"project":"web"}`), "http")
	if output != nil || !errors.As(err, &failure) || failure.Status != 503 {
		t.Fatal("output was delivered without a durable audit completion")
	}
}

func TestMCPToolCallsShareRuntimeAndEnforceBundle(t *testing.T) {
	registry := integrationRegistry(t, func(context.Context, hex.IntegrationContext, json.RawMessage) (json.RawMessage, error) {
		return json.RawMessage(`{"items":[{"id":"one"}]}`), nil
	})
	state := memory.NewIntegrationState()
	runtime := integrationRuntime(t, registry, state, hex.IntegrationBudget{})
	identity := &hex.Identity{ID: "alice", Groups: []string{"engineering"}}
	definitions, err := runtime.Tools(identity, "engineering")
	if err != nil {
		t.Fatal(err)
	}
	server := hex.NewIntegrationMCPServer(definitions, func(ctx context.Context, name string, input json.RawMessage) (json.RawMessage, error) {
		return runtime.Invoke(ctx, identity, "engineering", name, input, "mcp-stdio")
	})
	clientTransport, serverTransport := mcp.NewInMemoryTransports()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	serverSession, err := server.Connect(ctx, serverTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer serverSession.Close()
	client := mcp.NewClient(&mcp.Implementation{Name: "test-agent", Version: "1"}, nil)
	session, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	tools, err := session.ListTools(ctx, nil)
	if err != nil || len(tools.Tools) != 1 || tools.Tools[0].Name != "issues_summary" {
		t.Fatalf("bad MCP discovery: %+v %v", tools, err)
	}
	result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "issues_summary", Arguments: map[string]any{"project": "web"}})
	if err != nil || result.IsError || result.StructuredContent == nil {
		t.Fatalf("MCP invocation failed: %+v %v", result, err)
	}
	if err := registry.SetToolEnabled("issues_summary", false); err != nil {
		t.Fatal(err)
	}
	result, err = session.CallTool(ctx, &mcp.CallToolParams{Name: "issues_summary", Arguments: map[string]any{"project": "web"}})
	if err != nil || !result.IsError {
		t.Fatal("cached MCP tool bypassed revocation")
	}
	result, err = session.CallTool(ctx, &mcp.CallToolParams{Name: "unknown", Arguments: map[string]any{}})
	if err != nil || !result.IsError {
		t.Fatal("unknown tool did not reach audited denial path")
	}
	audits, err := state.ListToolAudit(ctx, hex.ToolAuditQuery{})
	if err != nil || len(audits) != 3 {
		t.Fatalf("MCP calls were not audited: %+v %v", audits, err)
	}
}

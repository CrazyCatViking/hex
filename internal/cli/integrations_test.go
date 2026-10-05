package cli

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	hex "github.com/crazycatviking/hex/server"
	"github.com/crazycatviking/hex/server/providers/memory"
	"golang.org/x/oauth2"
)

var cliObjectSchema = json.RawMessage(`{"type":"object"}`)

// startIntegrationPlatform runs a platform with integrations, a connector,
// AI and automations, signed in as a platform admin.
func startIntegrationPlatform(t *testing.T, calls *atomic.Int32) testPlatform {
	t.Helper()
	registry := new(hex.IntegrationRegistry)
	if err := registry.RegisterConnector(hex.Connector{
		Name: "docs", Title: "Docs",
		OAuth2: oauth2.Config{ClientID: "client", Endpoint: oauth2.Endpoint{
			AuthURL: "https://auth.example.test/authorize", TokenURL: "https://auth.example.test/token",
		}},
	}); err != nil {
		t.Fatal(err)
	}
	integrations := []hex.Integration{
		{
			Name: "crm", Title: "CRM", RequiresApproval: true,
			Endpoints: []hex.IntegrationEndpoint{{
				Name: "deals", Description: "List deals.",
				InputSchema: json.RawMessage(`{"type":"object","properties":{"stage":{"type":"string"}},"additionalProperties":false}`),
				Handler: func(_ context.Context, _ hex.IntegrationCall, input json.RawMessage) (any, error) {
					calls.Add(1)
					return map[string]any{"deals": []string{"Acme"}, "input": input}, nil
				},
			}},
		},
		{
			Name: "docs", Title: "Docs", Connector: "docs",
			Endpoints: []hex.IntegrationEndpoint{{
				Name: "pages", Description: "List pages.", InputSchema: cliObjectSchema,
				Handler: func(ctx context.Context, call hex.IntegrationCall, _ json.RawMessage) (any, error) {
					_, err := call.HTTPClient(ctx)
					return nil, err
				},
			}},
		},
		{
			Name: "chat", Title: "Chat",
			Endpoints: []hex.IntegrationEndpoint{{
				Name: "post", Description: "Post a message.", Write: true,
				InputSchema: json.RawMessage(`{"type":"object","properties":{"text":{"type":"string"}},"required":["text"]}`),
				Handler: func(context.Context, hex.IntegrationCall, json.RawMessage) (any, error) {
					calls.Add(1)
					return map[string]string{"ts": "1"}, nil
				},
			}},
		},
	}
	for _, integration := range integrations {
		if err := registry.Register(integration); err != nil {
			t.Fatal(err)
		}
	}
	return startPlatform(t, func(config *hex.Config) {
		config.Identity = hex.StaticIdentity{Identity: hex.Identity{ID: "local", Name: "Local"}}
		config.AdminGroups = []string{"local"}
		config.Access = memory.NewAccessStore()
		config.Database = memory.NewDatabase()
		config.Integrations = registry
		config.IntegrationStore = memory.NewIntegrationStore()
		config.CredentialKey = make([]byte, 32)
		config.IntegrationGrants = []hex.IntegrationGrant{
			{Principal: "*", Permissions: []string{"*"}},
			{Principal: "site:demo", Permissions: []string{"crm.*", "chat.post"}},
		}
		config.AI = &hex.AIConfig{Provider: &fakeAIProvider{}}
		config.Automations = memory.NewAutomationStore()
	})
}

type fakeAIProvider struct{}

func (fakeAIProvider) Models(context.Context) ([]hex.AIModel, error) {
	return []hex.AIModel{{ID: "general", Name: "General", Thinking: true, Tools: true}}, nil
}

func (fakeAIProvider) Stream(_ context.Context, request hex.AIRequest) (hex.AIStream, error) {
	answer := "You asked: " + request.Messages[0].Content[0].Text
	return &fakeAIStream{events: []hex.AIEvent{
		{Type: hex.EventThinking, Text: "considering"},
		{Type: hex.EventText, Text: answer},
		{Type: hex.EventMessage, Message: &hex.AIMessage{Role: hex.RoleAssistant, Content: []hex.AIContent{{Type: hex.ContentText, Text: answer}}}},
		{Type: hex.EventDone, StopReason: hex.StopEndTurn, Usage: &hex.AIUsage{InputTokens: 3, OutputTokens: 4}},
	}}, nil
}

type fakeAIStream struct{ events []hex.AIEvent }

func (s *fakeAIStream) Next() (hex.AIEvent, error) {
	if len(s.events) == 0 {
		return hex.AIEvent{}, io.EOF
	}
	event := s.events[0]
	s.events = s.events[1:]
	return event, nil
}

func (s *fakeAIStream) Close() error { return nil }

// runFailing runs a command that must fail and returns its error and output.
func runFailing(t *testing.T, directory string, args ...string) (error, string) {
	t.Helper()
	app, output := testApp(t, directory)
	err := app.Execute(context.Background(), args, "test")
	if err == nil {
		t.Fatalf("%v succeeded: %s", args, output)
	}
	return err, output.String()
}

func TestIntegrationsCLI(t *testing.T) {
	directory := t.TempDir()
	var calls atomic.Int32
	platform := startIntegrationPlatform(t, &calls)
	saveReadProfile(t, directory, platform.URL)

	listing := run(t, directory, "integrations", "list", "--site", "demo")
	if !strings.Contains(listing, "crm.deals") || !strings.Contains(listing, "needs approval (not requested)") ||
		!strings.Contains(listing, "connect with: hex connections connect docs") {
		t.Fatal(listing)
	}
	if catalog := run(t, directory, "integrations", "catalog"); !strings.Contains(catalog, `"permission": "chat.post"`) {
		t.Fatal(catalog)
	}

	err, _ := runFailing(t, directory, "integrations", "call", "--site", "demo", "crm.deals", "--input", `{"stage":5}`)
	if !strings.Contains(err.Error(), "invalid input") || calls.Load() != 0 {
		t.Fatalf("invalid input reached the platform: %v", err)
	}
	err, _ = runFailing(t, directory, "integrations", "call", "--site", "demo", "crm.deals")
	if !strings.Contains(err.Error(), "approve") {
		t.Fatalf("expected an approval error: %v", err)
	}

	// The admin's own request approves at once.
	if approved := run(t, directory, "integrations", "request", "--site", "demo", "crm", "--reason", "Dashboard"); !strings.Contains(approved, `"status": "approved"`) {
		t.Fatal(approved)
	}
	if approvals := run(t, directory, "integrations", "approvals", "--status", "approved"); !strings.Contains(approvals, `"reason": "Dashboard"`) {
		t.Fatal(approvals)
	}
	if err := os.WriteFile(filepath.Join(directory, "input.json"), []byte(`{"stage":"won"}`), 0600); err != nil {
		t.Fatal(err)
	}
	result := run(t, directory, "integrations", "call", "--site", "demo", "crm.deals", "--input", "@input.json")
	if !strings.Contains(result, `"Acme"`) || !strings.Contains(result, `"stage": "won"`) || calls.Load() != 1 {
		t.Fatal(result)
	}
	run(t, directory, "integrations", "revoke", "--site", "demo", "crm")
	runFailing(t, directory, "integrations", "call", "--site", "demo", "crm.deals")

	err, _ = runFailing(t, directory, "integrations", "call", "--site", "demo", "docs.pages")
	if !strings.Contains(err.Error(), "hex connections connect docs") || !strings.Contains(err.Error(), "/api/hex/connections/docs/start") {
		t.Fatalf("expected a connection hint: %v", err)
	}
	err, _ = runFailing(t, directory, "integrations", "call", "--site", "demo", "crm.missing")
	if !strings.Contains(err.Error(), "no endpoint missing") {
		t.Fatal(err)
	}
}

func TestConnectionsCLI(t *testing.T) {
	directory := t.TempDir()
	platform := startIntegrationPlatform(t, new(atomic.Int32))
	saveReadProfile(t, directory, platform.URL)

	if listing := run(t, directory, "connections", "list"); !strings.Contains(listing, `"connected": false`) {
		t.Fatal(listing)
	}
	app, output := testApp(t, directory)
	var opened string
	app.OpenBrowser = func(address string) error {
		opened = address
		return nil
	}
	if err := app.Execute(context.Background(), []string{"connections", "connect", "docs", "--no-wait"}, "test"); err != nil {
		t.Fatal(err)
	}
	if opened != platform.URL+"/api/hex/connections/docs/start" || !strings.Contains(output.String(), opened) {
		t.Fatalf("opened %q: %s", opened, output)
	}
	err, _ := runFailing(t, directory, "connections", "connect", "unknown", "--no-wait")
	if !strings.Contains(err.Error(), "no connector unknown") {
		t.Fatal(err)
	}
	if disconnected := run(t, directory, "connections", "disconnect", "docs"); !strings.Contains(disconnected, "Disconnected docs") {
		t.Fatal(disconnected)
	}
}

func TestAICLI(t *testing.T) {
	directory := t.TempDir()
	platform := startIntegrationPlatform(t, new(atomic.Int32))
	saveReadProfile(t, directory, platform.URL)

	if models := run(t, directory, "ai", "models", "--site", "demo"); !strings.Contains(models, `"id": "general"`) {
		t.Fatal(models)
	}
	answer := run(t, directory, "ai", "ask", "--site", "demo", "--show-thinking", "--thinking", "high", "How", "are", "deals?")
	if !strings.Contains(answer, "You asked: How are deals?") || !strings.Contains(answer, "[thinking] considering") || !strings.Contains(answer, "3 input and 4 output tokens") {
		t.Fatal(answer)
	}
	completion := run(t, directory, "ai", "ask", "--site", "demo", "--model", "general", "--json", "Hi")
	if !strings.Contains(completion, `"stopReason": "end_turn"`) || !strings.Contains(completion, `"text": "You asked: Hi"`) {
		t.Fatal(completion)
	}
	err, _ := runFailing(t, directory, "ai", "ask", "--site", "demo", "--thinking", "huge", "Hi")
	if !strings.Contains(err.Error(), "--thinking must be one of") {
		t.Fatal(err)
	}
	err, _ = runFailing(t, directory, "ai", "ask", "--site", "demo", "--model", "missing", "Hi")
	if !strings.Contains(err.Error(), "not available") {
		t.Fatal(err)
	}
}

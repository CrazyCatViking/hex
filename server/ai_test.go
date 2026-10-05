package hex_test

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"

	hex "github.com/crazycatviking/hex/server"
	"github.com/crazycatviking/hex/server/providers/easyauth"
	"github.com/crazycatviking/hex/server/providers/memory"
)

// scriptedProvider answers each model call with the next scripted turn and
// records the requests it received.
type scriptedProvider struct {
	mu       sync.Mutex
	turns    [][]hex.AIEvent
	requests []hex.AIRequest
}

func (p *scriptedProvider) Models(context.Context) ([]hex.AIModel, error) {
	return []hex.AIModel{
		{ID: "general", Name: "General", Thinking: true, Tools: true, MaxOutputTokens: 4000, Price: &hex.AIPrice{Input: 1, Output: 5}},
		{ID: "premium", Name: "Premium", Tools: true, Permission: "ai.premium"},
	}, nil
}

func (p *scriptedProvider) Stream(_ context.Context, request hex.AIRequest) (hex.AIStream, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.requests = append(p.requests, request)
	turn := p.turns[0]
	p.turns = p.turns[1:]
	return &scriptedStream{events: turn}, nil
}

type scriptedStream struct{ events []hex.AIEvent }

func (s *scriptedStream) Next() (hex.AIEvent, error) {
	if len(s.events) == 0 {
		return hex.AIEvent{}, io.EOF
	}
	event := s.events[0]
	s.events = s.events[1:]
	return event, nil
}

func (s *scriptedStream) Close() error { return nil }

func assistantTurn(stop string, content ...hex.AIContent) []hex.AIEvent {
	var events []hex.AIEvent
	for _, block := range content {
		if block.Type == hex.ContentText {
			events = append(events, hex.AIEvent{Type: hex.EventText, Text: block.Text})
		}
		if block.Type == hex.ContentToolCall {
			copied := block
			events = append(events, hex.AIEvent{Type: hex.EventToolCall, Content: &copied})
		}
	}
	return append(events,
		hex.AIEvent{Type: hex.EventMessage, Message: &hex.AIMessage{Role: hex.RoleAssistant, Content: content}},
		hex.AIEvent{Type: hex.EventDone, StopReason: stop, Usage: &hex.AIUsage{InputTokens: 10, OutputTokens: 5}},
	)
}

func setupAI(t *testing.T, provider hex.AIProvider, limits hex.AILimits) (*hex.Server, *memory.AIUsageStore) {
	t.Helper()
	registry := new(hex.IntegrationRegistry)
	if err := registry.Register(hex.Integration{
		Name: "crm", Title: "CRM",
		Endpoints: []hex.IntegrationEndpoint{
			{
				Name: "deals", Description: "List deals.", OutputSchema: anyResult, InputSchema: json.RawMessage(`{"type":"object","properties":{"stage":{"type":"string"}}}`),
				Handler: func(_ context.Context, call hex.IntegrationCall, input json.RawMessage) (any, error) {
					return map[string]any{"deals": []string{"Acme"}, "for": call.Identity.ID}, nil
				},
			},
			{
				Name: "secret", Description: "Restricted.", OutputSchema: anyResult, InputSchema: objectSchema,
				Handler: func(context.Context, hex.IntegrationCall, json.RawMessage) (any, error) { return nil, nil },
			},
		},
	}); err != nil {
		t.Fatal(err)
	}
	usage := memory.NewAIUsageStore()
	server := hex.New(hex.Config{
		Identity: easyauth.Resolver{}, Access: memory.NewAccessStore(),
		Integrations: registry, IntegrationStore: memory.NewIntegrationStore(),
		IntegrationGrants: []hex.IntegrationGrant{
			{Principal: "*", Permissions: []string{"ai", "crm.deals"}},
			{Principal: "role:Premium", Permissions: []string{"ai.premium"}},
		},
		AI:      &hex.AIConfig{Provider: provider, MaxToolRounds: 3, Limits: limits},
		AIUsage: usage,
	})
	return server, usage
}

func readEvents(t *testing.T, body string) []hex.AIEvent {
	t.Helper()
	var events []hex.AIEvent
	scanner := bufio.NewScanner(strings.NewReader(body))
	for scanner.Scan() {
		data, found := strings.CutPrefix(scanner.Text(), "data: ")
		if !found {
			continue
		}
		var event hex.AIEvent
		if err := json.Unmarshal([]byte(data), &event); err != nil {
			t.Fatal(err)
		}
		events = append(events, event)
	}
	return events
}

func TestAIStreamRunsIntegrationTools(t *testing.T) {
	provider := &scriptedProvider{turns: [][]hex.AIEvent{
		assistantTurn(hex.StopToolUse,
			hex.AIContent{Type: hex.ContentText, Text: "Looking up deals."},
			hex.AIContent{Type: hex.ContentToolCall, ToolCallID: "call-1", Name: "crm__deals", Input: json.RawMessage(`{"stage":"won"}`)},
			hex.AIContent{Type: hex.ContentToolCall, ToolCallID: "call-2", Name: "crm__secret", Input: json.RawMessage(`{}`)}),
		assistantTurn(hex.StopEndTurn, hex.AIContent{Type: hex.ContentText, Text: "Acme won."}),
	}}
	server, usage := setupAI(t, provider, hex.AILimits{})
	person := roleHeaders("person")

	models := requestAs(t, server, person, "GET", "/api/sites/demo/ai/models", nil, 200)
	if strings.Contains(models.Body.String(), "premium") || !strings.Contains(models.Body.String(), "general") {
		t.Fatal(models.Body.String())
	}

	body := `{"model":"general","maxTokens":999999,"thinking":{"effort":"high","show":true},
		"messages":[{"role":"user","content":[{"type":"text","text":"How are deals?"}]}],
		"integrationTools":["crm.*"]}`
	response := requestAs(t, server, person, "POST", "/api/sites/demo/ai/stream", []byte(body), 200)
	if response.Header().Get("Content-Type") != "text/event-stream" {
		t.Fatal(response.Header())
	}
	events := readEvents(t, response.Body.String())

	first := provider.requests[0]
	if first.MaxTokens != 4000 || first.Thinking == nil || len(first.Tools) != 1 || first.Tools[0].Name != "crm__deals" {
		t.Fatalf("unexpected first request: %+v", first)
	}
	second := provider.requests[1]
	if len(second.Messages) != 3 || second.Messages[2].Content[0].ToolCallID != "call-1" {
		t.Fatalf("tool results were not sent back: %+v", second.Messages)
	}

	var results []hex.AIContent
	for _, event := range events {
		if event.Type == hex.EventToolResult {
			results = append(results, *event.Content)
		}
	}
	if len(results) != 2 || results[0].IsError || !strings.Contains(results[0].Text, `"for":"person"`) {
		t.Fatalf("unexpected tool results: %+v", results)
	}
	if !results[1].IsError {
		t.Fatal("a tool the caller may not use was run instead of refused")
	}
	last := events[len(events)-1]
	if last.Type != hex.EventDone || last.StopReason != hex.StopEndTurn || last.Usage.InputTokens != 20 {
		t.Fatalf("unexpected final event: %+v", last)
	}

	totals, err := usage.AIUsageTotals(context.Background(), hex.AIUsageFilter{Site: "demo"}, hex.GroupByCaller)
	if err != nil || len(totals) != 1 || totals[0].Key != "user:person" || totals[0].Calls != 2 || totals[0].CostMicros != 70 {
		t.Fatalf("each model call must be recorded with its cost: %+v %v", totals, err)
	}
}

func TestAIBudgets(t *testing.T) {
	turn := func() []hex.AIEvent {
		return assistantTurn(hex.StopEndTurn, hex.AIContent{Type: hex.ContentText, Text: "Hi"})
	}
	provider := &scriptedProvider{turns: [][]hex.AIEvent{turn(), turn(), turn(), turn(), turn(), turn()}}
	// Each call costs 35 micro-dollars; sites may spend 100.
	server, usage := setupAI(t, provider, hex.AILimits{SiteMonthly: 0.0001, PersonMonthly: 0.00004})
	body := []byte(`{"model":"general","messages":[{"role":"user","content":[{"type":"text","text":"Hi"}]}]}`)
	call := func(headers http.Header, site string, want int) string {
		t.Helper()
		return requestAs(t, server, headers, "POST", "/api/sites/"+site+"/ai/complete", body, want).Body.String()
	}

	alice := roleHeaders("alice")
	call(alice, "demo", 200)
	call(alice, "demo", 200)
	if refused := call(alice, "demo", 429); !strings.Contains(refused, "Your AI budget") {
		t.Fatalf("unexpected refusal: %s", refused)
	}

	// A role override lifts the person limit; the site limit still applies.
	limit := int64(1_000_000)
	if err := usage.PutAIBudget(context.Background(), hex.AIBudget{Scope: hex.BudgetPerson, Subject: "role:Premium", LimitMicros: &limit}); err != nil {
		t.Fatal(err)
	}
	rich := roleHeaders("rich", "Premium")
	call(rich, "demo", 200)
	if refused := call(rich, "demo", 429); !strings.Contains(refused, "This site's AI budget") {
		t.Fatalf("the site limit did not apply: %s", refused)
	}
	call(rich, "other", 200)

	if err := usage.PutAIBudget(context.Background(), hex.AIBudget{Scope: hex.BudgetSite, Subject: "other", Disabled: true}); err != nil {
		t.Fatal(err)
	}
	if refused := call(rich, "other", 403); !strings.Contains(refused, "turned off") {
		t.Fatalf("AI was not turned off for the site: %s", refused)
	}

	platform := int64(140)
	if err := usage.PutAIBudget(context.Background(), hex.AIBudget{Scope: hex.BudgetPlatform, LimitMicros: &platform}); err != nil {
		t.Fatal(err)
	}
	if refused := call(rich, "third", 429); !strings.Contains(refused, "The platform's AI budget") {
		t.Fatalf("the platform limit did not apply: %s", refused)
	}
}

func TestAIReturnsAppToolsAndEnforcesLimits(t *testing.T) {
	provider := &scriptedProvider{turns: [][]hex.AIEvent{
		assistantTurn(hex.StopToolUse,
			hex.AIContent{Type: hex.ContentToolCall, ToolCallID: "call-1", Name: "pick_color", Input: json.RawMessage(`{}`)}),
	}}
	// Each scripted call costs 10 input and 5 output tokens: 35 micro-dollars.
	server, _ := setupAI(t, provider, hex.AILimits{PersonMonthly: 0.00003})
	person := roleHeaders("person")

	body := `{"model":"general","messages":[{"role":"user","content":[{"type":"text","text":"Pick"}]}],
		"tools":[{"name":"pick_color","description":"Ask the user for a color.","inputSchema":{"type":"object"}}]}`
	completion := requestAs(t, server, person, "POST", "/api/sites/demo/ai/complete", []byte(body), 200)
	var result hex.AICompletion
	if err := json.Unmarshal(completion.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.StopReason != hex.StopToolUse || len(result.Messages) != 1 || len(provider.requests) != 1 {
		t.Fatalf("app tools must be returned to the app: %s", completion.Body.String())
	}

	requestAs(t, server, person, "POST", "/api/sites/demo/ai/complete", []byte(body), 429)
	requestAs(t, server, roleHeaders("other"), "POST", "/api/sites/demo/ai/complete",
		[]byte(`{"model":"premium","messages":[{"role":"user","content":[{"type":"text","text":"Hi"}]}]}`), 400)
	requestAs(t, server, roleHeaders("rich", "Premium"), "POST", "/api/sites/demo/ai/complete",
		[]byte(`{"model":"general","messages":[{"role":"assistant","content":[{"type":"text","text":"Hi"}]}]}`), 400)
}

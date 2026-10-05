package openai

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	hex "github.com/crazycatviking/hex/server"
)

const toolStream = `data: {"choices":[{"index":0,"delta":{"role":"assistant","reasoning_content":"Thinking"}}]}

data: {"choices":[{"index":0,"delta":{"content":"Let me "}}]}

data: {"choices":[{"index":0,"delta":{"content":"check."}}]}

data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"crm__deals","arguments":""}}]}}]}

data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"stage\":"}}]}}]}

data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":1,"id":"call_2","type":"function","function":{"name":"other","arguments":"{}"}}]}}]}

data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"won\"}"}}]}}]}

data: {"choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}

data: {"choices":[],"usage":{"prompt_tokens":30,"completion_tokens":12}}

data: [DONE]

`

func testProvider(t *testing.T, handler http.HandlerFunc) *Provider {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	provider, err := New(Config{
		BaseURL: server.URL + "/openai/v1", APIKey: "key",
		Models: []Model{{AIModel: hex.AIModel{ID: "gpt", Thinking: true, Tools: true}, Upstream: "gpt-deployment"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return provider
}

func collect(t *testing.T, stream hex.AIStream) []hex.AIEvent {
	t.Helper()
	defer stream.Close()
	var events []hex.AIEvent
	for {
		event, err := stream.Next()
		if errors.Is(err, io.EOF) {
			return events
		}
		if err != nil {
			t.Fatal(err)
		}
		events = append(events, event)
	}
}

func TestStreamMapsRequestAndEvents(t *testing.T) {
	var sent json.RawMessage
	provider := testProvider(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/openai/v1/chat/completions" || r.Header.Get("Authorization") != "Bearer key" {
			t.Errorf("unexpected request %s %v", r.URL.Path, r.Header)
		}
		sent, _ = io.ReadAll(r.Body)
		io.WriteString(w, toolStream)
	})
	stream, err := provider.Stream(context.Background(), hex.AIRequest{
		Model: "gpt", System: "Be brief.", MaxTokens: 500,
		Thinking: &hex.AIThinking{Effort: "xhigh", Show: true},
		Tools:    []hex.AITool{{Name: "crm__deals", Description: "List deals.", InputSchema: json.RawMessage(`{"type":"object"}`)}},
		Messages: []hex.AIMessage{
			{Role: hex.RoleUser, Content: []hex.AIContent{{Type: hex.ContentText, Text: "Deals?"}, {Type: hex.ContentImage, MediaType: "image/png", Data: "iVBO"}}},
			{Role: hex.RoleAssistant, Content: []hex.AIContent{
				{Type: hex.ContentThinking, Text: "secret"},
				{Type: hex.ContentToolCall, ToolCallID: "t0", Name: "crm__deals", Input: json.RawMessage(`{"a":1}`)},
			}},
			{Role: hex.RoleUser, Content: []hex.AIContent{
				{Type: hex.ContentToolResult, ToolCallID: "t0", Text: "boom", IsError: true},
				{Type: hex.ContentText, Text: "and now?"},
			}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	events := collect(t, stream)

	for _, want := range []string{
		`"model":"gpt-deployment"`, `"stream":true`, `"stream_options":{"include_usage":true}`, `"max_completion_tokens":500`,
		`"reasoning_effort":"high"`, `{"role":"system","content":"Be brief."}`,
		`{"type":"image_url","image_url":{"url":"data:image/png;base64,iVBO"}}`,
		`"tool_calls":[{"id":"t0","type":"function","function":{"name":"crm__deals","arguments":"{\"a\":1}"}}]`,
		`{"role":"tool","content":"Error: boom","tool_call_id":"t0"},{"role":"user","content":"and now?"}`,
		`"tools":[{"type":"function","function":{"name":"crm__deals","description":"List deals.","parameters":{"type":"object"}}}]`,
	} {
		if !strings.Contains(string(sent), want) {
			t.Errorf("request lacks %s: %s", want, sent)
		}
	}
	if strings.Contains(string(sent), "secret") {
		t.Error("thinking was replayed")
	}

	var types []string
	for _, event := range events {
		types = append(types, event.Type)
	}
	if got := strings.Join(types, ","); got != "thinking,text,text,tool_call,tool_call,message,done" {
		t.Fatalf("unexpected events %s", got)
	}
	if call := events[3].Content; call.ToolCallID != "call_1" || string(call.Input) != `{"stage":"won"}` {
		t.Fatalf("unexpected first tool call %+v", call)
	}
	message := events[5].Message
	if len(message.Content) != 3 || message.Content[0].Text != "Let me check." {
		t.Fatalf("unexpected message %+v", message)
	}
	done := events[6]
	if done.StopReason != hex.StopToolUse || done.Usage.InputTokens != 30 || done.Usage.OutputTokens != 12 {
		t.Fatalf("unexpected done %+v", done)
	}
}

func TestStreamEndingsAndErrors(t *testing.T) {
	cases := map[string]struct {
		body   string
		stop   string
		failed bool
	}{
		"no done marker": {body: "data: {\"choices\":[{\"delta\":{\"content\":\"hi\"},\"finish_reason\":\"length\"}]}\n\n", stop: hex.StopMaxTokens},
		"filtered":       {body: "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"content_filter\"}]}\n\ndata: [DONE]\n\n", stop: hex.StopRefusal},
		"truncated":      {body: "data: {\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\n", failed: true},
		"error chunk":    {body: "data: {\"error\":{\"message\":\"overloaded\"}}\n\n", failed: true},
		"malformed":      {body: "data: {nope\n\n", failed: true},
	}
	for name, test := range cases {
		provider := testProvider(t, func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, test.body) })
		stream, err := provider.Stream(context.Background(), hex.AIRequest{Model: "gpt"})
		if err != nil {
			t.Fatal(err)
		}
		var last hex.AIEvent
		var streamErr error
		for {
			event, err := stream.Next()
			if err != nil {
				if !errors.Is(err, io.EOF) {
					streamErr = err
				}
				break
			}
			last = event
		}
		stream.Close()
		if test.failed != (streamErr != nil) {
			t.Errorf("%s: unexpected error %v", name, streamErr)
		}
		if !test.failed && last.StopReason != test.stop {
			t.Errorf("%s: stop reason %q, want %q", name, last.StopReason, test.stop)
		}
	}

	limited := testProvider(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusBadRequest) })
	_, err := limited.Stream(context.Background(), hex.AIRequest{Model: "gpt"})
	var aiError *hex.AIError
	if !errors.As(err, &aiError) || aiError.Status != 400 {
		t.Fatalf("expected a request error, got %v", err)
	}
}

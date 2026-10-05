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

const responsesToolStream = `event: response.created
data: {"type":"response.created","response":{"status":"in_progress"}}

event: response.output_item.added
data: {"type":"response.output_item.added","output_index":0,"item":{"type":"reasoning","id":"rs_1"}}

event: response.reasoning_summary_part.added
data: {"type":"response.reasoning_summary_part.added","item_id":"rs_1","summary_index":0}

event: response.reasoning_summary_text.delta
data: {"type":"response.reasoning_summary_text.delta","item_id":"rs_1","summary_index":0,"delta":"Looking at "}

event: response.reasoning_summary_text.delta
data: {"type":"response.reasoning_summary_text.delta","item_id":"rs_1","summary_index":0,"delta":"deals."}

event: response.reasoning_summary_part.added
data: {"type":"response.reasoning_summary_part.added","item_id":"rs_1","summary_index":1}

event: response.reasoning_summary_text.delta
data: {"type":"response.reasoning_summary_text.delta","item_id":"rs_1","summary_index":1,"delta":"Then stages."}

event: response.output_item.done
data: {"type":"response.output_item.done","output_index":0,"item":{"type":"reasoning","id":"rs_1","encrypted_content":"ENC","summary":[{"type":"summary_text","text":"Looking at deals."},{"type":"summary_text","text":"Then stages."}]}}

event: response.output_text.delta
data: {"type":"response.output_text.delta","item_id":"msg_1","output_index":1,"content_index":0,"delta":"Let me "}

event: response.output_text.delta
data: {"type":"response.output_text.delta","item_id":"msg_1","output_index":1,"content_index":0,"delta":"check."}

event: response.output_item.done
data: {"type":"response.output_item.done","output_index":1,"item":{"type":"message","id":"msg_1","role":"assistant","content":[{"type":"output_text","text":"Let me check."}]}}

event: response.function_call_arguments.delta
data: {"type":"response.function_call_arguments.delta","item_id":"fc_1","output_index":2,"delta":"{\"stage\":"}

event: response.output_item.done
data: {"type":"response.output_item.done","output_index":2,"item":{"type":"function_call","id":"fc_1","call_id":"call_1","name":"crm__deals","arguments":"{\"stage\":\"won\"}"}}

event: response.completed
data: {"type":"response.completed","response":{"status":"completed","usage":{"input_tokens":30,"input_tokens_details":{"cached_tokens":8},"output_tokens":12}}}

`

func testResponsesProvider(t *testing.T, handler http.HandlerFunc) *Provider {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	provider, err := New(Config{
		BaseURL: server.URL + "/openai/v1", APIKey: "key", API: APIResponses,
		Models: []Model{{AIModel: hex.AIModel{ID: "gpt", Thinking: true, Tools: true}, Upstream: "gpt-deployment"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return provider
}

func TestResponsesMapsRequestAndEvents(t *testing.T) {
	var sent json.RawMessage
	provider := testResponsesProvider(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/openai/v1/responses" || r.Header.Get("Authorization") != "Bearer key" {
			t.Errorf("unexpected request %s %v", r.URL.Path, r.Header)
		}
		sent, _ = io.ReadAll(r.Body)
		io.WriteString(w, responsesToolStream)
	})
	stream, err := provider.Stream(context.Background(), hex.AIRequest{
		Model: "gpt", System: "Be brief.", MaxTokens: 500,
		Thinking: &hex.AIThinking{Effort: "high", Show: true},
		Tools:    []hex.AITool{{Name: "crm__deals", Description: "List deals.", InputSchema: json.RawMessage(`{"type":"object"}`)}},
		Messages: []hex.AIMessage{
			{Role: hex.RoleUser, Content: []hex.AIContent{{Type: hex.ContentText, Text: "Deals?"}, {Type: hex.ContentImage, MediaType: "image/png", Data: "iVBO"}}},
			{Role: hex.RoleAssistant, Content: []hex.AIContent{
				{Type: hex.ContentThinking, Text: "Earlier.", Signature: `{"id":"rs_0","encrypted_content":"OLD"}`},
				{Type: hex.ContentThinking, Text: "claude", Signature: "anthropic-signature"},
				{Type: hex.ContentText, Text: "Checking."},
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
		`"model":"gpt-deployment"`, `"stream":true`, `"store":false`, `"include":["reasoning.encrypted_content"]`,
		`"instructions":"Be brief."`, `"max_output_tokens":500`, `"reasoning":{"effort":"high","summary":"auto"}`,
		`{"role":"user","content":[{"type":"input_text","text":"Deals?"},{"type":"input_image","image_url":"data:image/png;base64,iVBO"}]}`,
		`{"type":"reasoning","id":"rs_0","encrypted_content":"OLD","summary":[{"type":"summary_text","text":"Earlier."}]},{"role":"assistant","content":"Checking."},{"type":"function_call","call_id":"t0","name":"crm__deals","arguments":"{\"a\":1}"}`,
		`{"type":"function_call_output","call_id":"t0","output":"Error: boom"},{"role":"user","content":[{"type":"input_text","text":"and now?"}]}`,
		`"tools":[{"type":"function","name":"crm__deals","description":"List deals.","parameters":{"type":"object"},"strict":false}]`,
	} {
		if !strings.Contains(string(sent), want) {
			t.Errorf("request lacks %s: %s", want, sent)
		}
	}
	if strings.Contains(string(sent), "anthropic-signature") {
		t.Error("another protocol's thinking was replayed")
	}

	var types []string
	var thinking strings.Builder
	for _, event := range events {
		types = append(types, event.Type)
		if event.Type == hex.EventThinking {
			thinking.WriteString(event.Text)
		}
	}
	if got := strings.Join(types, ","); got != "thinking,thinking,thinking,thinking,text,text,tool_call,message,done" {
		t.Fatalf("unexpected events %s", got)
	}
	if thinking.String() != "Looking at deals.\n\nThen stages." {
		t.Fatalf("unexpected thinking %q", thinking.String())
	}
	if call := events[6].Content; call.ToolCallID != "call_1" || string(call.Input) != `{"stage":"won"}` {
		t.Fatalf("unexpected tool call %+v", call)
	}
	message := events[7].Message
	if len(message.Content) != 3 || message.Content[0].Type != hex.ContentThinking || message.Content[1].Text != "Let me check." {
		t.Fatalf("unexpected message %+v", message)
	}
	if message.Content[0].Signature != `{"id":"rs_1","encrypted_content":"ENC"}` || message.Content[0].Text != "Looking at deals.\n\nThen stages." {
		t.Fatalf("unexpected reasoning %+v", message.Content[0])
	}
	done := events[8]
	if done.StopReason != hex.StopToolUse || done.Usage.InputTokens != 22 || done.Usage.CachedInputTokens != 8 || done.Usage.OutputTokens != 12 {
		t.Fatalf("unexpected done %+v", done)
	}
}

func TestResponsesHiddenReasoningIsKept(t *testing.T) {
	var sent json.RawMessage
	provider := testResponsesProvider(t, func(w http.ResponseWriter, r *http.Request) {
		sent, _ = io.ReadAll(r.Body)
		io.WriteString(w, responsesToolStream)
	})
	stream, err := provider.Stream(context.Background(), hex.AIRequest{
		Model: "gpt", Thinking: &hex.AIThinking{Effort: "low"},
		Messages: []hex.AIMessage{{Role: hex.RoleUser, Content: []hex.AIContent{{Type: hex.ContentText, Text: "Hi"}}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	events := collect(t, stream)
	if !strings.Contains(string(sent), `"reasoning":{"effort":"low"}`) {
		t.Errorf("unexpected reasoning settings: %s", sent)
	}
	for _, event := range events {
		if event.Type == hex.EventThinking {
			t.Fatal("thinking was shown without being asked for")
		}
		if event.Type == hex.EventMessage {
			reasoning := event.Message.Content[0]
			if reasoning.Type != hex.ContentThinking || reasoning.Text != "" || reasoning.Signature == "" {
				t.Fatalf("hidden reasoning was not kept for the next turn: %+v", reasoning)
			}
		}
	}
}

func TestResponsesEndingsAndErrors(t *testing.T) {
	const textItem = `data: {"type":"response.output_item.done","item":{"type":"message","id":"m","content":[{"type":"output_text","text":"hi"}]}}` + "\n\n"
	cases := map[string]struct {
		body   string
		stop   string
		text   string
		failed bool
	}{
		"text without deltas": {body: textItem + `data: {"type":"response.completed","response":{"status":"completed"}}` + "\n\n", stop: hex.StopEndTurn, text: "hi"},
		"max tokens":          {body: `data: {"type":"response.incomplete","response":{"status":"incomplete","incomplete_details":{"reason":"max_output_tokens"}}}` + "\n\n", stop: hex.StopMaxTokens},
		"filtered":            {body: `data: {"type":"response.incomplete","response":{"status":"incomplete","incomplete_details":{"reason":"content_filter"}}}` + "\n\n", stop: hex.StopRefusal},
		"refusal": {body: `data: {"type":"response.output_item.done","item":{"type":"message","id":"m","content":[{"type":"refusal","refusal":"No."}]}}` + "\n\n" +
			`data: {"type":"response.completed","response":{"status":"completed"}}` + "\n\n", stop: hex.StopRefusal, text: "No."},
		"truncated":   {body: textItem, failed: true},
		"failed":      {body: `data: {"type":"response.failed","response":{"status":"failed","error":{"code":"server_error","message":"overloaded"}}}` + "\n\n", failed: true},
		"error event": {body: "event: error\ndata: {\"code\":\"rate_limit\",\"message\":\"slow down\"}\n\n", failed: true},
		"malformed":   {body: "data: {nope\n\n", failed: true},
	}
	for name, test := range cases {
		provider := testResponsesProvider(t, func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, test.body) })
		stream, err := provider.Stream(context.Background(), hex.AIRequest{Model: "gpt"})
		if err != nil {
			t.Fatal(err)
		}
		var last hex.AIEvent
		var text strings.Builder
		var streamErr error
		for {
			event, err := stream.Next()
			if err != nil {
				if !errors.Is(err, io.EOF) {
					streamErr = err
				}
				break
			}
			if event.Type == hex.EventText {
				text.WriteString(event.Text)
			}
			last = event
		}
		stream.Close()
		if test.failed != (streamErr != nil) {
			t.Errorf("%s: unexpected error %v", name, streamErr)
		}
		if !test.failed && (last.StopReason != test.stop || text.String() != test.text) {
			t.Errorf("%s: stop reason %q and text %q, want %q and %q", name, last.StopReason, text.String(), test.stop, test.text)
		}
	}
}

func TestUnknownAPIIsRejected(t *testing.T) {
	_, err := New(Config{BaseURL: "https://example.com/v1", APIKey: "key", API: "assistants", Models: []Model{{AIModel: hex.AIModel{ID: "gpt"}}}})
	if err == nil {
		t.Fatal("an unknown API was accepted")
	}
}

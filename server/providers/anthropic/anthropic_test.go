package anthropic

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

const toolStream = `event: message_start
data: {"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","content":[],"usage":{"input_tokens":20,"cache_read_input_tokens":5,"output_tokens":1}}}

event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":"","signature":""}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"Look up "}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"deals."}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"sig-abc"}}

event: content_block_stop
data: {"type":"content_block_stop","index":0}

event: ping
data: {"type":"ping"}

event: content_block_start
data: {"type":"content_block_start","index":1,"content_block":{"type":"text","text":""}}

event: content_block_delta
data: {"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"Checking"}}

event: content_block_stop
data: {"type":"content_block_stop","index":1}

event: content_block_start
data: {"type":"content_block_start","index":2,"content_block":{"type":"tool_use","id":"toolu_1","name":"crm__deals","input":{}}}

event: content_block_delta
data: {"type":"content_block_delta","index":2,"delta":{"type":"input_json_delta","partial_json":"{\"stage\":"}}

event: content_block_delta
data: {"type":"content_block_delta","index":2,"delta":{"type":"input_json_delta","partial_json":" \"won\"}"}}

event: content_block_stop
data: {"type":"content_block_stop","index":2}

event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":42}}

event: message_stop
data: {"type":"message_stop"}

`

func testProvider(t *testing.T, handler http.HandlerFunc) *Provider {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	provider, err := New(Config{
		BaseURL: server.URL, APIKey: "key",
		Models: []Model{{AIModel: hex.AIModel{ID: "opus", Name: "Opus", Thinking: true, Tools: true}, Upstream: "claude-opus-5-5"}},
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
	var sent map[string]any
	provider := testProvider(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/messages" || r.Header.Get("x-api-key") != "key" || r.Header.Get("anthropic-version") != "2023-06-01" {
			t.Errorf("unexpected request %s %v", r.URL.Path, r.Header)
		}
		if err := json.NewDecoder(r.Body).Decode(&sent); err != nil {
			t.Error(err)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, toolStream)
	})

	stream, err := provider.Stream(context.Background(), hex.AIRequest{
		Model: "opus", System: "Be brief.", MaxTokens: 1000,
		Thinking: &hex.AIThinking{Effort: "high", Show: true},
		Tools:    []hex.AITool{{Name: "crm__deals", Description: "List deals.", InputSchema: json.RawMessage(`{"type":"object"}`)}},
		Messages: []hex.AIMessage{
			{Role: hex.RoleUser, Content: []hex.AIContent{{Type: hex.ContentText, Text: "Deals?"}, {Type: hex.ContentImage, MediaType: "image/png", Data: "iVBO"}}},
			{Role: hex.RoleAssistant, Content: []hex.AIContent{
				{Type: hex.ContentThinking, Text: "hmm", Signature: "s1"},
				{Type: hex.ContentThinking, Redacted: "opaque"},
				{Type: hex.ContentThinking, Text: "unsigned"},
				{Type: hex.ContentToolCall, ToolCallID: "t0", Name: "crm__deals"},
			}},
			{Role: hex.RoleUser, Content: []hex.AIContent{{Type: hex.ContentToolResult, ToolCallID: "t0", Text: "[]", IsError: true}}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	events := collect(t, stream)

	encoded, _ := json.Marshal(sent)
	for _, want := range []string{
		`"model":"claude-opus-5-5"`, `"stream":true`, `"max_tokens":1000`, `"system":"Be brief."`,
		`"thinking":{"display":"summarized","type":"adaptive"}`, `"output_config":{"effort":"high"}`,
		`"input_schema":{"type":"object"}`, `"source":{"data":"iVBO","media_type":"image/png","type":"base64"}`,
		`{"signature":"s1","thinking":"hmm","type":"thinking"}`, `{"data":"opaque","type":"redacted_thinking"}`,
		`{"id":"t0","input":{},"name":"crm__deals","type":"tool_use"}`,
		`{"content":"[]","is_error":true,"tool_use_id":"t0","type":"tool_result"}`,
	} {
		if !strings.Contains(string(encoded), want) {
			t.Errorf("request lacks %s: %s", want, encoded)
		}
	}
	for _, unwanted := range []string{"unsigned", "budget_tokens", "temperature", "eager_input_streaming"} {
		if strings.Contains(string(encoded), unwanted) {
			t.Errorf("request contains %s", unwanted)
		}
	}

	var types []string
	for _, event := range events {
		types = append(types, event.Type)
	}
	if got := strings.Join(types, ","); got != "thinking,thinking,text,tool_call,message,done" {
		t.Fatalf("unexpected events %s", got)
	}
	call := events[3].Content
	if call.ToolCallID != "toolu_1" || string(call.Input) != `{"stage": "won"}` {
		t.Fatalf("unexpected tool call %+v", call)
	}
	message := events[4].Message
	if len(message.Content) != 3 || message.Content[0].Text != "Look up deals." || message.Content[0].Signature != "sig-abc" || message.Content[1].Text != "Checking" {
		t.Fatalf("unexpected message %+v", message)
	}
	done := events[5]
	if done.StopReason != hex.StopToolUse || done.Usage.InputTokens != 25 || done.Usage.OutputTokens != 42 {
		t.Fatalf("unexpected done %+v %+v", done, done.Usage)
	}
}

func TestStreamErrors(t *testing.T) {
	limited := testProvider(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		io.WriteString(w, `{"type":"error","error":{"type":"rate_limit_error"}}`)
	})
	_, err := limited.Stream(context.Background(), hex.AIRequest{Model: "opus", Messages: []hex.AIMessage{{Role: "user", Content: []hex.AIContent{{Type: "text", Text: "hi"}}}}})
	var aiError *hex.AIError
	if !errors.As(err, &aiError) || aiError.Status != 429 {
		t.Fatalf("expected a rate limit error, got %v", err)
	}
	if _, err := limited.Stream(context.Background(), hex.AIRequest{Model: "missing"}); !errors.As(err, &aiError) || aiError.Status != 400 {
		t.Fatalf("expected an unknown model error, got %v", err)
	}

	for name, body := range map[string]string{
		"overloaded": "event: error\ndata: {\"type\":\"error\",\"error\":{\"type\":\"overloaded_error\",\"message\":\"Overloaded\"}}\n\n",
		"truncated":  "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"usage\":{}}}\n\n",
		"malformed":  "event: message_start\ndata: {not json\n\n",
	} {
		provider := testProvider(t, func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, body) })
		stream, err := provider.Stream(context.Background(), hex.AIRequest{Model: "opus"})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := stream.Next(); err == nil {
			t.Errorf("%s: expected a stream error", name)
		}
		stream.Close()
	}
}

func TestRefusalAndInvalidToolInput(t *testing.T) {
	body := `event: message_start
data: {"type":"message_start","message":{"usage":{"input_tokens":3}}}

event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"t","name":"x"}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"cut"}}

event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"refusal"},"usage":{"output_tokens":2}}

event: message_stop
data: {"type":"message_stop"}
`
	provider := testProvider(t, func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, body) })
	stream, err := provider.Stream(context.Background(), hex.AIRequest{Model: "opus"})
	if err != nil {
		t.Fatal(err)
	}
	events := collect(t, stream)
	if len(events) != 2 || string(events[0].Message.Content[0].Input) != "{}" || events[1].StopReason != hex.StopRefusal {
		t.Fatalf("unexpected events %+v", events)
	}
}

func TestStreamHonorsCancellation(t *testing.T) {
	release := make(chan struct{})
	provider := testProvider(t, func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"usage\":{}}}\n\n")
		w.(http.Flusher).Flush()
		select {
		case <-r.Context().Done():
		case <-release:
		}
	})
	defer close(release)
	ctx, cancel := context.WithCancel(context.Background())
	stream, err := provider.Stream(ctx, hex.AIRequest{Model: "opus"})
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	cancel()
	if _, err := stream.Next(); err == nil {
		t.Fatal("a cancelled stream kept reading")
	}
}

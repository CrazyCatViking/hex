package foundry

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/policy"
	hex "github.com/crazycatviking/hex/server"
)

type fakeCredential struct {
	calls atomic.Int32
}

func (c *fakeCredential) GetToken(_ context.Context, options policy.TokenRequestOptions) (azcore.AccessToken, error) {
	c.calls.Add(1)
	if len(options.Scopes) != 1 || options.Scopes[0] != "https://cognitiveservices.azure.com/.default" {
		return azcore.AccessToken{}, errors.New("unexpected scope")
	}
	return azcore.AccessToken{Token: "entra-token", ExpiresOn: time.Now().Add(time.Hour)}, nil
}

const anthropicBody = "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":1}}}\n\n" +
	"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n" +
	"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"claude\"}}\n\n" +
	"event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n" +
	"event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":1}}\n\n" +
	"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"

const openaiBody = "data: {\"choices\":[{\"delta\":{\"content\":\"gpt\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n"

const responsesBody = "data: {\"type\":\"response.output_text.delta\",\"item_id\":\"m\",\"delta\":\"gpt-6\"}\n\n" +
	"data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\"}}\n\n"

func firstText(t *testing.T, stream hex.AIStream) string {
	t.Helper()
	defer stream.Close()
	for {
		event, err := stream.Next()
		if err != nil {
			if errors.Is(err, io.EOF) {
				t.Fatal("no text event")
			}
			t.Fatal(err)
		}
		if event.Type == hex.EventText {
			return event.Text
		}
	}
}

func TestRoutesModelsByProtocolWithEntraTokens(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer entra-token" {
			t.Errorf("unexpected authorization %q", r.Header.Get("Authorization"))
		}
		switch r.URL.Path {
		case "/anthropic/v1/messages":
			io.WriteString(w, anthropicBody)
		case "/openai/v1/chat/completions":
			io.WriteString(w, openaiBody)
		case "/openai/v1/responses":
			io.WriteString(w, responsesBody)
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
		}
	}))
	defer server.Close()

	models, err := ModelsFromJSON(`[
		{"id":"claude-opus-5-5","name":"Claude Opus 5.5","protocol":"anthropic","thinking":true,"tools":true},
		{"id":"gpt","protocol":"openai","deployment":"gpt-prod","restricted":true},
		{"id":"gpt-6","protocol":"openai-responses","thinking":true,"tools":true}
	]`)
	if err != nil {
		t.Fatal(err)
	}
	credential := &fakeCredential{}
	provider, err := New(Config{Endpoint: server.URL, Credential: credential, Models: models, HTTPClient: server.Client()})
	if err != nil {
		t.Fatal(err)
	}

	listed, _ := provider.Models(context.Background())
	if len(listed) != 3 || listed[1].Name != "gpt" || !listed[1].Restricted || !listed[0].Thinking {
		t.Fatalf("unexpected models %+v", listed)
	}
	for model, want := range map[string]string{"claude-opus-5-5": "claude", "gpt": "gpt", "gpt-6": "gpt-6"} {
		stream, err := provider.Stream(context.Background(), hex.AIRequest{Model: model})
		if err != nil {
			t.Fatal(err)
		}
		if got := firstText(t, stream); got != want {
			t.Errorf("%s answered %q", model, got)
		}
	}
	if credential.calls.Load() != 1 {
		t.Fatalf("the token was requested %d times", credential.calls.Load())
	}
	var aiError *hex.AIError
	if _, err := provider.Stream(context.Background(), hex.AIRequest{Model: "missing"}); !errors.As(err, &aiError) {
		t.Fatalf("expected an unknown model error, got %v", err)
	}
}

func TestAPIKeyHeadersAndValidation(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/anthropic/v1/messages":
			if r.Header.Get("x-api-key") != "secret" {
				t.Error("missing x-api-key")
			}
			io.WriteString(w, anthropicBody)
		default:
			if r.Header.Get("api-key") != "secret" {
				t.Error("missing api-key")
			}
			io.WriteString(w, openaiBody)
		}
	}))
	defer server.Close()
	provider, err := New(Config{Endpoint: server.URL, APIKey: "secret", HTTPClient: server.Client(), Models: []Model{
		{AIModel: hex.AIModel{ID: "claude"}, Protocol: ProtocolAnthropic},
		{AIModel: hex.AIModel{ID: "gpt"}, Protocol: ProtocolOpenAI},
	}})
	if err != nil {
		t.Fatal(err)
	}
	for _, model := range []string{"claude", "gpt"} {
		stream, err := provider.Stream(context.Background(), hex.AIRequest{Model: model})
		if err != nil {
			t.Fatal(err)
		}
		firstText(t, stream)
	}

	invalid := []Config{
		{Endpoint: "http://insecure.example", APIKey: "k", Models: []Model{{AIModel: hex.AIModel{ID: "a"}, Protocol: "openai"}}},
		{Endpoint: "https://x.example", Models: []Model{{AIModel: hex.AIModel{ID: "a"}, Protocol: "openai"}}},
		{Endpoint: "https://x.example", APIKey: "k", Models: []Model{{AIModel: hex.AIModel{ID: "a"}, Protocol: "gemini"}}},
		{Endpoint: "https://x.example", APIKey: "k", Models: []Model{{AIModel: hex.AIModel{ID: "a"}, Protocol: "openai"}, {AIModel: hex.AIModel{ID: "a"}, Protocol: "anthropic"}}},
		{Endpoint: "https://x.example", APIKey: "k"},
	}
	for index, config := range invalid {
		if _, err := New(config); err == nil {
			t.Errorf("config %d was accepted", index)
		}
	}
	if _, err := ModelsFromJSON(`{"id":"x"}`); err == nil {
		t.Error("a non-array model list was accepted")
	}
}

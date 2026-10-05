// Package openai is a Hex AI provider for OpenAI-compatible APIs, including
// the Azure OpenAI v1 API and other models in Azure AI Foundry
// ({endpoint}/openai/v1). It speaks Chat Completions by default or, with
// API set to "responses", the Responses API.
//
// Chat Completions: reasoning text that a model streams as
// reasoning_content or reasoning is shown as thinking when the app asks for
// it; it has no way to send reasoning back, so thinking blocks are dropped
// when a conversation is replayed. Reasoning models such as GPT-5 and GPT-6
// refuse function tools together with a reasoning effort there.
//
// Responses: tools and reasoning work together, reasoning summaries stream
// as thinking when the app asks to show it, and reasoning is carried
// between turns as encrypted content (requests are not stored by the
// service).
package openai

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	hex "github.com/crazycatviking/hex/server"
)

const maxErrorBytes = 64 << 10

// Model is a model apps can choose; Upstream is the model or deployment
// name sent to the API, defaulting to the Hex model ID.
type Model struct {
	hex.AIModel
	Upstream string `json:"upstream,omitempty"`
}

func (m Model) upstream() string {
	if m.Upstream == "" {
		return m.ID
	}
	return m.Upstream
}

// The APIs a provider can speak.
const (
	APIChatCompletions = "chat"
	APIResponses       = "responses"
)

// Config configures the provider. BaseURL is everything before
// /chat/completions or /responses, such as https://api.openai.com/v1.
// API is APIChatCompletions (the default) or APIResponses. Authorize adds
// credentials and defaults to a bearer APIKey.
type Config struct {
	BaseURL    string
	API        string
	APIKey     string
	Authorize  func(*http.Request) error
	Models     []Model
	HTTPClient *http.Client
}

// Provider implements hex.AIProvider.
type Provider struct {
	config Config
	models map[string]Model
}

func New(config Config) (*Provider, error) {
	if config.BaseURL == "" {
		return nil, errors.New("openai: a base URL is required")
	}
	config.BaseURL = strings.TrimSuffix(config.BaseURL, "/")
	switch config.API {
	case "":
		config.API = APIChatCompletions
	case APIChatCompletions, APIResponses:
	default:
		return nil, fmt.Errorf("openai: API must be %q or %q, got %q", APIChatCompletions, APIResponses, config.API)
	}
	if config.HTTPClient == nil {
		config.HTTPClient = http.DefaultClient
	}
	if config.Authorize == nil {
		if config.APIKey == "" {
			return nil, errors.New("openai: an API key or an Authorize function is required")
		}
		key := config.APIKey
		config.Authorize = func(request *http.Request) error {
			request.Header.Set("Authorization", "Bearer "+key)
			return nil
		}
	}
	if len(config.Models) == 0 {
		return nil, errors.New("openai: at least one model is required")
	}
	models := make(map[string]Model, len(config.Models))
	for _, model := range config.Models {
		if model.ID == "" {
			return nil, errors.New("openai: every model needs an ID")
		}
		if _, exists := models[model.ID]; exists {
			return nil, fmt.Errorf("openai: model %s is listed twice", model.ID)
		}
		models[model.ID] = model
	}
	return &Provider{config: config, models: models}, nil
}

func (p *Provider) Models(context.Context) ([]hex.AIModel, error) {
	result := make([]hex.AIModel, 0, len(p.config.Models))
	for _, model := range p.config.Models {
		result = append(result, model.AIModel)
	}
	return result, nil
}

func (p *Provider) Stream(ctx context.Context, request hex.AIRequest) (hex.AIStream, error) {
	model, exists := p.models[request.Model]
	if !exists {
		return nil, &hex.AIError{Status: http.StatusBadRequest, Message: fmt.Sprintf("model %q is not configured", request.Model)}
	}
	showThinking := request.Thinking != nil && request.Thinking.Show
	path, label := "/chat/completions", "chat completion"
	var payload any = completionRequest(model, request)
	if p.config.API == APIResponses {
		path, label = "/responses", "response"
		payload = responsesRequest(model, request)
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("encode %s request: %w", label, err)
	}
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, p.config.BaseURL+path, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpRequest.Header.Set("Content-Type", "application/json")
	httpRequest.Header.Set("Accept", "text/event-stream")
	if err := p.config.Authorize(httpRequest); err != nil {
		return nil, fmt.Errorf("authorize %s request: %w", label, err)
	}

	response, err := p.config.HTTPClient.Do(httpRequest)
	if err != nil {
		return nil, fmt.Errorf("%s request: %w", label, err)
	}
	if response.StatusCode < 200 || response.StatusCode > 299 {
		defer response.Body.Close()
		detail, _ := io.ReadAll(io.LimitReader(response.Body, maxErrorBytes))
		return nil, hex.ProviderError("The model service", response.StatusCode, detail)
	}
	if p.config.API == APIResponses {
		return newResponsesStream(response.Body, showThinking), nil
	}
	return newStream(response.Body, showThinking), nil
}

type chatRequest struct {
	Model               string        `json:"model"`
	Stream              bool          `json:"stream"`
	StreamOptions       streamOptions `json:"stream_options"`
	MaxCompletionTokens int           `json:"max_completion_tokens,omitempty"`
	Messages            []chatMessage `json:"messages"`
	Tools               []chatTool    `json:"tools,omitempty"`
	ReasoningEffort     string        `json:"reasoning_effort,omitempty"`
}

type streamOptions struct {
	IncludeUsage bool `json:"include_usage"`
}

type chatMessage struct {
	Role       string     `json:"role"`
	Content    any        `json:"content,omitempty"`
	ToolCalls  []toolCall `json:"tool_calls,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
}

type contentPart struct {
	Type     string    `json:"type"`
	Text     string    `json:"text,omitempty"`
	ImageURL *imageURL `json:"image_url,omitempty"`
}

type imageURL struct {
	URL string `json:"url"`
}

type toolCall struct {
	ID       string       `json:"id"`
	Type     string       `json:"type"`
	Function functionCall `json:"function"`
}

type functionCall struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type chatTool struct {
	Type     string       `json:"type"`
	Function toolFunction `json:"function"`
}

type toolFunction struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
}

func completionRequest(model Model, request hex.AIRequest) chatRequest {
	result := chatRequest{
		Model: model.upstream(), Stream: true, StreamOptions: streamOptions{IncludeUsage: true},
		MaxCompletionTokens: request.MaxTokens,
	}
	if request.Thinking != nil {
		result.ReasoningEffort = reasoningEffort(request.Thinking.Effort)
	}
	if request.System != "" {
		result.Messages = append(result.Messages, chatMessage{Role: "system", Content: request.System})
	}
	for _, tool := range request.Tools {
		result.Tools = append(result.Tools, chatTool{Type: "function", Function: toolFunction{
			Name: tool.Name, Description: tool.Description, Parameters: tool.InputSchema,
		}})
	}
	for _, message := range request.Messages {
		if message.Role == hex.RoleAssistant {
			result.Messages = append(result.Messages, assistantMessage(message))
			continue
		}
		result.Messages = append(result.Messages, userMessages(message)...)
	}
	return result
}

func reasoningEffort(effort string) string {
	switch effort {
	case "":
		return ""
	case "low", "medium":
		return effort
	default:
		return "high"
	}
}

func assistantMessage(message hex.AIMessage) chatMessage {
	converted := chatMessage{Role: "assistant"}
	var text strings.Builder
	for _, content := range message.Content {
		switch content.Type {
		case hex.ContentText:
			text.WriteString(content.Text)
		case hex.ContentToolCall:
			arguments := string(content.Input)
			if arguments == "" {
				arguments = "{}"
			}
			converted.ToolCalls = append(converted.ToolCalls, toolCall{
				ID: content.ToolCallID, Type: "function",
				Function: functionCall{Name: content.Name, Arguments: arguments},
			})
		}
	}
	if text.Len() > 0 || len(converted.ToolCalls) == 0 {
		converted.Content = text.String()
	}
	return converted
}

// userMessages turns a Hex user message into Chat Completions messages:
// tool results become "tool" messages, which must directly follow the
// assistant message that called them, and the rest one user message.
func userMessages(message hex.AIMessage) []chatMessage {
	var result []chatMessage
	var parts []contentPart
	for _, content := range message.Content {
		switch content.Type {
		case hex.ContentToolResult:
			text := content.Text
			if content.IsError {
				text = "Error: " + text
			}
			result = append(result, chatMessage{Role: "tool", ToolCallID: content.ToolCallID, Content: text})
		case hex.ContentText:
			parts = append(parts, contentPart{Type: "text", Text: content.Text})
		case hex.ContentImage:
			parts = append(parts, contentPart{Type: "image_url", ImageURL: &imageURL{
				URL: "data:" + content.MediaType + ";base64," + content.Data,
			}})
		}
	}
	if len(parts) == 1 && parts[0].Type == "text" {
		result = append(result, chatMessage{Role: "user", Content: parts[0].Text})
	} else if len(parts) > 0 {
		result = append(result, chatMessage{Role: "user", Content: parts})
	}
	return result
}

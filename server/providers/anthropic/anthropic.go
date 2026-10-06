// Package anthropic is a Hex AI provider for the Anthropic Messages API,
// called over plain HTTP so the same code serves the Claude API and Claude
// deployments in Azure AI Foundry (whose base URL and authentication differ;
// the Go SDK does not support Foundry).
//
// Requests use adaptive thinking when the app asks for reasoning: Hex's
// thinking effort maps to output_config.effort, and Show selects summarized
// rather than omitted thinking text. Fixed thinking budgets and sampling
// parameters are never sent, because current models reject them. A good
// default model is claude-opus-5-5; its effort defaults to medium, so apps
// that want deeper reasoning should set an effort explicitly.
package anthropic

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

const (
	defaultBaseURL = "https://api.anthropic.com"
	defaultVersion = "2023-06-01"
	maxErrorBytes  = 64 << 10
)

// Model is a model apps can choose; Upstream is the model ID or Foundry
// deployment name sent to the API, defaulting to the Hex model ID.
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

// Config configures the provider. Authorize adds credentials to each
// request and defaults to sending APIKey as x-api-key. EagerToolInput sets
// eager_input_streaming on tools, which streams large tool inputs as they
// are generated; it is off by default because proxies and some deployments,
// possibly including Foundry, reject the field.
type Config struct {
	BaseURL          string
	APIKey           string
	Authorize        func(*http.Request) error
	Models           []Model
	HTTPClient       *http.Client
	EagerToolInput   bool
	AnthropicVersion string
	// DisablePromptCaching turns off the cache breakpoints the provider
	// otherwise sets: one on the system prompt and automatic caching of the
	// growing conversation, so later turns read earlier ones from the cache.
	DisablePromptCaching bool
}

// Provider implements hex.AIProvider.
type Provider struct {
	config Config
	models map[string]Model
}

func New(config Config) (*Provider, error) {
	if config.BaseURL == "" {
		config.BaseURL = defaultBaseURL
	}
	config.BaseURL = strings.TrimSuffix(config.BaseURL, "/")
	if config.AnthropicVersion == "" {
		config.AnthropicVersion = defaultVersion
	}
	if config.HTTPClient == nil {
		config.HTTPClient = http.DefaultClient
	}
	if config.Authorize == nil {
		if config.APIKey == "" {
			return nil, errors.New("anthropic: an API key or an Authorize function is required")
		}
		key := config.APIKey
		config.Authorize = func(request *http.Request) error {
			request.Header.Set("x-api-key", key)
			return nil
		}
	}
	if len(config.Models) == 0 {
		return nil, errors.New("anthropic: at least one model is required")
	}
	models := make(map[string]Model, len(config.Models))
	for _, model := range config.Models {
		if model.ID == "" {
			return nil, errors.New("anthropic: every model needs an ID")
		}
		if _, exists := models[model.ID]; exists {
			return nil, fmt.Errorf("anthropic: model %s is listed twice", model.ID)
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
	body, err := json.Marshal(p.messagesRequest(model, request))
	if err != nil {
		return nil, &hex.AINoSpendError{Err: fmt.Errorf("encode Anthropic request: %w", err)}
	}
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, p.config.BaseURL+"/v1/messages", bytes.NewReader(body))
	if err != nil {
		return nil, &hex.AINoSpendError{Err: err}
	}
	httpRequest.Header.Set("Content-Type", "application/json")
	httpRequest.Header.Set("Accept", "text/event-stream")
	httpRequest.Header.Set("anthropic-version", p.config.AnthropicVersion)
	if err := p.config.Authorize(httpRequest); err != nil {
		return nil, &hex.AINoSpendError{Err: fmt.Errorf("authorize Anthropic request: %w", err)}
	}

	response, err := p.config.HTTPClient.Do(httpRequest)
	if err != nil {
		return nil, fmt.Errorf("Anthropic request: %w", err)
	}
	if response.StatusCode < 200 || response.StatusCode > 299 {
		defer response.Body.Close()
		detail, _ := io.ReadAll(io.LimitReader(response.Body, maxErrorBytes))
		return nil, hex.ProviderError("Anthropic", response.StatusCode, detail)
	}
	return newStream(response.Body), nil
}

type messagesRequest struct {
	Model        string        `json:"model"`
	MaxTokens    int           `json:"max_tokens"`
	Stream       bool          `json:"stream"`
	System       []systemBlock `json:"system,omitempty"`
	Messages     []apiMessage  `json:"messages"`
	CacheControl *cacheControl `json:"cache_control,omitempty"`
	Tools        []apiTool     `json:"tools,omitempty"`
	Thinking     *apiThinking  `json:"thinking,omitempty"`
	OutputConfig *outputConfig `json:"output_config,omitempty"`
}

type systemBlock struct {
	Type         string        `json:"type"`
	Text         string        `json:"text"`
	CacheControl *cacheControl `json:"cache_control,omitempty"`
}

type cacheControl struct {
	Type string `json:"type"`
}

var ephemeral = &cacheControl{Type: "ephemeral"}

type apiThinking struct {
	Type    string `json:"type"`
	Display string `json:"display"`
}

type outputConfig struct {
	Effort string `json:"effort"`
}

type apiMessage struct {
	Role    string     `json:"role"`
	Content []apiBlock `json:"content"`
}

type apiBlock struct {
	Type      string          `json:"type"`
	Text      string          `json:"text,omitempty"`
	Thinking  *string         `json:"thinking,omitempty"`
	Signature *string         `json:"signature,omitempty"`
	Data      string          `json:"data,omitempty"`
	Source    *imageSource    `json:"source,omitempty"`
	ID        string          `json:"id,omitempty"`
	Name      string          `json:"name,omitempty"`
	Input     json.RawMessage `json:"input,omitempty"`
	ToolUseID string          `json:"tool_use_id,omitempty"`
	Content   *string         `json:"content,omitempty"`
	IsError   bool            `json:"is_error,omitempty"`
}

type imageSource struct {
	Type      string `json:"type"`
	MediaType string `json:"media_type"`
	Data      string `json:"data"`
}

type apiTool struct {
	Name                string          `json:"name"`
	Description         string          `json:"description"`
	InputSchema         json.RawMessage `json:"input_schema"`
	EagerInputStreaming bool            `json:"eager_input_streaming,omitempty"`
}

func (p *Provider) messagesRequest(model Model, request hex.AIRequest) messagesRequest {
	result := messagesRequest{
		Model: model.upstream(), MaxTokens: request.MaxTokens, Stream: true,
		Messages: make([]apiMessage, 0, len(request.Messages)),
	}
	if request.System != "" {
		system := systemBlock{Type: "text", Text: request.System}
		if !p.config.DisablePromptCaching {
			system.CacheControl = ephemeral
		}
		result.System = []systemBlock{system}
	}
	if !p.config.DisablePromptCaching {
		result.CacheControl = ephemeral
	}
	if result.MaxTokens <= 0 {
		result.MaxTokens = 16000
	}
	if request.Thinking != nil {
		display := "omitted"
		if request.Thinking.Show {
			display = "summarized"
		}
		result.Thinking = &apiThinking{Type: "adaptive", Display: display}
		if request.Thinking.Effort != "" {
			result.OutputConfig = &outputConfig{Effort: request.Thinking.Effort}
		}
	}
	for _, tool := range request.Tools {
		result.Tools = append(result.Tools, apiTool{
			Name: tool.Name, Description: tool.Description, InputSchema: tool.InputSchema,
			EagerInputStreaming: p.config.EagerToolInput,
		})
	}
	for _, message := range request.Messages {
		converted := apiMessage{Role: message.Role}
		for _, content := range message.Content {
			if block, ok := toBlock(content); ok {
				converted.Content = append(converted.Content, block)
			}
		}
		if len(converted.Content) > 0 {
			result.Messages = append(result.Messages, converted)
		}
	}
	return result
}

func toBlock(content hex.AIContent) (apiBlock, bool) {
	switch content.Type {
	case hex.ContentText:
		if content.Text == "" {
			return apiBlock{}, false
		}
		return apiBlock{Type: "text", Text: content.Text}, true
	case hex.ContentImage:
		return apiBlock{Type: "image", Source: &imageSource{Type: "base64", MediaType: content.MediaType, Data: content.Data}}, true
	case hex.ContentThinking:
		if content.Redacted != "" {
			return apiBlock{Type: "redacted_thinking", Data: content.Redacted}, true
		}
		if content.Signature == "" {
			// Without its signature the API rejects a thinking block.
			return apiBlock{}, false
		}
		text, signature := content.Text, content.Signature
		return apiBlock{Type: "thinking", Thinking: &text, Signature: &signature}, true
	case hex.ContentToolCall:
		input := content.Input
		if len(input) == 0 {
			input = json.RawMessage(`{}`)
		}
		return apiBlock{Type: "tool_use", ID: content.ToolCallID, Name: content.Name, Input: input}, true
	case hex.ContentToolResult:
		text := content.Text
		return apiBlock{Type: "tool_result", ToolUseID: content.ToolCallID, Content: &text, IsError: content.IsError}, true
	default:
		return apiBlock{}, false
	}
}

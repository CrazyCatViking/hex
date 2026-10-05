// Package foundry is a Hex AI provider for models deployed in Azure AI
// Foundry. Claude deployments use the Anthropic Messages API under
// {endpoint}/anthropic and other models the OpenAI v1 Chat Completions API
// under {endpoint}/openai/v1. Requests authenticate with Entra ID tokens
// for https://cognitiveservices.azure.com/.default, such as from the
// server's managed identity, or with a resource API key.
package foundry

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/policy"
	hex "github.com/crazycatviking/hex/server"
	"github.com/crazycatviking/hex/server/providers/anthropic"
	"github.com/crazycatviking/hex/server/providers/openai"
)

const (
	ProtocolAnthropic = "anthropic"
	ProtocolOpenAI    = "openai"

	cognitiveServicesScope = "https://cognitiveservices.azure.com/.default"
	tokenRefreshMargin     = 5 * time.Minute
)

// Model is a Foundry deployment apps can choose by its Hex model ID.
// Deployment defaults to the ID; Protocol is "anthropic" or "openai".
type Model struct {
	hex.AIModel
	Deployment string `json:"deployment,omitempty"`
	Protocol   string `json:"protocol"`
}

// Config configures the provider. Endpoint is the resource's origin, such
// as https://my-resource.services.ai.azure.com. Credential is preferred;
// APIKey is the alternative when no Entra identity is available.
type Config struct {
	Endpoint   string
	Credential azcore.TokenCredential
	APIKey     string
	Models     []Model
	HTTPClient *http.Client
}

// Provider routes each request to the protocol of its model.
type Provider struct {
	models []hex.AIModel
	routes map[string]hex.AIProvider
}

// ModelsFromJSON reads model configuration such as HEX_AI_MODELS:
// [{"id":"claude-opus-5-5","name":"Claude Opus 5.5","protocol":"anthropic","thinking":true,"tools":true}].
func ModelsFromJSON(value string) ([]Model, error) {
	var models []Model
	if err := json.Unmarshal([]byte(value), &models); err != nil {
		return nil, fmt.Errorf("expected a JSON array of Foundry models: %w", err)
	}
	return models, nil
}

func New(config Config) (*Provider, error) {
	endpoint, err := url.Parse(strings.TrimSuffix(config.Endpoint, "/"))
	if err != nil || endpoint.Scheme != "https" || endpoint.Host == "" {
		return nil, fmt.Errorf("foundry: the endpoint must be an https URL, got %q", config.Endpoint)
	}
	if config.Credential == nil && config.APIKey == "" {
		return nil, errors.New("foundry: a credential or an API key is required")
	}
	if len(config.Models) == 0 {
		return nil, errors.New("foundry: at least one model is required")
	}

	var anthropicModels []anthropic.Model
	var openaiModels []openai.Model
	provider := &Provider{routes: make(map[string]hex.AIProvider)}
	for _, model := range config.Models {
		if model.ID == "" {
			return nil, errors.New("foundry: every model needs an ID")
		}
		if _, exists := provider.routes[model.ID]; exists {
			return nil, fmt.Errorf("foundry: model %s is listed twice", model.ID)
		}
		if model.Name == "" {
			model.Name = model.ID
		}
		deployment := model.Deployment
		if deployment == "" {
			deployment = model.ID
		}
		switch model.Protocol {
		case ProtocolAnthropic:
			anthropicModels = append(anthropicModels, anthropic.Model{AIModel: model.AIModel, Upstream: deployment})
		case ProtocolOpenAI:
			openaiModels = append(openaiModels, openai.Model{AIModel: model.AIModel, Upstream: deployment})
		default:
			return nil, fmt.Errorf("foundry: model %s needs protocol anthropic or openai", model.ID)
		}
		provider.routes[model.ID] = nil
		provider.models = append(provider.models, model.AIModel)
	}

	tokens := &tokenCache{credential: config.Credential}
	base := endpoint.String()
	if len(anthropicModels) > 0 {
		claude, err := anthropic.New(anthropic.Config{
			BaseURL: base + "/anthropic", Models: anthropicModels, HTTPClient: config.HTTPClient,
			Authorize: authorizer(tokens, config.APIKey, "x-api-key"),
		})
		if err != nil {
			return nil, err
		}
		for _, model := range anthropicModels {
			provider.routes[model.ID] = claude
		}
	}
	if len(openaiModels) > 0 {
		chat, err := openai.New(openai.Config{
			BaseURL: base + "/openai/v1", Models: openaiModels, HTTPClient: config.HTTPClient,
			Authorize: authorizer(tokens, config.APIKey, "api-key"),
		})
		if err != nil {
			return nil, err
		}
		for _, model := range openaiModels {
			provider.routes[model.ID] = chat
		}
	}
	return provider, nil
}

func (p *Provider) Models(context.Context) ([]hex.AIModel, error) {
	return append([]hex.AIModel(nil), p.models...), nil
}

func (p *Provider) Stream(ctx context.Context, request hex.AIRequest) (hex.AIStream, error) {
	route := p.routes[request.Model]
	if route == nil {
		return nil, &hex.AIError{Status: http.StatusBadRequest, Message: fmt.Sprintf("model %q is not configured", request.Model)}
	}
	return route.Stream(ctx, request)
}

// authorizer sends an Entra bearer token when a credential is configured
// and otherwise the API key in the protocol's key header.
func authorizer(tokens *tokenCache, apiKey, keyHeader string) func(*http.Request) error {
	return func(request *http.Request) error {
		if tokens.credential == nil {
			request.Header.Set(keyHeader, apiKey)
			return nil
		}
		token, err := tokens.token(request.Context())
		if err != nil {
			return err
		}
		request.Header.Set("Authorization", "Bearer "+token)
		return nil
	}
}

// tokenCache reuses an Entra access token until shortly before it expires.
type tokenCache struct {
	credential azcore.TokenCredential
	mu         sync.Mutex
	current    azcore.AccessToken
}

func (c *tokenCache) token(ctx context.Context) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.current.Token != "" && time.Until(c.current.ExpiresOn) > tokenRefreshMargin {
		return c.current.Token, nil
	}
	token, err := c.credential.GetToken(ctx, policy.TokenRequestOptions{Scopes: []string{cognitiveServicesScope}})
	if err != nil {
		return "", fmt.Errorf("get Entra token for Azure AI Foundry: %w", err)
	}
	c.current = token
	return token.Token, nil
}

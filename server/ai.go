package hex

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
	"sync"
	"time"
)

// The AI capability gives apps a built-in, streaming interface to language
// models. A platform plugs in an AIProvider (the foundry, anthropic and
// openai providers are included); the server owns authorization, budgets,
// the tool loop and the wire format, so apps get text, thinking and tool
// events in one shape whichever model answers. Integration endpoints can be
// offered to the model as tools: the server runs them with the caller's own
// grants, so a model never sees more than the person could.

// AIModel describes a model apps can choose. Permission defaults to "ai";
// expensive models can require a narrower permission granted through
// Config.IntegrationGrants.
type AIModel struct {
	ID              string `json:"id"`
	Name            string `json:"name"`
	Description     string `json:"description,omitempty"`
	Thinking        bool   `json:"thinking"`
	Tools           bool   `json:"tools"`
	Images          bool   `json:"images"`
	ContextTokens   int    `json:"contextTokens,omitempty"`
	MaxOutputTokens int    `json:"maxOutputTokens,omitempty"`
	Permission      string `json:"permission,omitempty"`
}

// AIProvider answers model requests. Stream returns events until io.EOF;
// the final events are a "message" event carrying the complete assistant
// message and a "done" event with the stop reason and usage.
type AIProvider interface {
	Models(ctx context.Context) ([]AIModel, error)
	Stream(ctx context.Context, request AIRequest) (AIStream, error)
}

// AIStream yields events in order. Next returns io.EOF after the last one.
type AIStream interface {
	Next() (AIEvent, error)
	Close() error
}

// AIRequest is a provider-neutral conversation turn.
type AIRequest struct {
	Model     string      `json:"model"`
	System    string      `json:"system,omitempty"`
	Messages  []AIMessage `json:"messages"`
	Tools     []AITool    `json:"tools,omitempty"`
	Thinking  *AIThinking `json:"thinking,omitempty"`
	MaxTokens int         `json:"maxTokens,omitempty"`
}

// AIThinking asks for reasoning. Effort is low, medium, high, xhigh or max;
// providers map it to what the model supports. Show streams readable
// reasoning (a summary on models that only summarize) as thinking events.
type AIThinking struct {
	Effort string `json:"effort,omitempty"`
	Show   bool   `json:"show,omitempty"`
}

const (
	RoleUser      = "user"
	RoleAssistant = "assistant"
)

// AIMessage is one turn. Tool results are content of user messages.
type AIMessage struct {
	Role    string      `json:"role"`
	Content []AIContent `json:"content"`
}

// AIContent is one block of a message. Types:
//   - text: Text
//   - image: MediaType and base64 Data
//   - thinking: Text plus the provider's opaque Signature, or Redacted data;
//     send thinking blocks back unchanged so the model can continue its reasoning
//   - tool_call: ToolCallID, Name and Input
//   - tool_result: ToolCallID, Text (usually JSON) and IsError
type AIContent struct {
	Type       string          `json:"type"`
	Text       string          `json:"text,omitempty"`
	Signature  string          `json:"signature,omitempty"`
	Redacted   string          `json:"redacted,omitempty"`
	ToolCallID string          `json:"toolCallId,omitempty"`
	Name       string          `json:"name,omitempty"`
	Input      json.RawMessage `json:"input,omitempty"`
	IsError    bool            `json:"isError,omitempty"`
	MediaType  string          `json:"mediaType,omitempty"`
	Data       string          `json:"data,omitempty"`
}

const (
	ContentText       = "text"
	ContentImage      = "image"
	ContentThinking   = "thinking"
	ContentToolCall   = "tool_call"
	ContentToolResult = "tool_result"
)

// AITool is a function the model may call.
type AITool struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"inputSchema"`
}

// AIEvent is one streamed event. Types:
//   - text / thinking: a Text delta
//   - tool_call: a complete tool call in Content
//   - tool_result: the server ran an integration tool; Content is its result
//   - message: a complete message (assistant output, or the user message of
//     server tool results) to append to the conversation
//   - done: StopReason (end_turn, max_tokens, tool_use, refusal) and Usage
//   - error: Error describes a failure after the stream started
type AIEvent struct {
	Type       string     `json:"type"`
	Text       string     `json:"text,omitempty"`
	Content    *AIContent `json:"content,omitempty"`
	Message    *AIMessage `json:"message,omitempty"`
	StopReason string     `json:"stopReason,omitempty"`
	Usage      *AIUsage   `json:"usage,omitempty"`
	Error      string     `json:"error,omitempty"`
}

const (
	EventText       = "text"
	EventThinking   = "thinking"
	EventToolCall   = "tool_call"
	EventToolResult = "tool_result"
	EventMessage    = "message"
	EventDone       = "done"
	EventError      = "error"

	StopEndTurn   = "end_turn"
	StopMaxTokens = "max_tokens"
	StopToolUse   = "tool_use"
	StopRefusal   = "refusal"
)

// AIUsage counts tokens for one model call or a whole tool loop.
type AIUsage struct {
	InputTokens  int `json:"inputTokens"`
	OutputTokens int `json:"outputTokens"`
}

func (u *AIUsage) add(other *AIUsage) {
	if other == nil {
		return
	}
	u.InputTokens += other.InputTokens
	u.OutputTokens += other.OutputTokens
}

// AIConfig enables the AI capability. Permission (default "ai") is needed to
// use it at all. MaxOutputTokens caps each model call (default 16000) and
// MaxToolRounds the server-side tool loop (default 8). DailyTokenLimit caps
// each person's input and output tokens per UTC day, counted in process.
type AIConfig struct {
	Provider        AIProvider
	Permission      string
	MaxOutputTokens int
	MaxToolRounds   int
	DailyTokenLimit int
}

func (c *AIConfig) permission() string {
	if c.Permission == "" {
		return "ai"
	}
	return c.Permission
}

func (c *AIConfig) maxOutputTokens() int {
	if c.MaxOutputTokens <= 0 {
		return 16000
	}
	return c.MaxOutputTokens
}

func (c *AIConfig) maxToolRounds() int {
	if c.MaxToolRounds <= 0 {
		return 8
	}
	return c.MaxToolRounds
}

func modelPermission(model AIModel) string {
	if model.Permission == "" {
		return "ai"
	}
	return model.Permission
}

// AIError is a request problem the app should see, such as an unknown model.
type AIError struct {
	Status  int
	Message string
}

func (e *AIError) Error() string { return e.Message }

var toolNamePattern = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,64}$`)

const (
	maxAIRequestBytes = 8 << 20
	maxAIMessages     = 500
	maxAITools        = 64
	maxToolResultSize = 256 << 10
)

// SiteAIRequest is what apps send: a conversation turn plus integration
// endpoints, by "<integration>.<endpoint>" or "<integration>.*", that the
// server offers to the model and runs itself.
type SiteAIRequest struct {
	AIRequest
	IntegrationTools []string `json:"integrationTools,omitempty"`
}

func (s *Server) aiEnabled() bool {
	return s.config.AI != nil && s.config.AI.Provider != nil
}

// usageMeter counts tokens per person and UTC day for DailyTokenLimit.
type usageMeter struct {
	mu   sync.Mutex
	day  string
	used map[string]int
}

func (m *usageMeter) spent(key string) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.rollOver()
	return m.used[key]
}

func (m *usageMeter) record(key string, tokens int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.rollOver()
	m.used[key] += tokens
}

func (m *usageMeter) rollOver() {
	today := time.Now().UTC().Format(time.DateOnly)
	if m.day != today || m.used == nil {
		m.day = today
		m.used = make(map[string]int)
	}
}

// availableModels lists the provider's models the caller may use.
func (s *Server) availableModels(ctx context.Context, caller integrationCaller) ([]AIModel, error) {
	models, err := s.config.AI.Provider.Models(ctx)
	if err != nil {
		return nil, fmt.Errorf("list AI models: %w", err)
	}
	if !s.granted(caller, s.config.AI.permission()) {
		return []AIModel{}, nil
	}
	allowed := make([]AIModel, 0, len(models))
	for _, model := range models {
		if s.granted(caller, modelPermission(model)) {
			allowed = append(allowed, model)
		}
	}
	return allowed, nil
}

// serverTool maps a tool name offered to the model back to its endpoint.
type serverTool struct {
	name     string
	endpoint *registeredEndpoint
}

// prepareAIRequest validates an app's request, applies the platform's caps
// and resolves the integration tools the caller may use.
func (s *Server) prepareAIRequest(ctx context.Context, caller integrationCaller, request SiteAIRequest) (AIRequest, map[string]serverTool, error) {
	if !s.granted(caller, s.config.AI.permission()) {
		return AIRequest{}, nil, &AIError{Status: 403, Message: "you do not have the " + s.config.AI.permission() + " permission"}
	}
	if caller.identity != nil {
		limit := s.config.AI.DailyTokenLimit
		if limit > 0 && s.aiUsage.spent(caller.usageKey()) >= limit {
			return AIRequest{}, nil, &AIError{Status: 429, Message: "you have used today's AI token allowance"}
		}
	}

	models, err := s.availableModels(ctx, caller)
	if err != nil {
		return AIRequest{}, nil, err
	}
	var model *AIModel
	for index := range models {
		if models[index].ID == request.Model {
			model = &models[index]
		}
	}
	if model == nil {
		return AIRequest{}, nil, &AIError{Status: 400, Message: fmt.Sprintf("model %q is not available to you", request.Model)}
	}
	if err := validateAIMessages(request.Messages); err != nil {
		return AIRequest{}, nil, &AIError{Status: 400, Message: err.Error()}
	}
	if request.Thinking != nil && !model.Thinking {
		request.Thinking = nil
	}

	prepared := request.AIRequest
	limit := s.config.AI.maxOutputTokens()
	if model.MaxOutputTokens > 0 && model.MaxOutputTokens < limit {
		limit = model.MaxOutputTokens
	}
	if prepared.MaxTokens <= 0 || prepared.MaxTokens > limit {
		prepared.MaxTokens = limit
	}

	tools := make(map[string]serverTool)
	for _, tool := range prepared.Tools {
		if !toolNamePattern.MatchString(tool.Name) || strings.TrimSpace(tool.Description) == "" {
			return AIRequest{}, nil, &AIError{Status: 400, Message: fmt.Sprintf("tool %q needs a name of letters, digits, _ or - and a description", tool.Name)}
		}
		if _, err := compileActionSchema(tool.InputSchema); err != nil {
			return AIRequest{}, nil, &AIError{Status: 400, Message: fmt.Sprintf("tool %s input schema: %v", tool.Name, err)}
		}
		tools[tool.Name] = serverTool{name: tool.Name}
	}
	for _, endpoint := range s.integrationTools(ctx, caller, request.IntegrationTools) {
		name := strings.ReplaceAll(endpoint.qualifiedName(), ".", "__")
		if _, taken := tools[name]; taken {
			return AIRequest{}, nil, &AIError{Status: 400, Message: fmt.Sprintf("tool %s is defined twice", name)}
		}
		tools[name] = serverTool{name: name, endpoint: endpoint}
		prepared.Tools = append(prepared.Tools, AITool{
			Name:        name,
			Description: endpoint.integration.integration.Title + ": " + endpoint.endpoint.Description,
			InputSchema: endpoint.endpoint.InputSchema,
		})
	}
	if len(prepared.Tools) > maxAITools {
		return AIRequest{}, nil, &AIError{Status: 400, Message: fmt.Sprintf("at most %d tools can be offered", maxAITools)}
	}
	if len(prepared.Tools) > 0 && !model.Tools {
		return AIRequest{}, nil, &AIError{Status: 400, Message: fmt.Sprintf("model %s does not support tools", model.ID)}
	}
	return prepared, tools, nil
}

func validateAIMessages(messages []AIMessage) error {
	if len(messages) == 0 || len(messages) > maxAIMessages {
		return fmt.Errorf("a request needs 1–%d messages", maxAIMessages)
	}
	for index, message := range messages {
		if message.Role != RoleUser && message.Role != RoleAssistant {
			return fmt.Errorf("message %d: role must be user or assistant", index)
		}
		if len(message.Content) == 0 {
			return fmt.Errorf("message %d has no content", index)
		}
		for _, content := range message.Content {
			switch content.Type {
			case ContentText, ContentThinking:
			case ContentImage:
				if content.MediaType == "" || content.Data == "" {
					return fmt.Errorf("message %d: images need a mediaType and base64 data", index)
				}
			case ContentToolCall:
				if content.ToolCallID == "" || content.Name == "" {
					return fmt.Errorf("message %d: tool calls need an ID and a name", index)
				}
			case ContentToolResult:
				if content.ToolCallID == "" {
					return fmt.Errorf("message %d: tool results need the ID of their call", index)
				}
			default:
				return fmt.Errorf("message %d: unknown content type %q", index, content.Type)
			}
		}
	}
	if messages[len(messages)-1].Role != RoleUser {
		return errors.New("the last message must come from the user")
	}
	return nil
}

// integrationTools resolves requested endpoint names and patterns to the
// endpoints the caller may call; others are left out silently, so an app can
// offer a broad set and each person gets what they are allowed.
func (s *Server) integrationTools(ctx context.Context, caller integrationCaller, requested []string) []*registeredEndpoint {
	var result []*registeredEndpoint
	seen := make(map[string]bool)
	for _, integration := range s.config.Integrations.all() {
		if integration.integration.RequiresApproval {
			status, err := s.approvalStatus(ctx, caller.site, integration.integration.Name)
			if err != nil || status != ApprovalApproved {
				continue
			}
		}
		for _, endpoint := range integration.sortedEndpoints() {
			name := endpoint.qualifiedName()
			if seen[name] || !matchesAnyPattern(requested, name) {
				continue
			}
			if s.integrationAccess(caller, endpoint) != nil {
				continue
			}
			seen[name] = true
			result = append(result, endpoint)
		}
	}
	return result
}

func matchesAnyPattern(patterns []string, name string) bool {
	for _, pattern := range patterns {
		if permissionMatches(pattern, name) {
			return true
		}
	}
	return false
}

// runAI performs a conversation turn, running server tools until the model
// finishes, asks for a tool only the app can run, or reaches the round limit.
// Every event goes to emit; the returned usage covers all model calls.
func (s *Server) runAI(ctx context.Context, caller integrationCaller, request AIRequest, tools map[string]serverTool, emit func(AIEvent) error) (AIUsage, error) {
	var total AIUsage
	messages := append([]AIMessage(nil), request.Messages...)
	defer func() {
		tokens := total.InputTokens + total.OutputTokens
		s.aiUsage.record(caller.usageKey(), tokens)
		logAIUsage(caller, request.Model, total)
	}()

	for round := 0; ; round++ {
		request.Messages = messages
		assistant, done, err := s.streamModel(ctx, request, emit)
		if done != nil {
			total.add(done.Usage)
		}
		if err != nil {
			return total, err
		}

		calls := toolCalls(assistant)
		// The server answers integration tools and tools nobody offered;
		// the app answers the tools it defined.
		serverCalls := 0
		for _, call := range calls {
			if !isAppTool(tools, call.Name) {
				serverCalls++
			}
		}
		if done.StopReason != StopToolUse || serverCalls == 0 {
			done.Usage = &total
			return total, emit(*done)
		}

		results := AIMessage{Role: RoleUser}
		for _, call := range calls {
			if isAppTool(tools, call.Name) {
				continue
			}
			result := s.runServerTool(ctx, caller, tools[call.Name], call)
			if err := emit(AIEvent{Type: EventToolResult, Content: &result}); err != nil {
				return total, err
			}
			results.Content = append(results.Content, result)
		}
		if err := emit(AIEvent{Type: EventMessage, Message: &results}); err != nil {
			return total, err
		}
		// When the model also asked for tools only the app can run, the app
		// appends their results to this user message and continues; at the
		// round limit it can continue the conversation itself.
		if serverCalls < len(calls) || round+1 >= s.config.AI.maxToolRounds() {
			done.StopReason = StopToolUse
			done.Usage = &total
			return total, emit(*done)
		}
		messages = append(messages, *assistant, results)
	}
}

// streamModel relays one model call and returns its assistant message and
// final done event, which is held back for the caller to emit.
func (s *Server) streamModel(ctx context.Context, request AIRequest, emit func(AIEvent) error) (*AIMessage, *AIEvent, error) {
	stream, err := s.config.AI.Provider.Stream(ctx, request)
	if err != nil {
		return nil, nil, err
	}
	defer func() {
		if err := stream.Close(); err != nil {
			slogAIError("close AI stream", err)
		}
	}()

	var assistant *AIMessage
	var done *AIEvent
	for {
		event, err := stream.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return assistant, done, err
		}
		switch event.Type {
		case EventDone:
			copied := event
			done = &copied
			continue
		case EventMessage:
			assistant = event.Message
		}
		if err := emit(event); err != nil {
			return assistant, done, err
		}
	}
	if assistant == nil || done == nil {
		return assistant, done, errors.New("the model stream ended without a complete message")
	}
	return assistant, done, nil
}

func isAppTool(tools map[string]serverTool, name string) bool {
	tool, offered := tools[name]
	return offered && tool.endpoint == nil
}

func toolCalls(message *AIMessage) []AIContent {
	var calls []AIContent
	if message == nil {
		return calls
	}
	for _, content := range message.Content {
		if content.Type == ContentToolCall {
			calls = append(calls, content)
		}
	}
	return calls
}

// runServerTool calls an integration endpoint for the model. Failures are
// reported to the model as error results so it can recover or explain.
func (s *Server) runServerTool(ctx context.Context, caller integrationCaller, tool serverTool, call AIContent) AIContent {
	result := AIContent{Type: ContentToolResult, ToolCallID: call.ToolCallID, Name: call.Name}
	if tool.endpoint == nil {
		result.IsError = true
		result.Text = "there is no tool named " + call.Name
		return result
	}
	input := call.Input
	if len(input) == 0 {
		input = json.RawMessage(`{}`)
	}
	output, _, err := s.callIntegration(ctx, caller, tool.endpoint, input)
	if err != nil {
		result.IsError = true
		result.Text = toolErrorText(tool.endpoint, err)
		return result
	}
	if len(output) > maxToolResultSize {
		result.IsError = true
		result.Text = fmt.Sprintf("the result was %d bytes, more than the %d bytes a tool may return; narrow the request", len(output), maxToolResultSize)
		return result
	}
	result.Text = string(output)
	return result
}

func toolErrorText(endpoint *registeredEndpoint, err error) string {
	var integrationError *IntegrationError
	switch {
	case errors.Is(err, ErrNotConnected):
		return "the user has not connected their " + endpoint.integration.integration.Title + " account; ask them to connect it in the app"
	case errors.As(err, &integrationError):
		return integrationError.Message
	default:
		slogAIError("integration tool "+endpoint.qualifiedName(), err)
		return endpoint.integration.integration.Title + " request failed"
	}
}

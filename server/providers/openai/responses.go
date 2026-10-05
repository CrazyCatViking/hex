package openai

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	hex "github.com/crazycatviking/hex/server"
)

// The Responses API (POST /responses). Requests are sent with store=false,
// so the service keeps nothing between turns; reasoning comes back as
// encrypted content, which Hex keeps in thinking blocks and sends back with
// the conversation so the model can continue its reasoning after tool calls.

type responsesBody struct {
	Model           string           `json:"model"`
	Stream          bool             `json:"stream"`
	Store           bool             `json:"store"`
	Include         []string         `json:"include,omitempty"`
	Instructions    string           `json:"instructions,omitempty"`
	Input           []any            `json:"input"`
	Tools           []responsesTool  `json:"tools,omitempty"`
	Reasoning       *reasoningConfig `json:"reasoning,omitempty"`
	MaxOutputTokens int              `json:"max_output_tokens,omitempty"`
}

type reasoningConfig struct {
	Effort  string `json:"effort,omitempty"`
	Summary string `json:"summary,omitempty"`
}

type responsesTool struct {
	Type        string          `json:"type"`
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters"`
	Strict      bool            `json:"strict"`
}

type inputMessage struct {
	Role    string `json:"role"`
	Content any    `json:"content"`
}

type inputPart struct {
	Type     string `json:"type"`
	Text     string `json:"text,omitempty"`
	ImageURL string `json:"image_url,omitempty"`
}

type functionCallItem struct {
	Type      string `json:"type"`
	CallID    string `json:"call_id"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type functionOutputItem struct {
	Type   string `json:"type"`
	CallID string `json:"call_id"`
	Output string `json:"output"`
}

type reasoningItem struct {
	Type             string        `json:"type"`
	ID               string        `json:"id,omitempty"`
	EncryptedContent string        `json:"encrypted_content,omitempty"`
	Summary          []summaryPart `json:"summary"`
}

type summaryPart struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// reasoningSignature is what a thinking block's Signature holds for this
// API: the reasoning item to send back.
type reasoningSignature struct {
	ID               string `json:"id"`
	EncryptedContent string `json:"encrypted_content"`
}

func responsesRequest(model Model, request hex.AIRequest) responsesBody {
	result := responsesBody{
		Model: model.upstream(), Stream: true, Instructions: request.System,
		Include: []string{"reasoning.encrypted_content"}, Input: []any{},
		MaxOutputTokens: request.MaxTokens,
	}
	if request.Thinking != nil {
		result.Reasoning = &reasoningConfig{Effort: request.Thinking.Effort}
		if request.Thinking.Show {
			result.Reasoning.Summary = "auto"
		}
	}
	for _, tool := range request.Tools {
		parameters := tool.InputSchema
		if len(parameters) == 0 {
			parameters = json.RawMessage(`{"type":"object","properties":{}}`)
		}
		result.Tools = append(result.Tools, responsesTool{
			Type: "function", Name: tool.Name, Description: tool.Description, Parameters: parameters,
		})
	}
	for _, message := range request.Messages {
		if message.Role == hex.RoleAssistant {
			result.Input = append(result.Input, assistantItems(message)...)
			continue
		}
		result.Input = append(result.Input, userItems(message)...)
	}
	return result
}

// assistantItems keeps the order of the assistant's output: a reasoning
// item must precede the call or text it led to.
func assistantItems(message hex.AIMessage) []any {
	var items []any
	var text strings.Builder
	flush := func() {
		if text.Len() > 0 {
			items = append(items, inputMessage{Role: "assistant", Content: text.String()})
			text.Reset()
		}
	}
	for _, content := range message.Content {
		switch content.Type {
		case hex.ContentText:
			text.WriteString(content.Text)
		case hex.ContentThinking:
			// Thinking from another protocol, such as a Claude signature,
			// cannot be sent here and is left out.
			var signature reasoningSignature
			if json.Unmarshal([]byte(content.Signature), &signature) != nil || signature.EncryptedContent == "" {
				continue
			}
			flush()
			item := reasoningItem{Type: "reasoning", ID: signature.ID, EncryptedContent: signature.EncryptedContent, Summary: []summaryPart{}}
			if content.Text != "" {
				item.Summary = append(item.Summary, summaryPart{Type: "summary_text", Text: content.Text})
			}
			items = append(items, item)
		case hex.ContentToolCall:
			flush()
			arguments := string(content.Input)
			if arguments == "" {
				arguments = "{}"
			}
			items = append(items, functionCallItem{Type: "function_call", CallID: content.ToolCallID, Name: content.Name, Arguments: arguments})
		}
	}
	flush()
	return items
}

// userItems turns tool results into function_call_output items and the
// rest of the message into one user message.
func userItems(message hex.AIMessage) []any {
	var items []any
	var parts []inputPart
	for _, content := range message.Content {
		switch content.Type {
		case hex.ContentToolResult:
			text := content.Text
			if content.IsError {
				text = "Error: " + text
			}
			items = append(items, functionOutputItem{Type: "function_call_output", CallID: content.ToolCallID, Output: text})
		case hex.ContentText:
			parts = append(parts, inputPart{Type: "input_text", Text: content.Text})
		case hex.ContentImage:
			parts = append(parts, inputPart{Type: "input_image", ImageURL: "data:" + content.MediaType + ";base64," + content.Data})
		}
	}
	if len(parts) > 0 {
		items = append(items, inputMessage{Role: "user", Content: parts})
	}
	return items
}

// responsesStream turns Responses API events into Hex events. Text and
// reasoning summaries stream as deltas; each finished output item becomes
// part of the assistant message, in the order the model produced them.
type responsesStream struct {
	body         io.ReadCloser
	reader       *bufio.Reader
	showThinking bool
	pending      []hex.AIEvent
	content      []hex.AIContent
	streamed     map[string]bool
	calls        int
	refused      bool
	finished     bool
}

func newResponsesStream(body io.ReadCloser, showThinking bool) *responsesStream {
	return &responsesStream{
		body: body, reader: bufio.NewReaderSize(body, 64<<10),
		showThinking: showThinking, streamed: make(map[string]bool),
	}
}

func (s *responsesStream) Close() error {
	return s.body.Close()
}

func (s *responsesStream) Next() (hex.AIEvent, error) {
	for len(s.pending) == 0 {
		if s.finished {
			return hex.AIEvent{}, io.EOF
		}
		name, data, err := readEvent(s.reader)
		if errors.Is(err, io.EOF) {
			return hex.AIEvent{}, errors.New("the response stream ended before the response finished")
		}
		if err != nil {
			return hex.AIEvent{}, fmt.Errorf("read response stream: %w", err)
		}
		if err := s.handle(name, data); err != nil {
			return hex.AIEvent{}, err
		}
	}
	event := s.pending[0]
	s.pending = s.pending[1:]
	return event, nil
}

type responseEvent struct {
	Type         string          `json:"type"`
	ItemID       string          `json:"item_id"`
	SummaryIndex int             `json:"summary_index"`
	Delta        string          `json:"delta"`
	Item         *outputItem     `json:"item"`
	Response     *responseStatus `json:"response"`
	// The error event carries its message at the top level or, in some
	// versions, in an error object.
	Message string     `json:"message"`
	Error   *errorBody `json:"error"`
}

type outputItem struct {
	Type             string        `json:"type"`
	ID               string        `json:"id"`
	CallID           string        `json:"call_id"`
	Name             string        `json:"name"`
	Arguments        string        `json:"arguments"`
	EncryptedContent string        `json:"encrypted_content"`
	Summary          []summaryPart `json:"summary"`
	Content          []struct {
		Type    string `json:"type"`
		Text    string `json:"text"`
		Refusal string `json:"refusal"`
	} `json:"content"`
}

type responseStatus struct {
	Status            string `json:"status"`
	IncompleteDetails *struct {
		Reason string `json:"reason"`
	} `json:"incomplete_details"`
	Error *errorBody `json:"error"`
	Usage *struct {
		InputTokens        int `json:"input_tokens"`
		OutputTokens       int `json:"output_tokens"`
		InputTokensDetails *struct {
			CachedTokens int `json:"cached_tokens"`
		} `json:"input_tokens_details"`
	} `json:"usage"`
}

type errorBody struct {
	Code    any    `json:"code"`
	Message string `json:"message"`
}

func (s *responsesStream) handle(name, data string) error {
	data = strings.TrimSpace(data)
	if data == "" || data == "[DONE]" {
		return nil
	}
	var event responseEvent
	if err := json.Unmarshal([]byte(data), &event); err != nil {
		return fmt.Errorf("decode response event: %w", err)
	}
	if event.Type == "" {
		event.Type = name
	}
	switch event.Type {
	case "response.output_text.delta", "response.refusal.delta":
		if event.Delta == "" {
			return nil
		}
		s.streamed[event.ItemID] = true
		if event.Type == "response.refusal.delta" {
			s.refused = true
		}
		s.pending = append(s.pending, hex.AIEvent{Type: hex.EventText, Text: event.Delta})
	case "response.reasoning_summary_part.added":
		// Summary parts are separate paragraphs.
		if event.SummaryIndex > 0 && s.showThinking {
			s.pending = append(s.pending, hex.AIEvent{Type: hex.EventThinking, Text: "\n\n"})
		}
	case "response.reasoning_summary_text.delta":
		if event.Delta != "" && s.showThinking {
			s.streamed[event.ItemID] = true
			s.pending = append(s.pending, hex.AIEvent{Type: hex.EventThinking, Text: event.Delta})
		}
	case "response.output_item.done":
		if event.Item != nil {
			s.addItem(*event.Item)
		}
	case "response.completed", "response.incomplete":
		s.finish(event.Response)
	case "response.failed":
		message := "the model service failed to respond"
		if event.Response != nil && event.Response.Error != nil && event.Response.Error.Message != "" {
			message = event.Response.Error.Message
		}
		return fmt.Errorf("response failed: %s", message)
	case "error":
		message := event.Message
		if event.Error != nil && event.Error.Message != "" {
			message = event.Error.Message
		}
		return fmt.Errorf("response stream error: %s", message)
	}
	return nil
}

func (s *responsesStream) addItem(item outputItem) {
	switch item.Type {
	case "message":
		var text strings.Builder
		for _, part := range item.Content {
			switch part.Type {
			case "output_text":
				text.WriteString(part.Text)
			case "refusal":
				s.refused = true
				text.WriteString(part.Refusal)
			}
		}
		if text.Len() == 0 {
			return
		}
		if !s.streamed[item.ID] {
			s.pending = append(s.pending, hex.AIEvent{Type: hex.EventText, Text: text.String()})
		}
		s.content = append(s.content, hex.AIContent{Type: hex.ContentText, Text: text.String()})
	case "reasoning":
		var summary []string
		for _, part := range item.Summary {
			summary = append(summary, part.Text)
		}
		text := strings.Join(summary, "\n\n")
		if !s.showThinking {
			text = ""
		}
		if text != "" && !s.streamed[item.ID] {
			s.pending = append(s.pending, hex.AIEvent{Type: hex.EventThinking, Text: text})
		}
		if item.EncryptedContent == "" && text == "" {
			return
		}
		thinking := hex.AIContent{Type: hex.ContentThinking, Text: text}
		if item.EncryptedContent != "" {
			signature, _ := json.Marshal(reasoningSignature{ID: item.ID, EncryptedContent: item.EncryptedContent})
			thinking.Signature = string(signature)
		}
		s.content = append(s.content, thinking)
	case "function_call":
		call := hex.AIContent{
			Type: hex.ContentToolCall, ToolCallID: item.CallID, Name: item.Name,
			Input: toolInput(item.Name, item.Arguments),
		}
		s.calls++
		s.pending = append(s.pending, hex.AIEvent{Type: hex.EventToolCall, Content: &call})
		s.content = append(s.content, call)
	}
}

func (s *responsesStream) finish(response *responseStatus) {
	if s.finished {
		return
	}
	s.finished = true
	var usage hex.AIUsage
	stop := hex.StopEndTurn
	if response != nil {
		if response.Usage != nil {
			// Input tokens include the cached ones, which are priced separately.
			cached := 0
			if response.Usage.InputTokensDetails != nil {
				cached = min(response.Usage.InputTokensDetails.CachedTokens, response.Usage.InputTokens)
			}
			usage = hex.AIUsage{
				InputTokens: response.Usage.InputTokens - cached, CachedInputTokens: cached,
				OutputTokens: response.Usage.OutputTokens,
			}
		}
		if response.IncompleteDetails != nil {
			switch response.IncompleteDetails.Reason {
			case "max_output_tokens":
				stop = hex.StopMaxTokens
			case "content_filter":
				stop = hex.StopRefusal
			}
		}
	}
	switch {
	case s.refused:
		stop = hex.StopRefusal
	case stop == hex.StopEndTurn && s.calls > 0:
		stop = hex.StopToolUse
	}
	content := s.content
	if content == nil {
		content = []hex.AIContent{}
	}
	s.pending = append(s.pending,
		hex.AIEvent{Type: hex.EventMessage, Message: &hex.AIMessage{Role: hex.RoleAssistant, Content: content}},
		hex.AIEvent{Type: hex.EventDone, StopReason: stop, Usage: &usage},
	)
}

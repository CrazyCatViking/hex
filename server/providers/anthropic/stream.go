package anthropic

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"

	hex "github.com/crazycatviking/hex/server"
)

const maxEventBytes = 8 << 20

// stream turns Messages API server-sent events into Hex events. Content
// blocks are assembled as their deltas arrive; message_stop yields the
// complete assistant message and the done event.
type stream struct {
	body     io.ReadCloser
	reader   *bufio.Reader
	pending  []hex.AIEvent
	blocks   []*blockBuilder
	usage    hex.AIUsage
	stop     string
	finished bool
}

type blockBuilder struct {
	content hex.AIContent
	text    strings.Builder
	json    strings.Builder
	done    bool
}

func newStream(body io.ReadCloser) *stream {
	return &stream{body: body, reader: bufio.NewReaderSize(body, 64<<10)}
}

func (s *stream) Close() error {
	return s.body.Close()
}

func (s *stream) Next() (hex.AIEvent, error) {
	for len(s.pending) == 0 {
		if s.finished {
			return hex.AIEvent{}, io.EOF
		}
		name, data, err := readEvent(s.reader)
		if errors.Is(err, io.EOF) {
			return hex.AIEvent{}, errors.New("the Anthropic stream ended before message_stop")
		}
		if err != nil {
			return hex.AIEvent{}, fmt.Errorf("read Anthropic stream: %w", err)
		}
		if err := s.handle(name, data); err != nil {
			return hex.AIEvent{}, err
		}
	}
	event := s.pending[0]
	s.pending = s.pending[1:]
	return event, nil
}

type streamEvent struct {
	Type    string `json:"type"`
	Index   int    `json:"index"`
	Message struct {
		Usage apiUsage `json:"usage"`
	} `json:"message"`
	ContentBlock struct {
		Type      string          `json:"type"`
		Text      string          `json:"text"`
		Thinking  string          `json:"thinking"`
		Signature string          `json:"signature"`
		Data      string          `json:"data"`
		ID        string          `json:"id"`
		Name      string          `json:"name"`
		Input     json.RawMessage `json:"input"`
	} `json:"content_block"`
	Delta struct {
		Type        string `json:"type"`
		Text        string `json:"text"`
		Thinking    string `json:"thinking"`
		Signature   string `json:"signature"`
		PartialJSON string `json:"partial_json"`
		StopReason  string `json:"stop_reason"`
	} `json:"delta"`
	Usage *apiUsage `json:"usage"`
	Error struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	} `json:"error"`
}

type apiUsage struct {
	InputTokens              int `json:"input_tokens"`
	OutputTokens             int `json:"output_tokens"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
	CacheReadInputTokens     int `json:"cache_read_input_tokens"`
}

func (s *stream) handle(name, data string) error {
	if data == "" {
		return nil
	}
	var event streamEvent
	if err := json.Unmarshal([]byte(data), &event); err != nil {
		return fmt.Errorf("decode Anthropic %s event: %w", name, err)
	}

	switch event.Type {
	case "message_start":
		usage := event.Message.Usage
		s.usage.InputTokens = usage.InputTokens + usage.CacheCreationInputTokens + usage.CacheReadInputTokens
		s.usage.OutputTokens = usage.OutputTokens
	case "content_block_start":
		return s.startBlock(event)
	case "content_block_delta":
		return s.applyDelta(event)
	case "content_block_stop":
		return s.stopBlock(event.Index)
	case "message_delta":
		if event.Delta.StopReason != "" {
			s.stop = event.Delta.StopReason
		}
		if event.Usage != nil {
			s.usage.OutputTokens = event.Usage.OutputTokens
		}
	case "message_stop":
		s.finish()
	case "error":
		return fmt.Errorf("Anthropic stream error %s: %s", event.Error.Type, event.Error.Message)
	}
	// ping and unknown event types are ignored, as the API asks clients to do.
	return nil
}

func (s *stream) block(index int) (*blockBuilder, error) {
	if index < 0 || index >= len(s.blocks) || s.blocks[index] == nil {
		return nil, fmt.Errorf("Anthropic stream referenced unknown content block %d", index)
	}
	return s.blocks[index], nil
}

func (s *stream) startBlock(event streamEvent) error {
	index := event.Index
	if index < 0 || index > 1024 {
		return fmt.Errorf("Anthropic stream has an invalid block index %d", index)
	}
	for len(s.blocks) <= index {
		s.blocks = append(s.blocks, nil)
	}
	start := event.ContentBlock
	builder := &blockBuilder{}
	switch start.Type {
	case "text":
		builder.content = hex.AIContent{Type: hex.ContentText}
		builder.text.WriteString(start.Text)
	case "thinking":
		builder.content = hex.AIContent{Type: hex.ContentThinking, Signature: start.Signature}
		builder.text.WriteString(start.Thinking)
	case "redacted_thinking":
		builder.content = hex.AIContent{Type: hex.ContentThinking, Redacted: start.Data}
	case "tool_use":
		builder.content = hex.AIContent{Type: hex.ContentToolCall, ToolCallID: start.ID, Name: start.Name}
	default:
		// Block types Hex does not model (such as server tool results) are
		// skipped rather than replayed incompletely.
		builder.done = true
		builder.content = hex.AIContent{Type: ""}
	}
	s.blocks[index] = builder
	return nil
}

func (s *stream) applyDelta(event streamEvent) error {
	builder, err := s.block(event.Index)
	if err != nil {
		return err
	}
	delta := event.Delta
	switch delta.Type {
	case "text_delta":
		builder.text.WriteString(delta.Text)
		if delta.Text != "" {
			s.pending = append(s.pending, hex.AIEvent{Type: hex.EventText, Text: delta.Text})
		}
	case "thinking_delta":
		builder.text.WriteString(delta.Thinking)
		if delta.Thinking != "" {
			s.pending = append(s.pending, hex.AIEvent{Type: hex.EventThinking, Text: delta.Thinking})
		}
	case "signature_delta":
		builder.content.Signature += delta.Signature
	case "input_json_delta":
		builder.json.WriteString(delta.PartialJSON)
	}
	return nil
}

func (s *stream) stopBlock(index int) error {
	builder, err := s.block(index)
	if err != nil {
		return err
	}
	if builder.done {
		return nil
	}
	builder.done = true
	switch builder.content.Type {
	case hex.ContentText, hex.ContentThinking:
		builder.content.Text = builder.text.String()
	case hex.ContentToolCall:
		builder.content.Input = toolInput(builder.content.Name, builder.json.String())
		call := builder.content
		s.pending = append(s.pending, hex.AIEvent{Type: hex.EventToolCall, Content: &call})
	}
	return nil
}

// toolInput parses the streamed tool input. An empty input means no
// arguments; input that is not a JSON object (for example cut off by the
// token limit) becomes {} so the tool's schema validation reports it.
func toolInput(name, raw string) json.RawMessage {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return json.RawMessage(`{}`)
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal([]byte(trimmed), &object); err != nil || object == nil {
		slog.Warn("Anthropic tool call input is not a JSON object", "tool", name, "error", err)
		return json.RawMessage(`{}`)
	}
	return json.RawMessage(trimmed)
}

func (s *stream) finish() {
	message := hex.AIMessage{Role: hex.RoleAssistant, Content: []hex.AIContent{}}
	for _, builder := range s.blocks {
		if builder == nil || builder.content.Type == "" {
			continue
		}
		if !builder.done {
			builder.done = true
			if builder.content.Type != hex.ContentToolCall {
				builder.content.Text = builder.text.String()
			} else {
				builder.content.Input = toolInput(builder.content.Name, builder.json.String())
			}
		}
		if builder.content.Type == hex.ContentText && builder.content.Text == "" {
			continue
		}
		message.Content = append(message.Content, builder.content)
	}
	usage := s.usage
	s.pending = append(s.pending,
		hex.AIEvent{Type: hex.EventMessage, Message: &message},
		hex.AIEvent{Type: hex.EventDone, StopReason: stopReason(s.stop), Usage: &usage},
	)
	s.finished = true
}

// stopReason maps the API's stop reasons to Hex's. pause_turn (a long
// server-tool turn the client may resume) ends the turn, because Hex does
// not offer server tools that pause.
func stopReason(reason string) string {
	switch reason {
	case "max_tokens", "model_context_window_exceeded":
		return hex.StopMaxTokens
	case "tool_use":
		return hex.StopToolUse
	case "refusal":
		return hex.StopRefusal
	default:
		return hex.StopEndTurn
	}
}

// readEvent reads one server-sent event, returning its name and the data
// lines joined by newlines. It returns io.EOF only when no event remains.
func readEvent(reader *bufio.Reader) (string, string, error) {
	var name string
	var data strings.Builder
	hasField := false
	size := 0
	for {
		line, err := reader.ReadString('\n')
		size += len(line)
		if size > maxEventBytes {
			return "", "", errors.New("server-sent event exceeds 8 MiB")
		}
		if err != nil && !errors.Is(err, io.EOF) {
			return "", "", err
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			if hasField {
				return name, data.String(), nil
			}
			if err != nil {
				return "", "", io.EOF
			}
			continue
		}

		field, value, _ := strings.Cut(line, ":")
		value = strings.TrimPrefix(value, " ")
		switch field {
		case "event":
			name = value
			hasField = true
		case "data":
			if data.Len() > 0 {
				data.WriteByte('\n')
			}
			data.WriteString(value)
			hasField = true
		}
		if err != nil {
			if hasField {
				return name, data.String(), nil
			}
			return "", "", io.EOF
		}
	}
}

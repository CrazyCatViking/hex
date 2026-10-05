package openai

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"slices"
	"strings"

	hex "github.com/crazycatviking/hex/server"
)

// stream turns Chat Completions chunks into Hex events. Text and tool calls
// are assembled across chunks; the end of the stream yields the complete
// assistant message and the done event.
type stream struct {
	body         io.ReadCloser
	reader       *bufio.Reader
	showThinking bool
	pending      []hex.AIEvent
	text         strings.Builder
	calls        map[int]*callBuilder
	finishReason string
	usage        hex.AIUsage
	finished     bool
}

type callBuilder struct {
	id        string
	name      string
	arguments strings.Builder
}

func newStream(body io.ReadCloser, showThinking bool) *stream {
	return &stream{
		body: body, reader: bufio.NewReaderSize(body, 64<<10),
		showThinking: showThinking, calls: make(map[int]*callBuilder),
	}
}

func (s *stream) Close() error {
	return s.body.Close()
}

func (s *stream) Next() (hex.AIEvent, error) {
	for len(s.pending) == 0 {
		if s.finished {
			return hex.AIEvent{}, io.EOF
		}
		_, data, err := readEvent(s.reader)
		if errors.Is(err, io.EOF) {
			// Some compatible services end the stream without [DONE].
			if s.finishReason == "" {
				return hex.AIEvent{}, errors.New("the chat completion stream ended before it finished")
			}
			s.finish()
			continue
		}
		if err != nil {
			return hex.AIEvent{}, fmt.Errorf("read chat completion stream: %w", err)
		}
		if err := s.handle(data); err != nil {
			return hex.AIEvent{}, err
		}
	}
	event := s.pending[0]
	s.pending = s.pending[1:]
	return event, nil
}

type chunk struct {
	Choices []struct {
		Delta struct {
			Content          string `json:"content"`
			ReasoningContent string `json:"reasoning_content"`
			Reasoning        string `json:"reasoning"`
			ToolCalls        []struct {
				Index    int    `json:"index"`
				ID       string `json:"id"`
				Function struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				} `json:"function"`
			} `json:"tool_calls"`
		} `json:"delta"`
		FinishReason *string `json:"finish_reason"`
	} `json:"choices"`
	Usage *struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
	} `json:"usage"`
	Error *struct {
		Message string `json:"message"`
		Code    any    `json:"code"`
	} `json:"error"`
}

func (s *stream) handle(data string) error {
	data = strings.TrimSpace(data)
	if data == "" {
		return nil
	}
	if data == "[DONE]" {
		s.finish()
		return nil
	}
	var parsed chunk
	if err := json.Unmarshal([]byte(data), &parsed); err != nil {
		return fmt.Errorf("decode chat completion chunk: %w", err)
	}
	if parsed.Error != nil {
		return fmt.Errorf("chat completion stream error: %s", parsed.Error.Message)
	}
	if parsed.Usage != nil {
		s.usage = hex.AIUsage{InputTokens: parsed.Usage.PromptTokens, OutputTokens: parsed.Usage.CompletionTokens}
	}
	if len(parsed.Choices) == 0 {
		return nil
	}

	choice := parsed.Choices[0]
	delta := choice.Delta
	reasoning := delta.ReasoningContent + delta.Reasoning
	if reasoning != "" && s.showThinking {
		s.pending = append(s.pending, hex.AIEvent{Type: hex.EventThinking, Text: reasoning})
	}
	if delta.Content != "" {
		s.text.WriteString(delta.Content)
		s.pending = append(s.pending, hex.AIEvent{Type: hex.EventText, Text: delta.Content})
	}
	for _, call := range delta.ToolCalls {
		builder, exists := s.calls[call.Index]
		if !exists {
			builder = &callBuilder{}
			s.calls[call.Index] = builder
		}
		if call.ID != "" {
			builder.id = call.ID
		}
		builder.name += call.Function.Name
		builder.arguments.WriteString(call.Function.Arguments)
	}
	if choice.FinishReason != nil && *choice.FinishReason != "" {
		s.finishReason = *choice.FinishReason
	}
	return nil
}

func (s *stream) finish() {
	if s.finished {
		return
	}
	s.finished = true
	message := hex.AIMessage{Role: hex.RoleAssistant, Content: []hex.AIContent{}}
	if s.text.Len() > 0 {
		message.Content = append(message.Content, hex.AIContent{Type: hex.ContentText, Text: s.text.String()})
	}

	indexes := make([]int, 0, len(s.calls))
	for index := range s.calls {
		indexes = append(indexes, index)
	}
	slices.Sort(indexes)
	for _, index := range indexes {
		builder := s.calls[index]
		call := hex.AIContent{
			Type: hex.ContentToolCall, ToolCallID: builder.id, Name: builder.name,
			Input: toolInput(builder.name, builder.arguments.String()),
		}
		s.pending = append(s.pending, hex.AIEvent{Type: hex.EventToolCall, Content: &call})
		message.Content = append(message.Content, call)
	}

	stop := stopReason(s.finishReason)
	if len(indexes) > 0 && stop == hex.StopEndTurn {
		// Some services report "stop" even when the turn ends in tool calls.
		stop = hex.StopToolUse
	}
	usage := s.usage
	s.pending = append(s.pending,
		hex.AIEvent{Type: hex.EventMessage, Message: &message},
		hex.AIEvent{Type: hex.EventDone, StopReason: stop, Usage: &usage},
	)
}

// toolInput parses streamed arguments; anything that is not a JSON object
// becomes {} so the tool's schema validation reports the problem.
func toolInput(name, raw string) json.RawMessage {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return json.RawMessage(`{}`)
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal([]byte(trimmed), &object); err != nil || object == nil {
		slog.Warn("tool call arguments are not a JSON object", "tool", name, "error", err)
		return json.RawMessage(`{}`)
	}
	return json.RawMessage(trimmed)
}

func stopReason(reason string) string {
	switch reason {
	case "length":
		return hex.StopMaxTokens
	case "tool_calls", "function_call":
		return hex.StopToolUse
	case "content_filter":
		return hex.StopRefusal
	default:
		return hex.StopEndTurn
	}
}

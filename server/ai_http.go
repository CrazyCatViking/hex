package hex

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sync"
	"time"
)

const aiHeartbeatInterval = 15 * time.Second

func (s *Server) registerAIRoutes() {
	if !s.aiEnabled() {
		return
	}
	s.mux.HandleFunc("GET /api/sites/{site}/ai/models", s.siteScoped(s.listAIModels))
	s.mux.HandleFunc("POST /api/sites/{site}/ai/stream", s.siteScoped(s.streamAI))
	s.mux.HandleFunc("POST /api/sites/{site}/ai/complete", s.siteScoped(s.completeAI))
}

func siteCaller(r *http.Request) integrationCaller {
	authorization := requestAuthorization(r)
	return integrationCaller{site: r.PathValue("site"), identity: authorization.identity, role: authorization.role}
}

func (s *Server) listAIModels(w http.ResponseWriter, r *http.Request) {
	models, _, err := s.availableModels(r.Context(), siteCaller(r))
	if err != nil {
		writeServerError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, models)
}

func (s *Server) readAIRequest(w http.ResponseWriter, r *http.Request) (AIRequest, map[string]serverTool, bool) {
	data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxAIRequestBytes))
	if err != nil {
		writeError(w, http.StatusRequestEntityTooLarge, "AI requests are limited to 8 MiB")
		return AIRequest{}, nil, false
	}
	var request SiteAIRequest
	if err := json.Unmarshal(data, &request); err != nil {
		writeError(w, http.StatusBadRequest, "expected a JSON AI request: "+err.Error())
		return AIRequest{}, nil, false
	}
	prepared, tools, err := s.prepareAIRequest(r.Context(), siteCaller(r), request)
	if err != nil {
		writeAIError(w, err)
		return AIRequest{}, nil, false
	}
	return prepared, tools, true
}

func writeAIError(w http.ResponseWriter, err error) {
	var aiError *AIError
	if errors.As(err, &aiError) {
		writeError(w, aiError.Status, aiError.Message)
		return
	}
	writeServerError(w, err)
}

// streamAI answers with server-sent events, one AIEvent per "data:" line,
// named by the event type. Comments keep idle connections alive while the
// model thinks silently or a tool runs.
func (s *Server) streamAI(w http.ResponseWriter, r *http.Request) {
	request, tools, ok := s.readAIRequest(w, r)
	if !ok {
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeServerError(w, errors.New("the response writer cannot stream"))
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	var writeMutex sync.Mutex
	write := func(payload string) error {
		writeMutex.Lock()
		defer writeMutex.Unlock()
		if _, err := io.WriteString(w, payload); err != nil {
			return err
		}
		flusher.Flush()
		return nil
	}
	emit := func(event AIEvent) error {
		encoded, err := json.Marshal(event)
		if err != nil {
			return err
		}
		return write("event: " + event.Type + "\ndata: " + string(encoded) + "\n\n")
	}

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	go func() {
		ticker := time.NewTicker(aiHeartbeatInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if err := write(": keep-alive\n\n"); err != nil {
					cancel()
					return
				}
			}
		}
	}()

	if _, err := s.runAI(ctx, siteCaller(r), request, tools, emit); err != nil && ctx.Err() == nil {
		slogAIError("AI stream", err)
		if emitError := emit(AIEvent{Type: EventError, Error: aiFailureMessage(err)}); emitError != nil {
			slogAIError("report AI stream failure", emitError)
		}
	}
}

// AICompletion is the non-streaming answer: every message the turn added
// (assistant output and server tool results), in order, plus the outcome.
type AICompletion struct {
	Messages   []AIMessage `json:"messages"`
	Text       string      `json:"text"`
	StopReason string      `json:"stopReason"`
	Usage      AIUsage     `json:"usage"`
}

func (s *Server) completeAI(w http.ResponseWriter, r *http.Request) {
	request, tools, ok := s.readAIRequest(w, r)
	if !ok {
		return
	}
	completion, err := s.completeConversation(r.Context(), siteCaller(r), request, tools)
	if err != nil {
		var aiError *AIError
		if errors.As(err, &aiError) {
			writeError(w, aiError.Status, aiError.Message)
			return
		}
		slogAIError("AI completion", err)
		writeError(w, http.StatusBadGateway, aiFailureMessage(err))
		return
	}
	writeJSON(w, http.StatusOK, completion)
}

// completeConversation runs a turn without streaming; automations use it too.
func (s *Server) completeConversation(ctx context.Context, caller integrationCaller, request AIRequest, tools map[string]serverTool) (AICompletion, error) {
	completion := AICompletion{Messages: []AIMessage{}}
	collect := func(event AIEvent) error {
		switch event.Type {
		case EventMessage:
			completion.Messages = append(completion.Messages, *event.Message)
			if event.Message.Role == RoleAssistant {
				completion.Text = messageText(event.Message)
			}
		case EventDone:
			completion.StopReason = event.StopReason
		}
		return nil
	}
	usage, err := s.runAI(ctx, caller, request, tools, collect)
	completion.Usage = usage
	return completion, err
}

func messageText(message *AIMessage) string {
	text := ""
	for _, content := range message.Content {
		if content.Type == ContentText {
			text += content.Text
		}
	}
	return text
}

func aiFailureMessage(err error) string {
	var aiError *AIError
	if errors.As(err, &aiError) {
		return aiError.Message
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "the model did not answer in time"
	}
	return "the model request failed"
}

func slogAIError(operation string, err error) {
	slog.Error(operation, "error", err)
}

func logAIUsage(caller integrationCaller, model string, usage AIUsage) {
	slog.Info("ai usage", "site", caller.site, "caller", caller.label(), "model", model,
		"inputTokens", usage.InputTokens, "outputTokens", usage.OutputTokens)
}

// ProviderError builds an AIError from a model provider's error status, so
// apps see rate limits and invalid requests while other failures stay
// internal.
func ProviderError(provider string, status int, body []byte) error {
	detail := string(body)
	if len(detail) > 300 {
		detail = detail[:300] + "…"
	}
	switch status {
	case http.StatusBadRequest, http.StatusUnprocessableEntity:
		return &AIError{Status: http.StatusBadRequest, Message: fmt.Sprintf("%s rejected the request: %s", provider, detail)}
	case http.StatusTooManyRequests:
		return &AIError{Status: http.StatusTooManyRequests, Message: provider + " is rate limiting requests; try again shortly"}
	default:
		return fmt.Errorf("%s answered %d: %s", provider, status, detail)
	}
}

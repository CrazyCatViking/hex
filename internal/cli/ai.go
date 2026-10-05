package cli

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"

	hex "github.com/crazycatviking/hex/server"
	"github.com/spf13/cobra"
)

var thinkingEfforts = []string{"low", "medium", "high", "xhigh", "max"}

const maxStreamLineBytes = 4 << 20

func (a *App) aiCommand() *cobra.Command {
	options := &siteCommandOptions{}
	command := &cobra.Command{Use: "ai", Short: "Use the platform's language models"}
	options.register(command)

	models := &cobra.Command{
		Use: "models", Short: "List the models you may use on a site", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			project, err := options.config(a)
			if err != nil {
				return err
			}
			data, err := a.apiRequest(cmd.Context(), project, "/api/sites/"+options.site+"/ai/models")
			if err != nil {
				return aiUnavailable(err)
			}
			return a.printJSON(data)
		},
	}
	command.AddCommand(models, a.aiAskCommand(options))
	return command
}

func aiUnavailable(err error) error {
	var status *apiStatusError
	if errors.As(err, &status) && status.Status == http.StatusNotFound && status.Message == "" {
		return errors.New("this platform has no AI capability")
	}
	return err
}

func (a *App) aiAskCommand(options *siteCommandOptions) *cobra.Command {
	var model, system, thinking string
	var tools []string
	var showThinking, jsonOutput bool
	command := &cobra.Command{
		Use:   "ask <prompt>",
		Short: "Ask a model, streaming its answer",
		Long: "Ask a model on a site, as yourself. --tools offers integration endpoints " +
			"(such as crm.deals or crm.*) that the platform runs with your permissions. " +
			"Without --model the first model you may use is chosen.",
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if thinking != "" && !slices.Contains(thinkingEfforts, thinking) {
				return fmt.Errorf("--thinking must be one of %s", strings.Join(thinkingEfforts, ", "))
			}
			project, err := options.config(a)
			if err != nil {
				return err
			}
			if model == "" {
				model, err = a.defaultModel(cmd.Context(), project, options.site)
				if err != nil {
					return err
				}
			}

			request := hex.SiteAIRequest{
				AIRequest: hex.AIRequest{
					Model: model, System: system,
					Messages: []hex.AIMessage{{
						Role:    hex.RoleUser,
						Content: []hex.AIContent{{Type: hex.ContentText, Text: strings.Join(args, " ")}},
					}},
				},
				IntegrationTools: tools,
			}
			if thinking != "" || showThinking {
				request.Thinking = &hex.AIThinking{Effort: thinking, Show: showThinking}
			}

			base := "/api/sites/" + options.site + "/ai/"
			if jsonOutput {
				data, err := a.apiCallWithoutTimeout(cmd.Context(), project, http.MethodPost, base+"complete", request)
				if err != nil {
					return aiUnavailable(err)
				}
				return a.printJSON(data)
			}
			return a.streamAnswer(cmd.Context(), project, base+"stream", request)
		},
	}
	flags := command.Flags()
	flags.StringVar(&model, "model", "", "Model ID (see hex ai models)")
	flags.StringVar(&system, "system", "", "System prompt")
	flags.StringVar(&thinking, "thinking", "", "Reasoning effort: low, medium, high, xhigh or max")
	flags.BoolVar(&showThinking, "show-thinking", false, "Print the model's reasoning to stderr")
	flags.StringSliceVar(&tools, "tools", nil, "Integration endpoints the model may call, comma-separated")
	flags.BoolVar(&jsonOutput, "json", false, "Print the complete answer as JSON instead of streaming")
	return command
}

func (a *App) defaultModel(ctx context.Context, project Project, site string) (string, error) {
	data, err := a.apiRequest(ctx, project, "/api/sites/"+site+"/ai/models")
	if err != nil {
		return "", aiUnavailable(err)
	}
	var models []hex.AIModel
	if err := json.Unmarshal(data, &models); err != nil {
		return "", fmt.Errorf("unexpected model list: %w", err)
	}
	if len(models) == 0 {
		return "", errors.New("you may not use any model on this site")
	}
	return models[0].ID, nil
}

// streamAnswer prints text to stdout as it arrives and reasoning, tool use
// and the outcome to stderr.
func (a *App) streamAnswer(ctx context.Context, project Project, path string, request hex.SiteAIRequest) error {
	payload, err := json.Marshal(request)
	if err != nil {
		return err
	}
	httpRequest, err := a.newAPIRequest(ctx, project, http.MethodPost, path, bytes.NewReader(payload), int64(len(payload)), "application/json")
	if err != nil {
		return err
	}
	httpRequest.Header.Set("Accept", "text/event-stream")
	streaming := *a.HTTP
	streaming.Timeout = 0
	response, err := streaming.Do(httpRequest)
	if err != nil {
		return err
	}
	defer closeLogged(a.Err, response.Body)
	if err := gatewayError(response); err != nil {
		return err
	}
	if response.StatusCode != http.StatusOK {
		return aiUnavailable(apiResponseError(response))
	}
	if !strings.HasPrefix(response.Header.Get("Content-Type"), "text/event-stream") {
		return errors.New("expected an event stream from Hex")
	}
	return a.printEvents(response.Body)
}

func (a *App) printEvents(body io.Reader) error {
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 64<<10), maxStreamLineBytes)
	thinking := false
	for scanner.Scan() {
		data, found := strings.CutPrefix(scanner.Text(), "data: ")
		if !found {
			continue
		}
		var event hex.AIEvent
		if err := json.Unmarshal([]byte(data), &event); err != nil {
			return fmt.Errorf("invalid event from Hex: %w", err)
		}
		if thinking && event.Type != hex.EventThinking {
			fmt.Fprintln(a.Err)
			thinking = false
		}
		switch event.Type {
		case hex.EventText:
			fmt.Fprint(a.Out, event.Text)
		case hex.EventThinking:
			if !thinking {
				fmt.Fprint(a.Err, "[thinking] ")
				thinking = true
			}
			fmt.Fprint(a.Err, event.Text)
		case hex.EventToolCall:
			fmt.Fprintf(a.Err, "\n→ %s %s\n", event.Content.Name, compactJSON(event.Content.Input))
		case hex.EventToolResult:
			outcome := "done"
			if event.Content.IsError {
				outcome = "failed: " + event.Content.Text
			}
			fmt.Fprintf(a.Err, "← %s %s\n", event.Content.Name, outcome)
		case hex.EventError:
			fmt.Fprintln(a.Out)
			return errors.New(event.Error)
		case hex.EventDone:
			fmt.Fprintln(a.Out)
			return a.reportOutcome(event)
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("read answer: %w", err)
	}
	return errors.New("the answer ended unexpectedly")
}

func (a *App) reportOutcome(event hex.AIEvent) error {
	if event.Usage != nil {
		fmt.Fprintf(a.Err, "(%d input and %d output tokens)\n", event.Usage.InputTokens, event.Usage.OutputTokens)
	}
	switch event.StopReason {
	case hex.StopMaxTokens:
		fmt.Fprintln(a.Err, "The answer reached its length limit.")
	case hex.StopRefusal:
		return errors.New("the model declined to answer")
	case hex.StopToolUse:
		fmt.Fprintln(a.Err, "The model wanted to keep using tools; ask a narrower question to finish within the tool limit.")
	}
	return nil
}

func compactJSON(value json.RawMessage) string {
	var buffer bytes.Buffer
	if json.Compact(&buffer, value) != nil {
		return string(value)
	}
	if buffer.Len() > 200 {
		return buffer.String()[:200] + "…"
	}
	return buffer.String()
}

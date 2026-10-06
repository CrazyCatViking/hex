package hex

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/crazycatviking/hex/internal/automationjs"
)

const (
	maxScriptCalls       = 100
	maxScriptLogBytes    = 32 << 10
	maxScannedDocuments  = 1000
	maxScannedBytes      = 4 << 20
	hostOperationTimeout = 30 * time.Second
	hostRunTimeBudget    = 2 * time.Minute
)

var errAutomationHostBudget = errors.New("automation host work budget exceeded")

var ErrAutomationValidationBusy = errors.New("automation validation is busy; try again shortly")
var validationAdmissions = make(chan struct{}, 4)

type automationAIRequest struct {
	Model     string      `json:"model"`
	System    string      `json:"system,omitempty"`
	Prompt    string      `json:"prompt"`
	MaxTokens int         `json:"maxTokens,omitempty"`
	Thinking  *AIThinking `json:"thinking,omitempty"`
	Tools     []string    `json:"tools,omitempty"`
}

func validateAutomationScript(ctx context.Context, script AutomationScript) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if script.File != "" && (path.Ext(script.File) != ".js" || len(script.File) > 255 || strings.ContainsRune(script.File, 0)) {
		return errors.New("script file must be a JavaScript (.js) filename of at most 255 bytes")
	}
	if strings.TrimSpace(script.Source) == "" || len(script.Source) > automationjs.MaxSourceBytes || !utf8.ValidString(script.Source) || strings.ContainsRune(script.Source, 0) {
		return fmt.Errorf("script needs UTF-8 JavaScript source of at most %d bytes", automationjs.MaxSourceBytes)
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	select {
	case validationAdmissions <- struct{}{}:
		defer func() { <-validationAdmissions }()
	default:
		return ErrAutomationValidationBusy
	}
	return automationjs.Validate(ctx, script.Source, scriptFilename(script, "script"))
}

func scriptFilename(script AutomationScript, name string) string {
	if script.File != "" {
		return script.File
	}
	return name + ".js"
}

func (r *automationRunner) executeScript(ctx context.Context) (json.RawMessage, error) {
	if r.automation.Script == nil {
		return nil, errors.New("automation requires a JavaScript script; redeploy its definition")
	}
	location, err := automationLocation(r.automation)
	if err != nil {
		return nil, err
	}
	metadata := map[string]any{
		"site":       r.site,
		"automation": r.automation.Name,
		"run":        map[string]any{"id": r.run.ID, "trigger": r.run.Trigger, "dryRun": r.run.DryRun},
		"now":        clockValues(r.run.StartedAt.In(location)),
	}
	encoded, err := json.Marshal(metadata)
	if err != nil {
		return nil, err
	}
	output, err := automationjs.Execute(ctx, r.automation.Script.Source, scriptFilename(*r.automation.Script, r.automation.Name), encoded, r.scriptInvoke)
	if err != nil {
		return nil, err
	}
	if r.scriptCalls > maxScriptCalls {
		return nil, errors.New("script exceeded its 100-operation run limit")
	}
	if r.hostBudgetError != nil {
		return nil, r.hostBudgetError
	}
	return output, nil
}

func decodeScriptInput(input json.RawMessage, value any) error {
	decoder := json.NewDecoder(bytes.NewReader(input))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		return fmt.Errorf("invalid operation input: %w", err)
	}
	return nil
}

func (r *automationRunner) scriptInvoke(ctx context.Context, kind string, input json.RawMessage) (output any, err error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	r.scriptCalls++
	if r.scriptCalls > maxScriptCalls {
		return nil, errors.New("script exceeded its 100-operation run limit")
	}
	if kind == "log" {
		return r.scriptLog(input)
	}
	if r.hostBudgetError != nil {
		return nil, r.hostBudgetError
	}
	remaining := hostRunTimeBudget - r.hostTimeUsed
	if remaining <= 0 {
		r.hostBudgetError = errAutomationHostBudget
		return nil, r.hostBudgetError
	}
	ctx, cancel := context.WithTimeout(ctx, min(hostOperationTimeout, remaining))
	defer cancel()
	ctx = context.WithValue(ctx, automationResultLimitKey{}, true)
	operation := AutomationOperation{Kind: kind, Status: OperationSucceeded}
	started := time.Now()
	defer func() {
		r.hostTimeUsed += time.Since(started)
		if r.hostTimeUsed >= hostRunTimeBudget {
			r.hostBudgetError = errAutomationHostBudget
		}
	}()
	defer func() {
		operation.DurationMS = time.Since(started).Milliseconds()
		operation.Target = boundedScriptText(operation.Target, 256)
		if err != nil {
			operation.Status, operation.Error = OperationFailed, boundedScriptText(err.Error(), 8192)
			err = errors.New(operation.Error)
		} else {
			encoded, encodeErr := marshalHostResult(ctx, output)
			if encodeErr != nil || len(encoded) > automationjs.MaxJSONBytes-128 {
				err = errors.New("operation output exceeds 1 MiB or is not JSON")
				output = nil
				operation.Status, operation.Error = OperationFailed, err.Error()
			} else {
				operation.Output, operation.Truncated = truncateOutput(encoded)
			}
		}
		r.run.Operations = append(r.run.Operations, operation)
	}()
	switch kind {
	case "call", "action":
		var request struct {
			Name  string          `json:"name"`
			Input json.RawMessage `json:"input"`
		}
		if err := decodeScriptInput(input, &request); err != nil {
			return nil, err
		}
		operation.Target = request.Name
		if len(request.Input) == 0 {
			request.Input = json.RawMessage(`{}`)
		}
		var status string
		if kind == "call" {
			integration, endpoint, found := strings.Cut(request.Name, ".")
			if !found || !integrationNamePattern.MatchString(integration) || !integrationNamePattern.MatchString(endpoint) {
				return nil, errors.New("call must name <integration>.<endpoint>")
			}
			output, status, err = r.callEndpoint(ctx, request.Name, request.Input)
		} else {
			if !namePattern.MatchString(request.Name) {
				return nil, errors.New("invalid action name")
			}
			output, status, err = r.runAction(ctx, request.Name, request.Input)
		}
		if status != "" {
			operation.Status = status
		}
		return output, err
	case "ai":
		var request automationAIRequest
		if err := decodeScriptInput(input, &request); err != nil {
			return nil, err
		}
		if request.Model == "" || strings.TrimSpace(request.Prompt) == "" {
			return nil, errors.New("ai needs a model and a prompt")
		}
		operation.Target = request.Model
		output, err = r.askAI(ctx, &request)
		if err != nil {
			var aiError *AIError
			if errors.As(err, &aiError) {
				return nil, errors.New(aiError.Message)
			}
			return nil, automationError(err)
		}
		return output, nil
	case "query":
		var request struct {
			Collection string         `json:"collection"`
			Where      map[string]any `json:"where"`
			Limit      int            `json:"limit"`
		}
		if err := decodeScriptInput(input, &request); err != nil {
			return nil, err
		}
		if !namePattern.MatchString(request.Collection) || request.Limit < 0 || request.Limit > maxQueryDocuments {
			return nil, errors.New("query needs a valid collection and a limit from 0 to 1000")
		}
		operation.Target = request.Collection
		return r.queryDocuments(ctx, request.Collection, request.Where, request.Limit)
	case "save":
		var request struct {
			Collection string          `json:"collection"`
			ID         string          `json:"id,omitempty"`
			Data       json.RawMessage `json:"data"`
		}
		if err := decodeScriptInput(input, &request); err != nil {
			return nil, err
		}
		if !namePattern.MatchString(request.Collection) {
			return nil, errors.New("save needs a valid collection")
		}
		operation.Target = request.Collection
		var status string
		output, status, err = r.saveDocument(ctx, request.Collection, request.ID, request.Data)
		if status != "" {
			operation.Status = status
		}
		return output, err
	default:
		return nil, fmt.Errorf("unknown script operation %q", kind)
	}
}

func boundedScriptText(text string, limit int) string {
	if len(text) <= limit {
		return text
	}
	end := limit
	for end > 0 && !utf8.RuneStart(text[end]) {
		end--
	}
	return text[:end] + "…"
}

func (r *automationRunner) scriptLog(input json.RawMessage) (any, error) {
	var log AutomationLog
	if err := decodeScriptInput(input, &log); err != nil {
		return nil, err
	}
	size := len(log.Message) + len(log.Data)
	for _, earlier := range r.run.Logs {
		size += len(earlier.Message) + len(earlier.Data)
	}
	if size > maxScriptLogBytes {
		return nil, errors.New("script logs are limited to 32 KiB per run")
	}
	r.run.Logs = append(r.run.Logs, log)
	return nil, nil
}

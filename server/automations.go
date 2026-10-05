package hex

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/robfig/cron/v3"
)

// Automations let a published app run work on a schedule or on demand
// without server code. Each automation is declared in JSON — in the app's
// hex.json or one file per automation under automations/ — as a list of
// steps: calling integration endpoints, running the site's actions, asking
// a model, and reading or saving the site's own documents. Templates pass
// values between steps. Automations run as the site, never as a person:
// integration grants name them with "site:<name>" (or "*"), integrations
// that require approval need it for the site, and connected accounts are
// unavailable to them.

// Automation is one declared automation. Schedule is a five-field cron
// expression or a descriptor such as @daily, evaluated in Timezone (an IANA
// name, UTC by default); without one it only runs when triggered.
type Automation struct {
	Name        string           `json:"name"`
	Description string           `json:"description,omitempty"`
	Schedule    string           `json:"schedule,omitempty"`
	Timezone    string           `json:"timezone,omitempty"`
	Disabled    bool             `json:"disabled,omitempty"`
	Steps       []AutomationStep `json:"steps"`
}

// AutomationStep does exactly one of: Call an integration endpoint
// ("<integration>.<endpoint>") or run a site Action with Input; ask AI;
// Query the site's documents; or Save one. If skips the step when its
// template is falsy. ForEach repeats the step for each item of a template's
// list, exposing item and index; the step's output is then the list of
// outputs.
type AutomationStep struct {
	ID      string           `json:"id"`
	If      string           `json:"if,omitempty"`
	ForEach string           `json:"forEach,omitempty"`
	Call    string           `json:"call,omitempty"`
	Action  string           `json:"action,omitempty"`
	Input   json.RawMessage  `json:"input,omitempty"`
	AI      *AutomationAI    `json:"ai,omitempty"`
	Query   *AutomationQuery `json:"query,omitempty"`
	Save    *AutomationSave  `json:"save,omitempty"`
}

// AutomationAI asks a model; Tools offers integration endpoints by name or
// pattern, which the server runs with the automation's grants.
type AutomationAI struct {
	Model     string      `json:"model"`
	System    string      `json:"system,omitempty"`
	Prompt    string      `json:"prompt"`
	MaxTokens int         `json:"maxTokens,omitempty"`
	Thinking  *AIThinking `json:"thinking,omitempty"`
	Tools     []string    `json:"tools,omitempty"`
}

// AutomationQuery lists up to Limit documents of a collection whose
// top-level fields equal every Where value.
type AutomationQuery struct {
	Collection string                     `json:"collection"`
	Where      map[string]json.RawMessage `json:"where,omitempty"`
	Limit      int                        `json:"limit,omitempty"`
}

// AutomationSave creates a document, or replaces the one with ID.
type AutomationSave struct {
	Collection string          `json:"collection"`
	ID         string          `json:"id,omitempty"`
	Data       json.RawMessage `json:"data"`
}

// ScheduledAutomation is a stored automation with its next scheduled run;
// a zero NextRun means it only runs when triggered.
type ScheduledAutomation struct {
	Site       string     `json:"site"`
	Automation Automation `json:"automation"`
	NextRun    time.Time  `json:"nextRun,omitzero"`
	UpdatedAt  time.Time  `json:"updatedAt"`
	UpdatedBy  *Person    `json:"updatedBy,omitempty"`
}

// AutomationRun records one run and what each step did.
type AutomationRun struct {
	ID         string              `json:"id"`
	Site       string              `json:"site"`
	Automation string              `json:"automation"`
	Trigger    string              `json:"trigger"`
	DryRun     bool                `json:"dryRun,omitempty"`
	Status     string              `json:"status"`
	StartedAt  time.Time           `json:"startedAt"`
	FinishedAt time.Time           `json:"finishedAt,omitzero"`
	Error      string              `json:"error,omitempty"`
	StartedBy  *Person             `json:"startedBy,omitempty"`
	Steps      []AutomationStepRun `json:"steps"`
}

// AutomationStepRun is one step's outcome; Output is truncated for storage.
type AutomationStepRun struct {
	ID         string          `json:"id"`
	Status     string          `json:"status"`
	Output     json.RawMessage `json:"output,omitempty"`
	Truncated  bool            `json:"truncated,omitempty"`
	Error      string          `json:"error,omitempty"`
	DurationMS int64           `json:"durationMs"`
}

const (
	RunRunning   = "running"
	RunSucceeded = "succeeded"
	RunFailed    = "failed"

	StepSucceeded = "succeeded"
	StepSkipped   = "skipped"
	StepFailed    = "failed"
	StepDryRun    = "dry-run"

	TriggerSchedule = "schedule"
	TriggerManual   = "manual"
	TriggerTest     = "test"
)

// AutomationStore keeps each site's automations, schedules and run history.
// ClaimAutomation atomically moves an automation's NextRun from expected to
// next and reports whether this caller won, so only one instance runs each
// scheduled occurrence. Stores keep at least the latest 50 runs per
// automation.
type AutomationStore interface {
	ReplaceSiteAutomations(ctx context.Context, site string, automations []ScheduledAutomation) error
	ListSiteAutomations(ctx context.Context, site string) ([]ScheduledAutomation, error)
	DueAutomations(ctx context.Context, now time.Time, limit int) ([]ScheduledAutomation, error)
	ClaimAutomation(ctx context.Context, site, name string, expected, next time.Time) (bool, error)
	RecordAutomationRun(ctx context.Context, run AutomationRun) error
	ListAutomationRuns(ctx context.Context, site, name string, limit int) ([]AutomationRun, error)
	GetAutomationRun(ctx context.Context, site, id string) (AutomationRun, error)
}

const (
	maxAutomations          = 32
	maxAutomationSteps      = 20
	maxForEachItems         = 100
	maxQueryDocuments       = 1000
	maxStoredStepOutput     = 16 << 10
	maxStepOutputInMemory   = 1 << 20
	automationRunTimeout    = 10 * time.Minute
	minimumScheduleInterval = 5 * time.Minute
)

var cronParser = cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow | cron.Descriptor)

func (s *Server) automationsEnabled() bool {
	return s.config.Automations != nil && s.config.Publisher != nil
}

// ValidateAutomations checks a site's set of automations.
func ValidateAutomations(automations []Automation) error {
	if len(automations) > maxAutomations {
		return fmt.Errorf("a site can have at most %d automations", maxAutomations)
	}
	names := make(map[string]bool)
	for _, automation := range automations {
		if err := validateAutomation(automation); err != nil {
			return err
		}
		if names[automation.Name] {
			return fmt.Errorf("automation %s is declared more than once", automation.Name)
		}
		names[automation.Name] = true
	}
	return nil
}

func validateAutomation(automation Automation) error {
	if !namePattern.MatchString(automation.Name) {
		return fmt.Errorf("invalid automation name %q; use letters, digits, - and _", automation.Name)
	}
	label := "automation " + automation.Name
	if utf8.RuneCountInString(automation.Description) > maxActionDescriptionSize {
		return fmt.Errorf("%s: the description is longer than %d characters", label, maxActionDescriptionSize)
	}
	if _, err := automationLocation(automation); err != nil {
		return fmt.Errorf("%s: %w", label, err)
	}
	if automation.Schedule != "" {
		if err := validateSchedule(automation); err != nil {
			return fmt.Errorf("%s: %w", label, err)
		}
	}
	if len(automation.Steps) == 0 || len(automation.Steps) > maxAutomationSteps {
		return fmt.Errorf("%s needs 1–%d steps", label, maxAutomationSteps)
	}

	earlier := make(map[string]bool)
	for index, step := range automation.Steps {
		stepLabel := fmt.Sprintf("%s step %d", label, index+1)
		if step.ID != "" {
			stepLabel = label + " step " + step.ID
		}
		if err := validateStep(step, earlier); err != nil {
			return fmt.Errorf("%s: %w", stepLabel, err)
		}
		earlier[step.ID] = true
	}
	return nil
}

func validateStep(step AutomationStep, earlier map[string]bool) error {
	if !namePattern.MatchString(step.ID) {
		return errors.New("needs an id of letters, digits, - and _")
	}
	if earlier[step.ID] {
		return errors.New("its id is used by an earlier step")
	}

	kinds := 0
	for _, present := range []bool{step.Call != "", step.Action != "", step.AI != nil, step.Query != nil, step.Save != nil} {
		if present {
			kinds++
		}
	}
	if kinds != 1 {
		return errors.New("needs exactly one of call, action, ai, query or save")
	}
	if len(step.Input) > 0 && step.Call == "" && step.Action == "" {
		return errors.New("input applies to call and action steps")
	}
	if step.Call != "" {
		integration, endpoint, found := strings.Cut(step.Call, ".")
		if !found || !integrationNamePattern.MatchString(integration) || !integrationNamePattern.MatchString(endpoint) {
			return fmt.Errorf("call %q must name <integration>.<endpoint>", step.Call)
		}
	}
	if step.Action != "" && !namePattern.MatchString(step.Action) {
		return fmt.Errorf("invalid action %q", step.Action)
	}
	if step.AI != nil && (step.AI.Model == "" || strings.TrimSpace(step.AI.Prompt) == "") {
		return errors.New("ai needs a model and a prompt")
	}
	if step.Query != nil {
		if !namePattern.MatchString(step.Query.Collection) {
			return errors.New("query needs a valid collection")
		}
		if step.Query.Limit < 0 || step.Query.Limit > maxQueryDocuments {
			return fmt.Errorf("query limit must be at most %d", maxQueryDocuments)
		}
	}
	if step.Save != nil {
		if !namePattern.MatchString(step.Save.Collection) || len(step.Save.Data) == 0 {
			return errors.New("save needs a valid collection and data")
		}
	}
	for _, document := range []json.RawMessage{step.Input, saveData(step)} {
		if len(document) > 0 && !json.Valid(document) {
			return errors.New("input and data must be valid JSON")
		}
	}

	for _, reference := range stepTemplateReferences(step) {
		if !earlier[reference] {
			return fmt.Errorf("refers to step %q, which does not run before it", reference)
		}
	}
	return nil
}

func saveData(step AutomationStep) json.RawMessage {
	if step.Save == nil {
		return nil
	}
	return step.Save.Data
}

func stepTemplateReferences(step AutomationStep) []string {
	texts := []string{step.If, step.ForEach, string(step.Input)}
	if step.AI != nil {
		texts = append(texts, step.AI.System, step.AI.Prompt)
	}
	if step.Save != nil {
		texts = append(texts, step.Save.ID, string(step.Save.Data))
	}
	if step.Query != nil {
		for _, value := range step.Query.Where {
			texts = append(texts, string(value))
		}
	}
	var references []string
	for _, text := range texts {
		references = append(references, templateReferences(text)...)
	}
	return references
}

func automationLocation(automation Automation) (*time.Location, error) {
	if automation.Timezone == "" {
		return time.UTC, nil
	}
	location, err := time.LoadLocation(automation.Timezone)
	if err != nil {
		return nil, fmt.Errorf("unknown time zone %q", automation.Timezone)
	}
	return location, nil
}

// validateSchedule parses the schedule and refuses ones that run more often
// than every five minutes.
func validateSchedule(automation Automation) error {
	schedule, err := cronParser.Parse(automation.Schedule)
	if err != nil {
		return fmt.Errorf("invalid schedule %q: %w", automation.Schedule, err)
	}
	location, err := automationLocation(automation)
	if err != nil {
		return err
	}
	start := time.Date(2026, time.January, 5, 0, 0, 0, 0, location)
	previous := schedule.Next(start)
	for range 300 {
		next := schedule.Next(previous)
		if next.IsZero() {
			break
		}
		if next.Sub(previous) < minimumScheduleInterval {
			return fmt.Errorf("schedule %q runs more often than every %s", automation.Schedule, minimumScheduleInterval)
		}
		previous = next
	}
	return nil
}

// nextRun is when the automation is next due after now, or zero when it has
// no schedule or is disabled.
func nextRun(automation Automation, now time.Time) time.Time {
	if automation.Disabled || automation.Schedule == "" {
		return time.Time{}
	}
	schedule, err := cronParser.Parse(automation.Schedule)
	if err != nil {
		return time.Time{}
	}
	location, err := automationLocation(automation)
	if err != nil {
		return time.Time{}
	}
	return schedule.Next(now.In(location)).UTC()
}

// replaceAutomations stores a site's new set, keeping the next run of
// automations whose schedule did not change.
func (s *Server) replaceAutomations(ctx context.Context, site string, automations []Automation, identity *Identity) error {
	if len(automations) > 0 && !s.automationsEnabled() {
		return errors.New("this platform has no automation store, so it cannot run automations")
	}
	if !s.automationsEnabled() {
		return nil
	}
	existing, err := s.config.Automations.ListSiteAutomations(ctx, site)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	scheduled := make([]ScheduledAutomation, 0, len(automations))
	for _, automation := range automations {
		entry := ScheduledAutomation{
			Site: site, Automation: automation, NextRun: nextRun(automation, now),
			UpdatedAt: now, UpdatedBy: personOf(identity),
		}
		index := slices.IndexFunc(existing, func(previous ScheduledAutomation) bool {
			return previous.Automation.Name == automation.Name
		})
		if index >= 0 && sameSchedule(existing[index].Automation, automation) && !existing[index].NextRun.IsZero() {
			entry.NextRun = existing[index].NextRun
		}
		scheduled = append(scheduled, entry)
	}
	return s.config.Automations.ReplaceSiteAutomations(ctx, site, scheduled)
}

func sameSchedule(a, b Automation) bool {
	return a.Schedule == b.Schedule && a.Timezone == b.Timezone && a.Disabled == b.Disabled
}

// automationRunner carries one run's state.
type automationRunner struct {
	server     *Server
	site       string
	automation Automation
	run        *AutomationRun
	data       map[string]any
	caller     integrationCaller
}

// runAutomation performs every step in order and records the run. Steps
// stop at the first failure. Dry runs skip write endpoints, actions and
// saves, recording the input they would have used.
func (s *Server) runAutomation(ctx context.Context, site string, automation Automation, run *AutomationRun) {
	ctx, cancel := context.WithTimeout(ctx, automationRunTimeout)
	defer cancel()

	location, _ := automationLocation(automation)
	steps := make(map[string]any)
	runner := &automationRunner{
		server: s, site: site, automation: automation, run: run,
		caller: integrationCaller{site: site, automation: automation.Name, role: roleOwner},
		data: map[string]any{
			"site":       site,
			"automation": automation.Name,
			"run":        map[string]any{"id": run.ID, "trigger": run.Trigger, "dryRun": run.DryRun},
			"now":        clockValues(run.StartedAt.In(location)),
			"steps":      steps,
		},
	}

	run.Status = RunSucceeded
	for _, step := range automation.Steps {
		result := runner.runStep(ctx, step, steps)
		run.Steps = append(run.Steps, result)
		if result.Status == StepFailed {
			run.Status = RunFailed
			run.Error = fmt.Sprintf("step %s failed: %s", step.ID, result.Error)
			break
		}
	}
	run.FinishedAt = time.Now().UTC()
	slog.Info("automation run", "site", site, "automation", automation.Name, "trigger", run.Trigger,
		"status", run.Status, "dryRun", run.DryRun, "duration", run.FinishedAt.Sub(run.StartedAt).Round(time.Millisecond))
	s.recordRun(*run)
}

func (s *Server) recordRun(run AutomationRun) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := s.config.Automations.RecordAutomationRun(ctx, run); err != nil {
		slog.Error("record automation run", "site", run.Site, "automation", run.Automation, "error", err)
	}
}

func (r *automationRunner) runStep(ctx context.Context, step AutomationStep, steps map[string]any) AutomationStepRun {
	started := time.Now()
	result := AutomationStepRun{ID: step.ID, Status: StepSucceeded}
	var finish func(output any, err error) AutomationStepRun
	finish = func(output any, err error) AutomationStepRun {
		result.DurationMS = time.Since(started).Milliseconds()
		if err != nil {
			result.Status = StepFailed
			result.Error = err.Error()
			steps[step.ID] = map[string]any{"status": StepFailed, "error": err.Error()}
			return result
		}
		encoded, encodeErr := json.Marshal(output)
		if encodeErr != nil {
			return finish(nil, encodeErr)
		}
		if len(encoded) > maxStepOutputInMemory {
			return finish(nil, fmt.Errorf("the output is larger than %d bytes", maxStepOutputInMemory))
		}
		var decoded any
		if err := json.Unmarshal(encoded, &decoded); err != nil {
			return finish(nil, err)
		}
		steps[step.ID] = map[string]any{"status": result.Status, "output": decoded}
		result.Output, result.Truncated = truncateOutput(encoded)
		return result
	}

	if step.If != "" {
		run, err := evaluateCondition(step.If, r.data)
		if err != nil {
			return finish(nil, err)
		}
		if !run {
			result.Status = StepSkipped
			return finish(nil, nil)
		}
	}
	if step.ForEach == "" {
		output, status, err := r.perform(ctx, step, r.data)
		if status != "" {
			result.Status = status
		}
		return finish(output, err)
	}

	items, err := renderString(placeholderOrPath(step.ForEach), r.data)
	if err != nil {
		return finish(nil, err)
	}
	list, isList := items.([]any)
	if items != nil && !isList {
		return finish(nil, errors.New("forEach must produce a list"))
	}
	if len(list) > maxForEachItems {
		return finish(nil, fmt.Errorf("forEach produced %d items; the limit is %d", len(list), maxForEachItems))
	}
	outputs := make([]any, 0, len(list))
	for index, item := range list {
		scope := make(map[string]any, len(r.data)+2)
		for key, value := range r.data {
			scope[key] = value
		}
		scope["item"] = item
		scope["index"] = float64(index)
		output, status, err := r.perform(ctx, step, scope)
		if err != nil {
			return finish(nil, fmt.Errorf("item %d: %w", index, err))
		}
		if status != "" {
			result.Status = status
		}
		outputs = append(outputs, output)
	}
	return finish(outputs, nil)
}

func placeholderOrPath(text string) string {
	if placeholderPattern.MatchString(text) {
		return text
	}
	return "{{ " + text + " }}"
}

func truncateOutput(encoded []byte) (json.RawMessage, bool) {
	if len(encoded) <= maxStoredStepOutput {
		return encoded, false
	}
	preview, err := json.Marshal(string(encoded[:maxStoredStepOutput]))
	if err != nil {
		return nil, true
	}
	return preview, true
}

// perform runs one step, or one forEach item, returning its output and a
// status other than succeeded when it did not run.
func (r *automationRunner) perform(ctx context.Context, step AutomationStep, data map[string]any) (any, string, error) {
	switch {
	case step.Call != "":
		return r.callStep(ctx, step, data)
	case step.Action != "":
		return r.actionStep(ctx, step, data)
	case step.AI != nil:
		output, err := r.aiStep(ctx, step.AI, data)
		return output, "", err
	case step.Query != nil:
		output, err := r.queryStep(ctx, step.Query, data)
		return output, "", err
	default:
		return r.saveStep(ctx, step.Save, data)
	}
}

func (r *automationRunner) callStep(ctx context.Context, step AutomationStep, data map[string]any) (any, string, error) {
	integration, endpointName, _ := strings.Cut(step.Call, ".")
	endpoint := r.server.config.Integrations.endpoint(integration, endpointName)
	if endpoint == nil {
		return nil, "", fmt.Errorf("there is no integration endpoint %s", step.Call)
	}
	input, err := renderTemplate(step.Input, data)
	if err != nil {
		return nil, "", err
	}
	if endpoint.endpoint.Write && r.run.DryRun {
		return map[string]any{"wouldCall": step.Call, "input": json.RawMessage(input)}, StepDryRun, nil
	}
	output, _, err := r.server.callIntegration(ctx, r.caller, endpoint, input)
	if err != nil {
		return nil, "", automationError(err)
	}
	return json.RawMessage(output), "", nil
}

func (r *automationRunner) actionStep(ctx context.Context, step AutomationStep, data map[string]any) (any, string, error) {
	actions, err := r.server.siteActions(ctx, r.site)
	if err != nil {
		return nil, "", err
	}
	index := slices.IndexFunc(actions, func(action *registeredAction) bool { return action.definition.Name == step.Action })
	if index < 0 {
		return nil, "", fmt.Errorf("the site has no action %s", step.Action)
	}
	action := actions[index]
	input, err := renderTemplate(step.Input, data)
	if err != nil {
		return nil, "", err
	}
	if err := validateActionJSON(action.input, input); err != nil {
		return nil, "", fmt.Errorf("invalid input for action %s: %w", step.Action, err)
	}
	if r.run.DryRun {
		return map[string]any{"wouldRun": step.Action, "input": json.RawMessage(input)}, StepDryRun, nil
	}

	authorization, err := r.server.automationAuthorization(ctx, r.site, r.automation.Name)
	if err != nil {
		return nil, "", err
	}
	caller := ActionContext{Site: r.site, Identity: authorization.identity, Role: roleNames[roleOwner], authorization: authorization}
	output, err := action.handler(ctx, caller, input)
	if err != nil {
		return nil, "", automationError(err)
	}
	return output, "", nil
}

// automationAuthorization lets an automation act as an owner of its own
// site, recording its documents as created by the automation.
func (s *Server) automationAuthorization(ctx context.Context, site, automation string) (siteAuthorization, error) {
	access, _, err := s.sitePolicy(ctx, site)
	if err != nil {
		return siteAuthorization{}, err
	}
	identity := &Identity{Provider: "automation", ID: "automation:" + automation, Name: automation}
	return siteAuthorization{identity: identity, access: access, role: roleOwner}, nil
}

func (r *automationRunner) aiStep(ctx context.Context, step *AutomationAI, data map[string]any) (any, error) {
	if !r.server.aiEnabled() {
		return nil, errors.New("this platform has no AI provider")
	}
	prompt, err := renderText(step.Prompt, data)
	if err != nil {
		return nil, err
	}
	system, err := renderText(step.System, data)
	if err != nil {
		return nil, err
	}
	request := SiteAIRequest{
		AIRequest: AIRequest{
			Model: step.Model, System: system, MaxTokens: step.MaxTokens, Thinking: step.Thinking,
			Messages: []AIMessage{{Role: RoleUser, Content: []AIContent{{Type: ContentText, Text: prompt}}}},
		},
		IntegrationTools: step.Tools,
	}
	prepared, tools, err := r.server.prepareAIRequest(ctx, r.caller, request)
	if err != nil {
		return nil, err
	}
	completion, err := r.server.completeConversation(ctx, r.caller, prepared, tools)
	if err != nil {
		return nil, err
	}
	return map[string]any{"text": completion.Text, "stopReason": completion.StopReason, "usage": completion.Usage}, nil
}

func (r *automationRunner) queryStep(ctx context.Context, query *AutomationQuery, data map[string]any) (any, error) {
	if r.server.config.Database == nil {
		return nil, errors.New("this platform has no database")
	}
	where := make(map[string]any, len(query.Where))
	for field, template := range query.Where {
		rendered, err := renderTemplate(template, data)
		if err != nil {
			return nil, err
		}
		var value any
		if err := json.Unmarshal(rendered, &value); err != nil {
			return nil, err
		}
		where[field] = value
	}
	limit := query.Limit
	if limit == 0 {
		limit = 100
	}

	matches := make([]any, 0)
	after := ""
	for len(matches) < limit {
		page, err := r.server.config.Database.List(ctx, r.site, query.Collection, ListOptions{After: after, Limit: 100})
		if err != nil {
			return nil, err
		}
		for _, document := range page {
			var fields map[string]any
			if err := json.Unmarshal(document.Data, &fields); err != nil {
				continue
			}
			if documentMatches(fields, where) && len(matches) < limit {
				matches = append(matches, map[string]any{"id": document.ID, "data": fields})
			}
		}
		if len(page) < 100 {
			break
		}
		after = page[len(page)-1].ID
	}
	return matches, nil
}

func documentMatches(fields, where map[string]any) bool {
	for field, wanted := range where {
		if templateText(fields[field]) != templateText(wanted) {
			return false
		}
	}
	return true
}

func (r *automationRunner) saveStep(ctx context.Context, save *AutomationSave, data map[string]any) (any, string, error) {
	if r.server.config.Database == nil {
		return nil, "", errors.New("this platform has no database")
	}
	document, err := renderTemplate(save.Data, data)
	if err != nil {
		return nil, "", err
	}
	var fields map[string]any
	if err := json.Unmarshal(document, &fields); err != nil || fields == nil {
		return nil, "", errors.New("save data must be a JSON object")
	}
	if len(document) > MaxActionInputBytes {
		return nil, "", errors.New("the document would exceed 1 MiB")
	}
	id, err := renderText(save.ID, data)
	if err != nil {
		return nil, "", err
	}
	if id == "" {
		id, err = newID()
		if err != nil {
			return nil, "", err
		}
	}
	if !namePattern.MatchString(id) {
		return nil, "", fmt.Errorf("invalid document ID %q", id)
	}
	if r.run.DryRun {
		return map[string]any{"wouldSave": save.Collection, "id": id, "data": json.RawMessage(document)}, StepDryRun, nil
	}
	options := WriteOptions{Creator: "automation:" + r.automation.Name}
	stored, err := r.server.config.Database.Put(ctx, r.site, save.Collection, id, document, options)
	if err != nil {
		return nil, "", err
	}
	return map[string]any{"id": stored.ID, "collection": save.Collection}, "", nil
}

func automationError(err error) error {
	var integrationError *IntegrationError
	var actionError *ActionError
	switch {
	case errors.As(err, &integrationError):
		return errors.New(integrationError.Message)
	case errors.As(err, &actionError):
		return errors.New(actionError.Message)
	case errors.Is(err, ErrForbidden):
		return errors.New("not permitted")
	default:
		slog.Error("automation step", "error", err)
		return errors.New("the request failed; see the platform log")
	}
}

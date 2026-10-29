package hex

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"time"
)

// Workflow jobs pin their script and input. Completed steps are stored in the
// same guarded record as the lease, so stale workers cannot commit checkpoints.
type Workflow struct {
	ActorProvider string                  `json:"actorProvider"`
	ID            string                  `json:"id"`
	Site          string                  `json:"site"`
	Actor         string                  `json:"actor"`
	Definition    ScheduledAutomation     `json:"definition"`
	Input         json.RawMessage         `json:"input"`
	Fingerprint   string                  `json:"fingerprint"`
	Status        string                  `json:"status"`
	Pending       bool                    `json:"pending"`
	Attempts      int                     `json:"attempts"`
	Failures      int                     `json:"failures"`
	CreatedAt     time.Time               `json:"createdAt"`
	NextAttempt   time.Time               `json:"nextAttempt"`
	Lease         string                  `json:"lease,omitempty"`
	LeaseUntil    time.Time               `json:"leaseUntil,omitzero"`
	RunID         string                  `json:"runId,omitempty"`
	Output        json.RawMessage         `json:"output,omitempty"`
	Error         string                  `json:"error,omitempty"`
	Steps         map[string]WorkflowStep `json:"steps"`
}

type WorkflowStep struct {
	Done   bool            `json:"done"`
	Output json.RawMessage `json:"output,omitempty"`
	WakeAt time.Time       `json:"wakeAt,omitzero"`
}

type workflowStart struct {
	Name           string          `json:"name"`
	Input          json.RawMessage `json:"input"`
	IdempotencyKey string          `json:"idempotencyKey"`
}

var errWorkflowSuspended = errors.New("workflow suspended until its durable wake time")

func (s *Server) startWorkflow(ctx context.Context, site string, authorization siteAuthorization, request workflowStart, dryRun bool) (Workflow, error) {
	if !namePattern.MatchString(request.Name) || len(request.IdempotencyKey) < 1 || len(request.IdempotencyKey) > 128 || len(request.Input) > maxEventBytes {
		return Workflow{}, &ActionError{Message: "workflow requires a name, input up to 64 KiB and a 1–128 byte idempotencyKey"}
	}
	if !authorization.permits("workflow", request.Name, "run") {
		return Workflow{}, ErrForbidden
	}
	if len(request.Input) == 0 {
		request.Input = json.RawMessage(`{}`)
	}
	definitions, err := s.config.Automations.ListSiteAutomations(ctx, site)
	if err != nil {
		return Workflow{}, err
	}
	index := slices.IndexFunc(definitions, func(a ScheduledAutomation) bool { return a.Automation.Name == request.Name })
	if index < 0 {
		return Workflow{}, ErrNotFound
	}
	definition := definitions[index]
	if definition.Automation.Disabled {
		return Workflow{}, ErrForbidden
	}
	if len(definition.Automation.Input) > 0 {
		schema, err := compileActionSchema(definition.Automation.Input)
		if err != nil {
			return Workflow{}, err
		}
		if err := validateActionJSON(schema, request.Input); err != nil {
			return Workflow{}, &actionInputError{err: err}
		}
	} else if !json.Valid(request.Input) {
		return Workflow{}, &ActionError{Message: "workflow input must be JSON"}
	}
	now := time.Now().UTC()
	workflow := Workflow{ID: internalKey(site, authorization.creator(), request.Name, request.IdempotencyKey), Site: site, Actor: authorization.creator(), Definition: definition, Input: request.Input, Fingerprint: internalKey(string(request.Input)), Status: "pending", Pending: true, CreatedAt: now, NextAttempt: now, Steps: map[string]WorkflowStep{}}
	if authorization.identity != nil {
		workflow.ActorProvider = authorization.identity.Provider
	}
	if dryRun {
		return workflow, nil
	}
	_, err = s.mutateSystem(ctx, "workflows", workflow.ID, func(current *Document) (json.RawMessage, error) {
		if current != nil {
			var previous Workflow
			if err := json.Unmarshal(current.Data, &previous); err != nil {
				return nil, err
			}
			if previous.Fingerprint != workflow.Fingerprint {
				return nil, errInvocationConflict
			}
			workflow = previous
			return current.Data, nil
		}
		return json.Marshal(workflow)
	})
	return workflow, err
}

func (s *Server) readWorkflow(ctx context.Context, site, id string, authorization siteAuthorization) (Workflow, error) {
	document, err := s.config.Database.Get(ctx, systemSite, "workflows", id)
	if err != nil {
		return Workflow{}, err
	}
	var workflow Workflow
	if err := json.Unmarshal(document.Data, &workflow); err != nil {
		return Workflow{}, err
	}
	if workflow.Site != site {
		return Workflow{}, ErrNotFound
	}
	if !authorization.permits("workflow", workflow.Definition.Automation.Name, "run") {
		return Workflow{}, ErrForbidden
	}
	return workflow, nil
}

func (s *Server) workflowStartHTTP(w http.ResponseWriter, r *http.Request) {
	var request workflowStart
	if !decodeEventRequest(w, r, &request) {
		return
	}
	workflow, err := s.startWorkflow(r.Context(), r.PathValue("site"), requestAuthorization(r), request, false)
	if err != nil {
		writeSiteCallError(w, err)
		return
	}
	writeJSON(w, 202, workflowSummary(workflow))
}

func workflowSummary(workflow Workflow) any {
	return map[string]any{"id": workflow.ID, "automation": workflow.Definition.Automation.Name, "status": workflow.Status, "attempts": workflow.Attempts, "failures": workflow.Failures, "nextAttempt": workflow.NextAttempt, "runId": workflow.RunID, "output": workflow.Output, "error": workflow.Error, "steps": workflow.Steps}
}

func (s *Server) workflowGetHTTP(w http.ResponseWriter, r *http.Request) {
	workflow, err := s.readWorkflow(r.Context(), r.PathValue("site"), r.PathValue("id"), requestAuthorization(r))
	if err != nil {
		writeSiteCallError(w, err)
		return
	}
	writeJSON(w, 200, workflowSummary(workflow))
}

func (s *Server) workflowControlHTTP(w http.ResponseWriter, r *http.Request) {
	if !eventOwner(w, r) {
		return
	}
	control := r.PathValue("control")
	if control != "cancel" && control != "retry" {
		writeError(w, 400, "expected cancel or retry")
		return
	}
	_, err := s.mutateSystem(r.Context(), "workflows", r.PathValue("id"), func(current *Document) (json.RawMessage, error) {
		if current == nil {
			return nil, ErrNotFound
		}
		var workflow Workflow
		if err := json.Unmarshal(current.Data, &workflow); err != nil {
			return nil, err
		}
		if workflow.Site != r.PathValue("site") {
			return nil, ErrNotFound
		}
		if control == "retry" {
			if workflow.Status != "dead" {
				return nil, &ActionError{Message: "only dead workflows can be retried"}
			}
			workflow.Status = "pending"
			workflow.Pending = true
			workflow.Failures = 0
			workflow.NextAttempt = time.Now().UTC()
			workflow.Error = ""
		} else {
			if workflow.Status == "succeeded" {
				return nil, &ActionError{Message: "completed workflow cannot be cancelled"}
			}
			workflow.Status = "cancelled"
			workflow.Pending = false
		}
		workflow.Lease = ""
		workflow.LeaseUntil = time.Time{}
		return json.Marshal(workflow)
	})
	if err != nil {
		writeSiteCallError(w, err)
		return
	}
	writeJSON(w, 202, map[string]bool{"accepted": true})
}

func (s *Server) registerWorkflowRoutes() {
	if !s.eventsEnabled() {
		return
	}
	s.mux.HandleFunc("POST /api/sites/{site}/workflows", s.siteScoped(s.workflowStartHTTP))
	s.mux.HandleFunc("GET /api/sites/{site}/workflows/{id}", s.siteScoped(s.workflowGetHTTP))
	s.mux.HandleFunc("POST /api/sites/{site}/workflows/{id}/{control}", s.siteScoped(s.workflowControlHTTP))
}

func (s *Server) runWorkflowDispatcher(ctx context.Context) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	after := ""
	for {
		next, err := s.dispatchWorkflowPage(ctx, after)
		if err != nil && ctx.Err() == nil {
			slog.Error("dispatch workflows", "error", err)
		}
		after = next
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (s *Server) DispatchWorkflows(ctx context.Context) error {
	_, err := s.dispatchWorkflowPage(ctx, "")
	return err
}

func (s *Server) dispatchWorkflowPage(ctx context.Context, after string) (string, error) {
	database, ok := s.config.Database.(GuardedDatabase)
	if !ok || !s.eventsEnabled() {
		return "", ErrAuthorizationUnsupported
	}
	documents, err := database.ListMatching(ctx, systemSite, "workflows", ListOptions{After: after, Limit: 32}, &RecordCondition{Field: "data.pending", Op: "eq", Value: json.RawMessage(`true`)})
	if err != nil {
		return after, err
	}
	for _, document := range documents {
		if err := s.executeWorkflow(ctx, document.ID); err != nil {
			return after, err
		}
		after = document.ID
	}
	if len(documents) < 32 {
		after = ""
	}
	return after, nil
}

func (s *Server) executeWorkflow(ctx context.Context, id string) error {
	select {
	case s.automationSlots <- struct{}{}:
		defer func() { <-s.automationSlots }()
	default:
		return nil
	}
	lease, err := newID()
	if err != nil {
		return err
	}
	var workflow Workflow
	claimed := false
	_, err = s.mutateSystem(ctx, "workflows", id, func(current *Document) (json.RawMessage, error) {
		if current == nil {
			return nil, ErrNotFound
		}
		if err := json.Unmarshal(current.Data, &workflow); err != nil {
			return nil, err
		}
		now := time.Now().UTC()
		if !workflow.Pending || workflow.NextAttempt.After(now) || workflow.LeaseUntil.After(now) {
			return current.Data, nil
		}
		if workflow.Status == "running" {
			workflow.Failures++
		}
		workflow.Status = "running"
		workflow.Attempts++
		workflow.Lease = lease
		workflow.LeaseUntil = now.Add(automationRunTimeout + time.Minute)
		claimed = true
		return json.Marshal(workflow)
	})
	if err != nil || !claimed {
		return err
	}
	run, executionError := s.runWorkflowAttempt(ctx, workflow)
	finish, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, err = s.mutateSystem(finish, "workflows", id, func(current *Document) (json.RawMessage, error) {
		if current == nil {
			return nil, ErrNotFound
		}
		var latest Workflow
		if err := json.Unmarshal(current.Data, &latest); err != nil {
			return nil, err
		}
		if latest.Lease != lease {
			return current.Data, nil
		}
		latest.Lease = ""
		latest.LeaseUntil = time.Time{}
		latest.RunID = run.ID
		latest.Status = "succeeded"
		latest.Pending = false
		latest.Error = ""
		latest.Output = run.Output
		if !run.WakeAt.IsZero() {
			latest.Status = "waiting"
			latest.Pending = true
			latest.NextAttempt = run.WakeAt
		} else if executionError != nil {
			latest.Failures++
			latest.Status = "pending"
			latest.Pending = true
			latest.Error = boundedScriptText(executionError.Error(), 8192)
			latest.NextAttempt = time.Now().UTC().Add(time.Second * time.Duration(1<<min(latest.Failures, 10)))
			if latest.Failures >= maxDeliveryAttempts || errors.Is(executionError, ErrForbidden) || errors.Is(executionError, ErrNotFound) {
				latest.Status = "dead"
				latest.Pending = false
			}
		}
		return json.Marshal(latest)
	})
	return err
}

func (s *Server) runWorkflowAttempt(ctx context.Context, workflow Workflow) (AutomationRun, error) {
	if workflow.Failures >= maxDeliveryAttempts {
		return AutomationRun{}, errors.New("workflow exhausted recovery attempts")
	}
	definitions, err := s.config.Automations.ListSiteAutomations(ctx, workflow.Site)
	if err != nil {
		return AutomationRun{}, err
	}
	index := slices.IndexFunc(definitions, func(a ScheduledAutomation) bool { return a.Automation.Name == workflow.Definition.Automation.Name })
	if index < 0 {
		return AutomationRun{}, ErrNotFound
	}
	if definitions[index].Automation.Disabled {
		return AutomationRun{}, ErrForbidden
	}
	// Human grants authorize enqueueing, not delegated execution. Work always
	// runs under current automation grants. Automation initiators are locally
	// resolvable and their workflow grant can also be checked on each attempt.
	if workflow.ActorProvider == "automation" {
		authorization, err := s.automationAuthorization(ctx, workflow.Site, strings.TrimPrefix(workflow.Actor, "automation:"))
		if err != nil {
			return AutomationRun{}, err
		}
		if !authorization.permits("workflow", workflow.Definition.Automation.Name, "run") {
			return AutomationRun{}, ErrForbidden
		}
	}
	runID, err := newID()
	if err != nil {
		return AutomationRun{}, err
	}
	run := AutomationRun{ID: runID, Site: workflow.Site, Automation: workflow.Definition.Automation.Name, Trigger: "workflow", Status: RunRunning, StartedAt: time.Now().UTC(), Revision: workflow.Definition.Revision, WorkflowID: workflow.ID, WorkflowLease: workflow.Lease, Input: workflow.Input}
	if err := s.config.Automations.RecordAutomationRun(ctx, run); err != nil {
		return run, err
	}
	s.runAutomation(ctx, workflow.Site, workflow.Definition.Automation, &run)
	if run.Status != RunSucceeded && run.Status != "waiting" {
		return run, errors.New(run.Error)
	}
	return run, nil
}

func (r *automationRunner) workflowCheckpoint(ctx context.Context, name string, update func(*WorkflowStep) error) (WorkflowStep, error) {
	if r.run.WorkflowID == "" {
		return WorkflowStep{}, errors.New("durable steps require a workflow run")
	}
	if !namePattern.MatchString(name) {
		return WorkflowStep{}, errors.New("step names must be valid identifiers")
	}
	var step WorkflowStep
	_, err := r.server.mutateSystem(ctx, "workflows", r.run.WorkflowID, func(current *Document) (json.RawMessage, error) {
		if current == nil {
			return nil, ErrNotFound
		}
		var workflow Workflow
		if err := json.Unmarshal(current.Data, &workflow); err != nil {
			return nil, err
		}
		if workflow.Lease != r.run.WorkflowLease || workflow.Status != "running" || workflow.LeaseUntil.Before(time.Now()) {
			return nil, ErrForbidden
		}
		if len(workflow.Steps) >= 100 {
			if _, ok := workflow.Steps[name]; !ok {
				return nil, errors.New("workflow supports at most 100 steps")
			}
		}
		step = workflow.Steps[name]
		if err := update(&step); err != nil {
			return nil, err
		}
		workflow.Steps[name] = step
		encoded, err := json.Marshal(workflow)
		if len(encoded) > MaxActionInputBytes {
			return nil, errors.New("workflow state exceeds 1 MiB")
		}
		return encoded, err
	})
	return step, err
}

func (r *automationRunner) workflowOperation(ctx context.Context, kind string, input json.RawMessage) (any, error) {
	switch kind {
	case "workflow-start":
		var request workflowStart
		if err := decodeScriptInput(input, &request); err != nil {
			return nil, err
		}
		authorization, err := r.server.automationAuthorization(ctx, r.site, r.automation.Name)
		if err != nil {
			return nil, err
		}
		workflow, err := r.server.startWorkflow(ctx, r.site, authorization, request, r.run.DryRun)
		return map[string]string{"id": workflow.ID}, err
	case "workflow-wait":
		var request struct {
			ID string `json:"id"`
		}
		if err := decodeScriptInput(input, &request); err != nil {
			return nil, err
		}
		if r.run.WorkflowID == "" || request.ID == r.run.WorkflowID {
			return nil, errors.New("wait requires a workflow and a different child ID")
		}
		authorization, err := r.server.automationAuthorization(ctx, r.site, r.automation.Name)
		if err != nil {
			return nil, err
		}
		workflow, err := r.server.readWorkflow(ctx, r.site, request.ID, authorization)
		if err != nil {
			return nil, err
		}
		if workflow.Status == "succeeded" {
			return workflow.Output, nil
		}
		if workflow.Status == "dead" || workflow.Status == "cancelled" {
			return nil, fmt.Errorf("child workflow %s %s: %s", request.ID, workflow.Status, workflow.Error)
		}
		r.run.WakeAt = time.Now().UTC().Add(time.Second)
		return nil, errWorkflowSuspended
	default:
		var request struct {
			Name         string          `json:"name"`
			Output       json.RawMessage `json:"output,omitempty"`
			Milliseconds int64           `json:"milliseconds,omitempty"`
		}
		if err := decodeScriptInput(input, &request); err != nil {
			return nil, err
		}
		if kind == "step-complete" && r.checkpoint != request.Name {
			return nil, errors.New("step completion does not match active step")
		}
		if kind == "step-begin" && r.checkpoint != "" {
			return nil, errors.New("nested or concurrent steps are not supported")
		}
		step, err := r.workflowCheckpoint(ctx, request.Name, func(step *WorkflowStep) error {
			switch kind {
			case "step-begin":
			case "step-complete":
				if len(request.Output) > maxStoredAutomationOutput || !json.Valid(request.Output) {
					return errors.New("checkpoint output must be JSON of at most 16 KiB")
				}
				step.Done = true
				step.Output = request.Output
			case "workflow-sleep":
				if request.Milliseconds < 1 || request.Milliseconds > int64((30*24*time.Hour)/time.Millisecond) {
					return errors.New("durable sleep must be from 1 ms to 30 days")
				}
				if step.WakeAt.IsZero() {
					step.WakeAt = time.Now().UTC().Add(time.Duration(request.Milliseconds) * time.Millisecond)
				}
				step.Done = !step.WakeAt.After(time.Now())
			default:
				return errors.New("unknown workflow operation")
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
		if kind == "step-begin" && !step.Done {
			r.checkpoint = request.Name
			r.checkpointCalls = 0
		}
		if kind == "step-complete" {
			r.checkpoint = ""
		}
		if kind == "workflow-sleep" && !step.Done {
			r.run.WakeAt = step.WakeAt
			return nil, errWorkflowSuspended
		}
		return step, nil
	}
}

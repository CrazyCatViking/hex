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
)

// Declared actions let a published app expose operations without server code.
// The app lists them in the "actions" section of its hex.json; publishing
// stores them with the site, and the server performs each one as a fixed
// operation on the site's own documents with the caller's permissions. App
// code never runs on the server.

// DeclaredAction is one action from an app's hex.json. Operation is "create"
// (insert the input as a new document), "update" (merge the input into an
// existing document) or "delete". Update and delete find the document by the
// input property named IDField, "id" by default, which the input schema must
// require. Audience defaults to the site's editors, like registered actions.
type DeclaredAction struct {
	Name          string                     `json:"name"`
	Description   string                     `json:"description"`
	Operation     string                     `json:"operation"`
	Collection    string                     `json:"collection"`
	IDField       string                     `json:"idField,omitempty"`
	Input         json.RawMessage            `json:"input"`
	Audience      Audience                   `json:"audience,omitzero"`
	Authorization string                     `json:"authorization,omitempty"`
	Fields        []string                   `json:"fields,omitempty"`
	Set           map[string]json.RawMessage `json:"set,omitempty"`
	Where         *RecordCondition           `json:"where,omitempty"`
	Check         *RecordCondition           `json:"check,omitempty"`
}

const (
	OperationCreate = "create"
	OperationUpdate = "update"
	OperationDelete = "delete"

	siteActionsFile          = ".hex-actions.json"
	maxDeclaredActions       = 32
	maxActionDescriptionSize = 500
)

// Every declared action answers with the ID of the document it changed.
// Callers read documents through the data API, which applies read rules.
var declaredActionOutput = json.RawMessage(`{"type":"object","properties":{"id":{"type":"string"}},"required":["id"],"additionalProperties":false}`)

// validateDeclaredActions checks the actions of a publication before any
// file is changed. Names may not shadow actions the server registers for the
// site in Go.
func (s *Server) validateDeclaredActions(site string, actions []DeclaredAction) error {
	if len(actions) > maxDeclaredActions {
		return fmt.Errorf("a site can declare at most %d actions", maxDeclaredActions)
	}
	if len(actions) > 0 && s.config.Database == nil {
		return errors.New("this platform has no database, so it cannot run declared actions")
	}
	names := make(map[string]bool, len(actions))
	for _, action := range actions {
		if _, err := compileDeclaredAction(action); err != nil {
			return err
		}
		if names[action.Name] {
			return fmt.Errorf("action %s is declared more than once", action.Name)
		}
		names[action.Name] = true
		if s.config.Actions.action(site, action.Name) != nil {
			return fmt.Errorf("action %s is provided by the platform for this site; choose another name", action.Name)
		}
	}
	return nil
}

// compileDeclaredAction validates a declared action and compiles its input
// schema. The handler is attached separately because it needs the database.
func compileDeclaredAction(action DeclaredAction) (*registeredAction, error) {
	if !namePattern.MatchString(action.Name) {
		return nil, fmt.Errorf("invalid action name %q; use letters, digits, - and _", action.Name)
	}
	label := "action " + action.Name
	if action.Authorization != "" && action.Authorization != "caller" && action.Authorization != "action" {
		return nil, fmt.Errorf("%s: authorization must be caller or action", label)
	}
	if action.Authorization != "action" && (action.Set != nil || action.Fields != nil || action.Where != nil || action.Check != nil) {
		return nil, fmt.Errorf("%s: fixed effects and record constraints require action authorization", label)
	}
	if len(action.Fields) > 64 || len(action.Set) > 64 {
		return nil, fmt.Errorf("%s: at most 64 input fields and fixed effects are allowed", label)
	}
	for _, field := range action.Fields {
		if !validCollectionName(field) {
			return nil, fmt.Errorf("%s: invalid input field %q", label, field)
		}
	}
	for field := range action.Set {
		if !validCollectionName(field) {
			return nil, fmt.Errorf("%s: invalid fixed-effect field %q", label, field)
		}
	}
	for _, condition := range []*RecordCondition{action.Where, action.Check} {
		if err := validateRecordCondition(condition); err != nil {
			return nil, fmt.Errorf("%s: %w", label, err)
		}
	}
	description := strings.TrimSpace(action.Description)
	if description == "" || utf8.RuneCountInString(description) > maxActionDescriptionSize {
		return nil, fmt.Errorf("%s needs a description of at most %d characters", label, maxActionDescriptionSize)
	}
	switch action.Operation {
	case OperationCreate, OperationUpdate, OperationDelete:
	default:
		return nil, fmt.Errorf("%s: operation must be create, update or delete", label)
	}
	if !namePattern.MatchString(action.Collection) {
		return nil, fmt.Errorf("%s needs a valid collection", label)
	}
	if action.IDField != "" && action.Operation == OperationCreate {
		return nil, fmt.Errorf("%s: idField applies to update and delete; create always makes a new document", label)
	}
	if err := validateAudience(label, action.Audience, false); err != nil {
		return nil, err
	}

	input, err := compileActionSchema(action.Input)
	if err != nil {
		return nil, fmt.Errorf("%s input schema: %w", label, err)
	}
	var shape struct {
		Type     string   `json:"type"`
		Required []string `json:"required"`
	}
	if err := json.Unmarshal(action.Input, &shape); err != nil || shape.Type != "object" {
		return nil, fmt.Errorf(`%s: the input schema must have "type": "object"`, label)
	}
	if action.Operation != OperationCreate && !slices.Contains(shape.Required, action.idField()) {
		return nil, fmt.Errorf("%s: the input schema must require %q, the ID of the document to %s", label, action.idField(), action.Operation)
	}
	output, err := compileActionSchema(declaredActionOutput)
	if err != nil {
		return nil, err
	}

	return &registeredAction{
		definition: ActionDefinition{
			Name: action.Name, Description: description,
			InputSchema: action.Input, OutputSchema: declaredActionOutput,
		},
		audience: cloneAudience(action.Audience),
		input:    input,
		output:   output,
		effect:   action.effect(),
	}, nil
}

func (action DeclaredAction) effect() string {
	switch action.Operation {
	case OperationCreate:
		return "Adds a record to " + action.Collection
	case OperationUpdate:
		return "Changes a record in " + action.Collection
	default:
		return "Removes a record from " + action.Collection
	}
}

func (action DeclaredAction) idField() string {
	if action.IDField == "" {
		return "id"
	}
	return action.IDField
}

// writeDeclaredActions records a publication's actions; a publication
// without actions removes earlier ones.
func (s *Server) writeDeclaredActions(ctx context.Context, site string, actions []DeclaredAction) error {
	if len(actions) == 0 {
		if err := s.config.Publisher.DeleteSiteFile(ctx, site, siteActionsFile); err != nil && !errors.Is(err, ErrNotFound) {
			return fmt.Errorf("remove actions of %s: %w", site, err)
		}
		return nil
	}
	encoded, err := json.Marshal(actions)
	if err != nil {
		return err
	}
	return s.writeRecord(ctx, site, siteActionsFile, encoded)
}

// declaredActions returns the site's published actions with their handlers.
// Entries that no longer validate are skipped, so one bad record cannot
// break the site's other actions.
func (s *Server) declaredActions(ctx context.Context, site string) ([]*registeredAction, error) {
	if s.config.Publisher == nil || s.config.Database == nil {
		return nil, nil
	}
	var declared []DeclaredAction
	if err := s.readRecord(ctx, site, siteActionsFile, &declared); err != nil {
		return nil, fmt.Errorf("read actions of %s: %w", site, err)
	}
	actions := make([]*registeredAction, 0, len(declared))
	for _, action := range declared {
		compiled, err := compileDeclaredAction(action)
		if err != nil {
			slog.Warn("skipping invalid declared action", "site", site, "action", action.Name, "error", err)
			continue
		}
		compiled.handler = s.declaredHandler(action)
		actions = append(actions, compiled)
	}
	return actions, nil
}

// declaredHandler performs the action's operation. The collection's write
// rule applies exactly as for the data API, including creator-only rules.
func (s *Server) declaredHandler(action DeclaredAction) func(context.Context, ActionContext, json.RawMessage) (any, error) {
	return func(ctx context.Context, caller ActionContext, input json.RawMessage) (any, error) {
		return s.executeDeclaredMutation(ctx, caller, action, input)
	}
}

func (s *Server) executeDeclaredMutation(ctx context.Context, caller ActionContext, action DeclaredAction, input json.RawMessage) (any, error) {
	if !caller.authorization.permits("action", action.Name, "run") {
		return nil, ErrForbidden
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(input, &fields); err != nil || fields == nil {
		return nil, &ActionError{Message: "the input must be a JSON object"}
	}

	var id string
	if action.Operation == OperationCreate {
		generated, err := newID()
		if err != nil {
			return nil, err
		}
		id = generated
	} else if err := json.Unmarshal(fields[action.idField()], &id); err != nil || !namePattern.MatchString(id) {
		return nil, &ActionError{Message: fmt.Sprintf("%q must be a document ID", action.idField())}
	}
	database, supported := s.config.Database.(GuardedDatabase)
	if !supported {
		return nil, ErrAuthorizationUnsupported
	}
	_, err := database.Mutate(ctx, caller.Site, action.Collection, id, caller.authorization.creator(), func(before *Document) (json.RawMessage, error) {
		if action.Operation != OperationCreate && before == nil {
			return nil, &ActionError{Message: "no document " + id + " in " + action.Collection}
		}
		if action.Operation == OperationCreate && before != nil {
			return nil, ErrForbidden
		}
		data := map[string]json.RawMessage{}
		after := Document{ID: id, CreatedBy: caller.authorization.creator()}
		if before != nil {
			after = *before
			if err := json.Unmarshal(before.Data, &data); err != nil {
				return nil, err
			}
		}
		for name, value := range fields {
			if action.Operation != OperationCreate && name == action.idField() {
				continue
			}
			if action.Authorization == "action" && !slices.Contains(action.Fields, name) {
				return nil, &ActionError{Message: "the action does not accept changes to " + name}
			}
			data[name] = value
		}
		for field, value := range action.Set {
			switch conditionString(value) {
			case "$actor.id":
				value, _ = json.Marshal(caller.authorization.creator())
			case "$now":
				value, _ = json.Marshal(time.Now().UTC().Format(time.RFC3339Nano))
			}
			data[field] = value
		}
		encoded, err := json.Marshal(data)
		if err != nil {
			return nil, err
		}
		after.Data = encoded
		if !caller.authorization.canChangeRecord("action", action.Name, "run", before, after) {
			return nil, ErrForbidden
		}
		if action.Authorization == "action" {
			old := after
			if before != nil {
				old = *before
			}
			if !resolveRecordCondition(action.Where, caller.Identity).Matches(old) || !resolveRecordCondition(action.Check, caller.Identity).Matches(after) {
				return nil, ErrForbidden
			}
		} else if !caller.authorization.canMutate(action.Collection, action.Operation, before, after) {
			return nil, ErrForbidden
		}
		if action.Operation == OperationDelete {
			return nil, nil
		}
		if err := ValidateDocumentData(encoded); err != nil {
			return nil, &ActionError{Message: err.Error()}
		}
		return encoded, nil
	})
	if err != nil {
		return nil, err
	}
	return map[string]string{"id": id}, nil
}

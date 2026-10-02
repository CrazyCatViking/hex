package hex

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
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
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Operation   string          `json:"operation"`
	Collection  string          `json:"collection"`
	IDField     string          `json:"idField,omitempty"`
	Input       json.RawMessage `json:"input"`
	Audience    Audience        `json:"audience,omitzero"`
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
		options, err := caller.CollectionWriteOptions(action.Collection)
		if err != nil {
			return nil, ErrForbidden
		}
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(input, &fields); err != nil || fields == nil {
			return nil, &ActionError{Message: "the input must be a JSON object"}
		}

		if action.Operation == OperationCreate {
			id, err := newID()
			if err != nil {
				return nil, err
			}
			document, err := s.config.Database.Put(ctx, caller.Site, action.Collection, id, input, options)
			if err != nil {
				return nil, err
			}
			return map[string]string{"id": document.ID}, nil
		}

		var id string
		if err := json.Unmarshal(fields[action.idField()], &id); err != nil || !namePattern.MatchString(id) {
			return nil, &ActionError{Message: fmt.Sprintf("%q must be a document ID", action.idField())}
		}
		missing := &ActionError{Message: fmt.Sprintf("no document %s in %s", id, action.Collection)}

		if action.Operation == OperationDelete {
			err := s.config.Database.Delete(ctx, caller.Site, action.Collection, id, options)
			if errors.Is(err, ErrNotFound) {
				return nil, missing
			}
			if err != nil {
				return nil, err
			}
			return map[string]string{"id": id}, nil
		}

		// Update merges the input's top-level fields, except the ID, into
		// the existing document; it never creates one.
		existing, err := s.config.Database.Get(ctx, caller.Site, action.Collection, id)
		if errors.Is(err, ErrNotFound) {
			return nil, missing
		}
		if err != nil {
			return nil, err
		}
		var data map[string]json.RawMessage
		if err := json.Unmarshal(existing.Data, &data); err != nil || data == nil {
			data = make(map[string]json.RawMessage)
		}
		for name, value := range fields {
			if name != action.idField() {
				data[name] = value
			}
		}
		merged, err := json.Marshal(data)
		if err != nil {
			return nil, err
		}
		if len(merged) > MaxActionInputBytes {
			return nil, &ActionError{Message: "the updated document would exceed 1 MiB"}
		}
		if _, err := s.config.Database.Put(ctx, caller.Site, action.Collection, id, merged, options); err != nil {
			return nil, err
		}
		return map[string]string{"id": id}, nil
	}
}

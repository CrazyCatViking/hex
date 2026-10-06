package hex

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"sync"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

// Action registers an operation and its contract together. Audience uses the
// site's existing viewers/editors/owners or principals; it defaults to editors.
// The handler applies business rules and returns a JSON-marshalable result.
type Action struct {
	Definition ActionDefinition
	Audience   Audience
	Handler    func(context.Context, ActionContext, json.RawMessage) (any, error)
}

// ActionContext carries the existing caller and site authorization, not a
// separate agent identity. Its helpers apply the site's data rules when a
// handler uses providers directly.
type ActionContext struct {
	Site          string
	Identity      *Identity
	Role          string
	authorization siteAuthorization
}

func (c ActionContext) CollectionWriteOptions(collection string) (WriteOptions, error) {
	if !namePattern.MatchString(collection) {
		return WriteOptions{}, errors.New("invalid collection")
	}
	return c.authorization.collectionWriteOptions(collection)
}

func (c ActionContext) CanReadDocument(collection string, document Document) bool {
	if !namePattern.MatchString(collection) {
		return false
	}
	switch c.authorization.collection(collection).read {
	case grantAll:
		return true
	case grantOwn:
		return document.CreatedBy == c.authorization.creator()
	default:
		return false
	}
}

func (c ActionContext) CanReadFile(key string) bool {
	return validKey(key) && c.authorization.file(key).read == grantAll
}

func (c ActionContext) CanWriteFile(key string) bool {
	return validKey(key) && c.authorization.file(key).write == grantAll
}

// ActionError reports a user-correctable business-rule failure as HTTP 400.
// Other handler errors remain internal, except ErrForbidden and ErrNotFound.
type ActionError struct{ Message string }

func (e *ActionError) Error() string { return e.Message }

type registeredAction struct {
	definition ActionDefinition
	audience   Audience
	handler    func(context.Context, ActionContext, json.RawMessage) (any, error)
	input      *jsonschema.Schema
	output     *jsonschema.Schema
	// effect describes a declared action's operation in plain words for
	// the portal; registered actions leave it empty.
	effect string
}

type actionInputError struct {
	err error
}

func (e *actionInputError) Error() string { return e.err.Error() }
func (e *actionInputError) Unwrap() error { return e.err }

// A result failure is an internal contract error, even when a custom JSON
// marshaler returns an ActionError or an authorization error.
type actionResultError struct {
	name string
	err  error
}

func (e *actionResultError) Error() string {
	return fmt.Sprintf("action %s returned an invalid result: %v", e.name, e.err)
}

func (a *registeredAction) validateInput(input json.RawMessage) error {
	if len(input) > MaxActionInputBytes {
		return &actionInputError{err: errors.New("action input exceeds 1 MiB")}
	}
	if err := validateActionJSON(a.input, input); err != nil {
		return &actionInputError{err: err}
	}
	return nil
}

// execute applies the action's contract independently of its transport. The
// caller supplies its existing authorization context; handler errors retain
// their identity so each transport can map them appropriately.
func (a *registeredAction) execute(ctx context.Context, caller ActionContext, input json.RawMessage) (json.RawMessage, error) {
	if err := a.validateInput(input); err != nil {
		return nil, err
	}
	result, err := a.handler(ctx, caller, input)
	if err != nil {
		return nil, err
	}
	output, err := json.Marshal(result)
	if err == nil {
		err = validateActionJSON(a.output, output)
	}
	if err != nil {
		return nil, &actionResultError{name: a.definition.Name, err: err}
	}
	return json.RawMessage(output), nil
}

// ActionRegistry stores per-site operations. Register validates and compiles
// contracts before exposing them; registered definitions cannot be replaced.
// Its zero value is ready to use and registration is safe alongside requests.
type ActionRegistry struct {
	mu    sync.RWMutex
	sites map[string]map[string]*registeredAction
}

func (r *ActionRegistry) Register(site string, action Action) error {
	if !siteNamePattern.MatchString(site) {
		return errors.New("invalid action site name")
	}
	definition := action.Definition
	if !namePattern.MatchString(definition.Name) || strings.TrimSpace(definition.Description) == "" || action.Handler == nil {
		return errors.New("an action requires a valid name, description and handler")
	}
	if err := validateAudience("action", action.Audience, false); err != nil {
		return err
	}
	input, err := compileActionSchema(definition.InputSchema)
	if err != nil {
		return fmt.Errorf("action %s input schema: %w", definition.Name, err)
	}
	output, err := compileActionSchema(definition.OutputSchema)
	if err != nil {
		return fmt.Errorf("action %s output schema: %w", definition.Name, err)
	}
	definition.InputSchema = slices.Clone(definition.InputSchema)
	definition.OutputSchema = slices.Clone(definition.OutputSchema)
	entry := &registeredAction{
		definition: definition, audience: cloneAudience(action.Audience),
		handler: action.Handler, input: input, output: output,
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.sites == nil {
		r.sites = make(map[string]map[string]*registeredAction)
	}
	if r.sites[site] == nil {
		r.sites[site] = make(map[string]*registeredAction)
	}
	if _, exists := r.sites[site][definition.Name]; exists {
		return fmt.Errorf("action %s is already registered for %s", definition.Name, site)
	}
	r.sites[site][definition.Name] = entry
	return nil
}

func (r *ActionRegistry) action(site, name string) *registeredAction {
	if r == nil {
		return nil
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.sites[site][name]
}

func (r *ActionRegistry) actions(site string) []*registeredAction {
	if r == nil {
		return nil
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	result := make([]*registeredAction, 0, len(r.sites[site]))
	for _, action := range r.sites[site] {
		result = append(result, action)
	}
	slices.SortFunc(result, func(a, b *registeredAction) int { return strings.Compare(a.definition.Name, b.definition.Name) })
	return result
}

func actionAllowed(action *registeredAction, authorization siteAuthorization) bool {
	return audienceGrant(authorization.role, authorization.identity, action.audience, LevelEditors) == grantAll
}

// actionsEnabled reports whether sites can have actions: registered in Go
// by the platform, or declared in a published app's hex.json.
func (s *Server) actionsEnabled() bool {
	return s.config.Actions != nil || (s.config.Database != nil && s.config.Publisher != nil)
}

// siteActions merges the platform's registered actions with the site's
// declared ones, sorted by name. Publishing rejects declared names that
// shadow registered ones; registered actions win if both exist anyway.
func (s *Server) siteActions(ctx context.Context, site string) ([]*registeredAction, error) {
	registered := s.config.Actions.actions(site)
	declared, err := s.declaredActions(ctx, site)
	if err != nil {
		return nil, err
	}
	for _, action := range declared {
		if s.config.Actions.action(site, action.definition.Name) == nil {
			registered = append(registered, action)
		}
	}
	slices.SortFunc(registered, func(a, b *registeredAction) int { return strings.Compare(a.definition.Name, b.definition.Name) })
	return registered, nil
}

func (s *Server) listActions(w http.ResponseWriter, r *http.Request) {
	authorization := requestAuthorization(r)
	actions, err := s.siteActions(r.Context(), r.PathValue("site"))
	if err != nil {
		writeServerError(w, err)
		return
	}
	definitions := make([]ActionDefinition, 0)
	for _, action := range actions {
		if actionAllowed(action, authorization) {
			definitions = append(definitions, action.definition)
		}
	}
	writeJSON(w, http.StatusOK, definitions)
}

func (s *Server) requestAction(w http.ResponseWriter, r *http.Request) *registeredAction {
	name := r.PathValue("action")
	if !namePattern.MatchString(name) {
		writeError(w, http.StatusBadRequest, "invalid action name")
		return nil
	}
	actions, err := s.siteActions(r.Context(), r.PathValue("site"))
	if err != nil {
		writeServerError(w, err)
		return nil
	}
	index := slices.IndexFunc(actions, func(action *registeredAction) bool { return action.definition.Name == name })
	if index < 0 {
		writeError(w, http.StatusNotFound, "action not found")
		return nil
	}
	action := actions[index]
	if !actionAllowed(action, requestAuthorization(r)) {
		writeError(w, http.StatusForbidden, "performing this action is restricted")
		return nil
	}
	return action
}

func (s *Server) describeAction(w http.ResponseWriter, r *http.Request) {
	action := s.requestAction(w, r)
	if action != nil {
		writeJSON(w, http.StatusOK, action.definition)
	}
}

func (s *Server) runAction(w http.ResponseWriter, r *http.Request) {
	action := s.requestAction(w, r)
	if action == nil {
		return
	}
	input, err := io.ReadAll(http.MaxBytesReader(w, r.Body, MaxActionInputBytes))
	if err != nil {
		writeError(w, http.StatusBadRequest, "expected action input of at most 1 MiB")
		return
	}
	authorization := requestAuthorization(r)
	caller := ActionContext{
		Site: r.PathValue("site"), Identity: authorization.identity,
		Role: roleNames[authorization.role], authorization: authorization,
	}
	output, err := action.execute(r.Context(), caller, input)
	if err != nil {
		var inputError *actionInputError
		var actionError *ActionError
		switch {
		case errors.As(err, &inputError):
			writeError(w, http.StatusBadRequest, "invalid action input: "+inputError.Error())
		case errors.Is(err, ErrForbidden):
			writeError(w, http.StatusForbidden, "performing this action is restricted")
		case errors.As(err, &actionError):
			writeError(w, http.StatusBadRequest, actionError.Message)
		default:
			writeServerError(w, err)
		}
		return
	}
	writeJSON(w, http.StatusOK, output)
}

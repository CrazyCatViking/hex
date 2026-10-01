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
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.sites[site][name]
}

func (r *ActionRegistry) actions(site string) []*registeredAction {
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

func (s *Server) listActions(w http.ResponseWriter, r *http.Request) {
	authorization := requestAuthorization(r)
	definitions := make([]ActionDefinition, 0)
	for _, action := range s.config.Actions.actions(r.PathValue("site")) {
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
	action := s.config.Actions.action(r.PathValue("site"), name)
	if action == nil {
		writeError(w, http.StatusNotFound, "action not found")
		return nil
	}
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
	if err := validateActionJSON(action.input, input); err != nil {
		writeError(w, http.StatusBadRequest, "invalid action input: "+err.Error())
		return
	}
	authorization := requestAuthorization(r)
	caller := ActionContext{
		Site: r.PathValue("site"), Identity: authorization.identity,
		Role: roleNames[authorization.role], authorization: authorization,
	}
	result, err := action.handler(r.Context(), caller, input)
	if err != nil {
		var actionError *ActionError
		switch {
		case errors.Is(err, ErrForbidden):
			writeError(w, http.StatusForbidden, "performing this action is restricted")
		case errors.As(err, &actionError):
			writeError(w, http.StatusBadRequest, actionError.Message)
		default:
			writeServerError(w, err)
		}
		return
	}
	output, err := json.Marshal(result)
	if err == nil {
		err = validateActionJSON(action.output, output)
	}
	if err != nil {
		writeServerError(w, fmt.Errorf("action %s returned an invalid result: %w", action.definition.Name, err))
		return
	}
	writeJSON(w, http.StatusOK, json.RawMessage(output))
}

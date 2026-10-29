package hex

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"time"
)

// Internal namespaces are deliberately invalid public site/collection names.
// GuardedDatabase makes these records durable and serializes all replicas.
const systemSite = ".hex-system"

func (s *Server) siteAPIEnabled() bool {
	_, ok := s.config.Database.(GuardedDatabase)
	return ok && s.actionsEnabled()
}

var errInvocationConflict = errors.New("invocation already started or key reused with different input; inspect its status before retrying")

func internalKey(parts ...string) string {
	encoded, _ := json.Marshal(parts)
	return fmt.Sprintf("%x", sha256.Sum256(encoded))
}

func (s *Server) mutateSystem(ctx context.Context, collection, id string, fn DocumentMutation) (Document, error) {
	database, ok := s.config.Database.(GuardedDatabase)
	if !ok {
		return Document{}, ErrAuthorizationUnsupported
	}
	return database.Mutate(ctx, systemSite, collection, id, "", fn)
}

func (s *Server) sitePrincipal(ctx context.Context, site string) (*Identity, error) {
	if !siteNamePattern.MatchString(site) {
		return nil, ErrForbidden
	}
	// Site ownership survives unpublishing; so does this identity. A namespace
	// must not be reassigned without deleting its grants and rotating this ID.
	document, err := s.mutateSystem(ctx, "identities", site, func(current *Document) (json.RawMessage, error) {
		if current != nil {
			return current.Data, nil
		}
		id, err := newID()
		if err != nil {
			return nil, err
		}
		return json.Marshal(Identity{ID: "site:" + id, Provider: "site", Name: site})
	})
	if err != nil {
		return nil, err
	}
	var identity Identity
	if err := json.Unmarshal(document.Data, &identity); err != nil {
		return nil, err
	}
	return &identity, nil
}

type SiteCall struct {
	Target         string          `json:"target"`
	Action         string          `json:"action"`
	Version        int             `json:"version"`
	Input          json.RawMessage `json:"input"`
	IdempotencyKey string          `json:"idempotencyKey,omitempty"`
}

type siteInvocation struct {
	ID          string          `json:"id"`
	Source      string          `json:"source"`
	Target      string          `json:"target"`
	Action      string          `json:"action"`
	Version     int             `json:"version"`
	Actor       string          `json:"actor"`
	Fingerprint string          `json:"fingerprint"`
	Status      string          `json:"status"`
	StartedAt   time.Time       `json:"startedAt"`
	Output      json.RawMessage `json:"output,omitempty"`
}

func (s *Server) prepareSiteCall(ctx context.Context, source string, sourceAuth siteAuthorization, request SiteCall, asSite bool) (*registeredAction, ActionContext, error) {
	if !siteNamePattern.MatchString(request.Target) || !namePattern.MatchString(request.Action) || request.Version < 1 {
		return nil, ActionContext{}, &ActionError{Message: "target, action and positive version are required"}
	}
	if !sourceAuth.permits("site-action", request.Target+"."+request.Action, "run") {
		return nil, ActionContext{}, ErrForbidden
	}
	principal, err := s.sitePrincipal(ctx, source)
	if err != nil {
		return nil, ActionContext{}, err
	}
	targetAccess, exists, err := s.sitePolicy(ctx, request.Target)
	if err != nil {
		return nil, ActionContext{}, err
	}
	if !exists {
		return nil, ActionContext{}, ErrForbidden
	}
	connection := siteAuthorization{identity: principal, access: targetAccess, role: roleViewer, apiOnly: true}
	if !connection.permits("action-consumer", request.Action, "run") {
		return nil, ActionContext{}, ErrForbidden
	}
	actor := sourceAuth.identity
	if asSite {
		actor = principal
	}
	if actor == nil {
		return nil, ActionContext{}, ErrForbidden
	}
	authorization := siteAuthorization{identity: actor, access: targetAccess, role: s.siteRole(actor, targetAccess, true), apiOnly: true}
	// API access is independent of page admission, including for site actors.
	if authorization.role == roleNone || asSite {
		authorization.role = roleViewer
	}
	actions, err := s.siteActions(ctx, request.Target)
	if err != nil {
		return nil, ActionContext{}, err
	}
	index := slices.IndexFunc(actions, func(a *registeredAction) bool { return a.definition.Name == request.Action })
	if index < 0 {
		return nil, ActionContext{}, ErrNotFound
	}
	action := actions[index]
	if !action.definition.Exported || action.definition.Version != request.Version || !actionAllowed(action, authorization) {
		return nil, ActionContext{}, ErrForbidden
	}
	return action, ActionContext{Site: request.Target, SourceSite: source, Identity: actor, Role: roleNames[authorization.role], authorization: authorization}, nil
}

func (s *Server) invokeSiteAction(ctx context.Context, source string, authorization siteAuthorization, request SiteCall, asSite, dryRun bool) (json.RawMessage, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	action, caller, err := s.prepareSiteCall(ctx, source, authorization, request, asSite)
	if err != nil {
		return nil, err
	}
	if len(request.Input) == 0 {
		request.Input = json.RawMessage(`{}`)
	}
	if err := action.validateInput(request.Input); err != nil {
		return nil, err
	}
	if dryRun && !action.definition.ReadOnly {
		return json.Marshal(map[string]any{"wouldRun": request.Action, "target": request.Target})
	}
	if action.definition.ReadOnly {
		return action.execute(ctx, caller, request.Input)
	}
	if len(request.IdempotencyKey) < 1 || len(request.IdempotencyKey) > 128 {
		return nil, &ActionError{Message: "mutations require an idempotencyKey of 1–128 bytes"}
	}
	key := internalKey(source, caller.Identity.ID, request.Target, request.Action, fmt.Sprint(request.Version), request.IdempotencyKey)
	fingerprint := internalKey(string(request.Input))
	invocation := siteInvocation{ID: key, Source: source, Target: request.Target, Action: request.Action, Version: request.Version, Actor: caller.Identity.ID, Fingerprint: fingerprint, Status: "running", StartedAt: time.Now().UTC()}
	created := false
	_, err = s.mutateSystem(ctx, "invocations", key, func(current *Document) (json.RawMessage, error) {
		if current != nil {
			if err := json.Unmarshal(current.Data, &invocation); err != nil {
				return nil, err
			}
			if invocation.Fingerprint != fingerprint || invocation.Status != "succeeded" {
				return nil, errInvocationConflict
			}
			return current.Data, nil
		}
		created = true
		return json.Marshal(invocation)
	})
	if err != nil {
		return nil, err
	}
	if !created {
		return invocation.Output, nil
	}
	caller.InvocationID = key
	output, executionError := action.execute(ctx, caller, request.Input)
	invocation.Status = "uncertain"
	if executionError == nil {
		invocation.Status, invocation.Output = "succeeded", output
	}
	// A handler can commit before failing or losing its result. Never retry an
	// uncertain mutation automatically, including after a process restart.
	cleanup, finish := context.WithTimeout(context.Background(), 10*time.Second)
	defer finish()
	_, storeError := s.mutateSystem(cleanup, "invocations", key, func(*Document) (json.RawMessage, error) { return json.Marshal(invocation) })
	slog.Info("site action", "invocation", key, "source", source, "target", request.Target, "actor", caller.Identity.ID, "status", invocation.Status)
	if storeError != nil {
		return nil, storeError
	}
	return output, executionError
}

func writeSiteCallError(w http.ResponseWriter, err error) {
	var input *actionInputError
	var business *ActionError
	switch {
	case errors.Is(err, ErrForbidden):
		writeError(w, http.StatusForbidden, "site connection or action permission denied")
	case errors.Is(err, ErrNotFound):
		writeError(w, http.StatusNotFound, "action or record not found")
	case errors.Is(err, errInvocationConflict):
		writeError(w, http.StatusConflict, err.Error())
	case errors.As(err, &input), errors.As(err, &business):
		writeError(w, http.StatusBadRequest, err.Error())
	default:
		writeServerError(w, err)
	}
}

func (s *Server) siteCallHTTP(w http.ResponseWriter, r *http.Request) {
	var request SiteCall
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, MaxActionInputBytes+4096))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		writeError(w, 400, "invalid site call")
		return
	}
	output, err := s.invokeSiteAction(r.Context(), r.PathValue("site"), requestAuthorization(r), request, false, false)
	if err != nil {
		writeSiteCallError(w, err)
		return
	}
	writeJSON(w, 200, output)
}

func (s *Server) siteIdentityHTTP(w http.ResponseWriter, r *http.Request) {
	identity, err := s.sitePrincipal(r.Context(), r.PathValue("site"))
	if err != nil {
		writeServerError(w, err)
		return
	}
	writeJSON(w, 200, identity)
}

func (s *Server) siteActionsHTTP(w http.ResponseWriter, r *http.Request) {
	target := r.PathValue("target")
	if !siteNamePattern.MatchString(target) {
		writeError(w, 400, "invalid target")
		return
	}
	actions, err := s.siteActions(r.Context(), target)
	if err != nil {
		writeServerError(w, err)
		return
	}
	definitions := []ActionDefinition{}
	for _, action := range actions {
		_, _, err := s.prepareSiteCall(r.Context(), r.PathValue("site"), requestAuthorization(r), SiteCall{Target: target, Action: action.definition.Name, Version: action.definition.Version}, false)
		if errors.Is(err, ErrForbidden) || errors.Is(err, ErrNotFound) {
			continue
		}
		if err != nil {
			writeSiteCallError(w, err)
			return
		}
		definitions = append(definitions, action.definition)
	}
	writeJSON(w, 200, definitions)
}

func (s *Server) siteInvocationHTTP(w http.ResponseWriter, r *http.Request) {
	document, err := s.config.Database.Get(r.Context(), systemSite, "invocations", r.PathValue("id"))
	if err != nil {
		writeSiteCallError(w, err)
		return
	}
	var invocation siteInvocation
	if err := json.Unmarshal(document.Data, &invocation); err != nil {
		writeServerError(w, err)
		return
	}
	authorization := requestAuthorization(r)
	if invocation.Source != r.PathValue("site") || authorization.identity == nil || (invocation.Actor != authorization.identity.ID && authorization.role != roleOwner) {
		writeSiteCallError(w, ErrForbidden)
		return
	}
	if !authorization.permits("site-action", invocation.Target+"."+invocation.Action, "run") {
		writeSiteCallError(w, ErrForbidden)
		return
	}
	writeJSON(w, 200, invocation)
}

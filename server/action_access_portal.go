package hex

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"reflect"
	"slices"
)

type actionConnectionEdit struct {
	Source     string           `json:"source"`
	User       bool             `json:"user"`
	Site       bool             `json:"site"`
	Subscribe  bool             `json:"subscribe,omitempty"`
	Where      *RecordCondition `json:"where,omitempty"`
	Check      *RecordCondition `json:"check,omitempty"`
	Fields     []string         `json:"fields,omitzero"`
	KeepLimits bool             `json:"keepLimits,omitempty"`
	KeepWhere  bool             `json:"keepWhere,omitempty"`
	KeepCheck  bool             `json:"keepCheck,omitempty"`
	KeepFields bool             `json:"keepFields,omitempty"`
}

func connectionGroupID(action, source string) string {
	return "app-" + internalKey(action, source)[:32]
}

// Only the two managed grants and their group are edited. Unknown grants and
// publication baselines are never reconstructed from this simplified UI.
func editActionConnection(access SiteAccess, action string, edit actionConnectionEdit, principal string) (SiteAccess, error) {
	return editResourceConnection(access, "action-consumer", action, edit, principal)
}

func resourceConnectionID(kind, resource, source string) string {
	if kind == "event" {
		resource = "event:" + resource
	}
	return connectionGroupID(resource, source)
}

func editResourceConnection(access SiteAccess, kind, resource string, edit actionConnectionEdit, principal string) (SiteAccess, error) {
	local := CloneSiteAccess(access)
	id := resourceConnectionID(kind, resource, edit.Source)
	modes := []string{"user", "site"}
	if kind == "event" {
		modes = []string{"subscribe"}
	}
	if local.Roles == nil {
		local.Roles = map[string]ApplicationRole{}
	}
	if local.Grants == nil {
		local.Grants = map[string]PermissionGrant{}
	}
	if local.Bindings == nil {
		local.Bindings = map[string]RoleBinding{}
	}
	role, exists := local.Roles[id]
	if exists && (role.SignedIn || role.Owners || role.Workspace != (edit.Source == "*")) {
		return access, errors.New("this app permission group has custom audiences; edit it in Sharing")
	}
	for _, mode := range modes {
		grant, exists := local.Grants[id+"-"+mode]
		if exists && (grant.Kind != kind || grant.Resource != resource || !slices.Equal(grant.Operations, []string{mode}) || !reflect.DeepEqual(grant.Subjects, Subjects{Roles: []string{id}}) || len(grant.Except) > 0 || grant.Scope != nil) {
			return access, errors.New("this connection has custom rules; edit its permission group in Sharing")
		}
	}
	if !exists {
		name := edit.Source
		if name == "*" {
			name = "All workspace apps"
		}
		role = ApplicationRole{Name: name + " · " + resource, Description: "App connection permissions", Workspace: edit.Source == "*"}
	}
	local.Roles[id] = role
	if edit.Source != "*" {
		binding := RoleBinding{Principal: principal, Role: id}
		if previous, exists := local.Bindings[id]; exists && !reflect.DeepEqual(previous, binding) {
			return access, errors.New("the connection membership was customized; edit it in Sharing")
		}
		local.Bindings[id] = binding
	}
	for _, mode := range modes {
		if mode == "user" && !edit.User || mode == "site" && !edit.Site || mode == "subscribe" && !edit.Subscribe {
			delete(local.Grants, id+"-"+mode)
			continue
		}
		grant := PermissionGrant{Kind: kind, Resource: resource, Operations: []string{mode}, Subjects: Subjects{Roles: []string{id}}}
		if mode == "site" {
			grant.Where, grant.Check, grant.Fields = edit.Where, edit.Check, slices.Clone(edit.Fields)
			if edit.KeepLimits {
				previous := access.Grants[id+"-site"]
				grant.Where, grant.Check, grant.Fields = previous.Where, previous.Check, previous.Fields
			}
			previous := access.Grants[id+"-site"]
			if edit.KeepWhere {
				grant.Where = previous.Where
			}
			if edit.KeepCheck {
				grant.Check = previous.Check
			}
			if edit.KeepFields {
				grant.Fields = previous.Fields
			}
		}
		local.Grants[id+"-"+mode] = grant
	}
	return local, ValidatePublishedAccess(local)
}

func (s *Server) actionConnectionSources(ctx context.Context) ([]string, error) {
	sites, err := s.config.Sites.ListSites(ctx)
	if err != nil {
		return nil, err
	}
	slices.Sort(sites)
	result := []string{"*"}
	for _, site := range slices.Compact(sites) {
		if siteNamePattern.MatchString(site) {
			result = append(result, site)
		}
	}
	return result, nil
}

func (s *Server) manageActionAccess(w http.ResponseWriter, r *http.Request) {
	identity, site, access, _, ok := s.manageCaller(w, r)
	if !ok {
		return
	}
	kind, resource, siteMode := "action-consumer", r.PathValue("action"), false
	if event := r.PathValue("event"); event != "" {
		kind, resource = "event", event
		rows, err := s.workspaceShareRows(r.Context(), site, access)
		if err != nil {
			writeServerError(w, err)
			return
		}
		if !slices.ContainsFunc(rows, func(row workspaceShareRow) bool { return row.Kind == "event" && row.Name == event }) {
			http.NotFound(w, r)
			return
		}
	} else {
		actions, err := s.siteActions(r.Context(), site)
		if err != nil {
			writeServerError(w, err)
			return
		}
		index := slices.IndexFunc(actions, func(action *registeredAction) bool { return action.definition.Name == resource })
		if index < 0 {
			http.NotFound(w, r)
			return
		}
		action := actions[index]
		if !action.definition.Exported {
			writeError(w, 400, "publish an exported, versioned action before sharing it")
			return
		}
		siteMode = action.declared != nil
	}
	sources, err := s.actionConnectionSources(r.Context())
	if err != nil {
		writeServerError(w, err)
		return
	}
	if r.Method == http.MethodGet {
		connections := []actionConnectionEdit{}
		managed := map[string]bool{}
		custom := false
		for _, source := range sources {
			id := resourceConnectionID(kind, resource, source)
			role, exists := access.Roles[id]
			if !exists {
				continue
			}
			if role.SignedIn || role.Owners || role.Workspace != (source == "*") {
				custom = true
			}
			for bindingID, binding := range access.Bindings {
				if binding.Role == id && (source == "*" || bindingID != id || binding.Scope != nil) {
					custom = true
				}
			}
			user, hasUser := access.Grants[id+"-user"]
			siteGrant, hasSite := access.Grants[id+"-site"]
			managed[id+"-user"], managed[id+"-site"] = true, true
			subscription, hasSubscription := access.Grants[id+"-subscribe"]
			managed[id+"-subscribe"] = true
			for _, mode := range []string{"user", "site", "subscribe"} {
				grant, exists := access.Grants[id+"-"+mode]
				if exists && (grant.Kind != kind || grant.Resource != resource || !slices.Equal(grant.Operations, []string{mode}) || !reflect.DeepEqual(grant.Subjects, Subjects{Roles: []string{id}}) || grant.Scope != nil || len(grant.Except) > 0) {
					custom = true
				}
			}
			connections = append(connections, actionConnectionEdit{Source: source, User: hasUser && slices.Contains(user.Operations, "user"), Site: hasSite && slices.Contains(siteGrant.Operations, "site"), Subscribe: hasSubscription && slices.Contains(subscription.Operations, "subscribe"), Where: siteGrant.Where, Check: siteGrant.Check, Fields: siteGrant.Fields})
		}
		for id, grant := range access.Grants {
			if grant.Kind == kind && permissionSelectorMatches(grant.Kind, grant.Resource, resource) && !slices.ContainsFunc(grant.Except, func(selector string) bool {
				return permissionSelectorMatches(grant.Kind, selector, resource)
			}) && !managed[id] {
				custom = true
			}
		}
		access.Published = nil
		base, err := json.Marshal(access)
		if err != nil {
			writeServerError(w, err)
			return
		}
		writeJSON(w, 200, map[string]any{"base": string(base), "sources": sources, "connections": connections, "custom": custom, "siteMode": siteMode, "event": kind == "event"})
		return
	}
	var request struct {
		Base        string                 `json:"base"`
		Connections []actionConnectionEdit `json:"connections"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		writeError(w, 400, "invalid connection settings")
		return
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		writeError(w, 400, "submit one connection edit")
		return
	}
	base, err := decodePermissionPolicy([]byte(request.Base))
	if err != nil {
		writeError(w, 400, err.Error())
		return
	}
	if len(request.Connections) > 64 {
		writeError(w, 400, "at most 64 connection entries are allowed")
		return
	}
	local := CloneSiteAccess(base)
	seen := map[string]bool{}
	for _, edit := range request.Connections {
		if !slices.Contains(sources, edit.Source) || seen[edit.Source] {
			writeError(w, 400, "choose distinct published sites or the workspace audience")
			return
		}
		seen[edit.Source] = true
		if edit.Site && !siteMode {
			writeError(w, 400, "site mode requires a declared action")
			return
		}
		principal := ""
		if edit.Source != "*" {
			actor, err := s.sitePrincipal(r.Context(), edit.Source)
			if err != nil {
				writeServerError(w, err)
				return
			}
			principal = actor.ID
		}
		local, err = editResourceConnection(local, kind, resource, edit, principal)
		if err != nil {
			writeError(w, 400, err.Error())
			return
		}
	}
	ctx, err := s.managementContext(r.Context(), identity, site)
	if err != nil {
		writeServerError(w, err)
		return
	}
	err = s.config.Access.UpdateSiteAccess(ctx, site, func(current SiteAccess, exists bool) (*SiteAccess, error) {
		if err := s.mayChangeAccess(ctx, identity, site, current, exists); err != nil {
			return nil, err
		}
		merged, conflicts := mergePermissionPolicy(base, current, local)
		if len(conflicts) > 0 {
			return nil, errors.New("connection permissions changed while editing; reload before saving")
		}
		if err := s.validateSiteAccess(merged); err != nil {
			return nil, err
		}
		merged.Published = current.Published
		return &merged, nil
	})
	if err != nil {
		writeError(w, 409, err.Error())
		return
	}
	slog.Info("app connection permissions changed", "site", site, "kind", kind, "resource", resource, "by", identity.ID)
	writeJSON(w, 200, map[string]bool{"saved": true})
}

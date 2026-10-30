package hex

import (
	"context"
	"net/http"
	"slices"
	"strings"
)

type appActionCatalog struct {
	Site    string
	Owner   bool
	Actions []appActionCard
}

type appActionCard struct {
	ActionDefinition
	Effect, Access, Tone, Callers string
	SiteMode                      bool
	Kind                          string
}

func (s *Server) appActionCatalog(ctx context.Context, site string, identity *Identity, access SiteAccess, owner bool) (appActionCatalog, error) {
	view := appActionCatalog{Site: site, Owner: owner}
	actions, err := s.siteActions(ctx, site)
	if err != nil {
		return view, err
	}
	authorization := siteAuthorization{access: access, identity: identity, role: s.siteRole(identity, access, true)}
	for _, action := range actions {
		public := len(workspaceConnectionModes(access, action.definition.Name)) > 0
		if !owner && authorization.role == roleNone && (!action.definition.Exported || !public) {
			continue
		}
		card := appActionCard{ActionDefinition: action.definition, Effect: action.effect, Access: "Action permission required", Tone: "warn", Callers: "This app", SiteMode: action.declared != nil, Kind: "action"}
		if card.Effect == "" {
			card.Effect = "Platform operation"
		}
		if actionAllowed(action, authorization) {
			card.Access, card.Tone = "Action granted · record rules apply", "good"
			if declared := action.declared; declared != nil && declared.Authorization != "action" {
				op := declared.Operation
				if op == "get" || op == "list" {
					op = "read"
				}
				if !authorization.permits("collection", declared.Collection, op) {
					card.Access, card.Tone = "Data permission required", "warn"
				}
			}
		}
		if action.definition.Exported {
			modes := []string{}
			for _, grant := range access.Grants {
				if grant.Kind != "action-consumer" || !permissionSelectorMatches(grant.Kind, grant.Resource, action.definition.Name) || slices.ContainsFunc(grant.Except, func(selector string) bool {
					return permissionSelectorMatches(grant.Kind, selector, action.definition.Name)
				}) {
					continue
				}
				for _, operation := range grant.Operations {
					label := map[string]string{"user": "Users via other apps", "site": "Site automations", "run": "Existing app connections"}[operation]
					if label != "" && !slices.Contains(modes, label) {
						modes = append(modes, label)
					}
				}
			}
			slices.Sort(modes)
			card.Callers = strings.Join(modes, " · ")
			if card.Callers == "" {
				card.Callers = "Not shared with other apps"
			}
		}
		view.Actions = append(view.Actions, card)
	}
	if s.eventsEnabled() {
		rows, err := s.workspaceShareRows(ctx, site, access)
		if err != nil {
			return view, err
		}
		for _, row := range rows {
			if row.Kind != "event" {
				continue
			}
			public := workspaceShared(access, "event", row.Name, "subscribe")
			if !owner && authorization.role == roleNone && !public {
				continue
			}
			card := appActionCard{Kind: "event", ActionDefinition: ActionDefinition{Name: row.Name, Description: row.Description, Exported: true}, Effect: "Triggers subscriber automations", Access: "Publishing permission required", Tone: "warn", Callers: "Subscription access by permission group"}
			if public {
				card.Callers = "All workspace apps can subscribe"
			}
			if authorization.permits("event", row.Name, "publish") {
				card.Access, card.Tone = "You can publish this event", "good"
			}
			view.Actions = append(view.Actions, card)
		}
	}
	return view, nil
}

func (s *Server) appActionsPage(w http.ResponseWriter, r *http.Request) {
	if !s.platformRequest(r) {
		http.NotFound(w, r)
		return
	}
	identity := s.requestIdentity(r)
	if identity == nil {
		writeError(w, 401, "not authenticated")
		return
	}
	site := r.PathValue("site")
	access, exists, err := s.sitePolicy(r.Context(), site)
	if err != nil {
		writeServerError(w, err)
		return
	}
	if !exists {
		http.NotFound(w, r)
		return
	}
	owner := identity.matchesAny(access.Owners)
	catalog, err := s.appActionCatalog(r.Context(), site, identity, access, owner)
	if err != nil {
		writeServerError(w, err)
		return
	}
	if !owner && s.siteRole(identity, access, exists) == roleNone && len(catalog.Actions) == 0 {
		writeError(w, 403, "no visible actions")
		return
	}
	s.writePortalPage(w, "app-actions-page", struct {
		Chrome
		Catalog appActionCatalog
	}{s.chromeFor(identity, "sites"), catalog})
}

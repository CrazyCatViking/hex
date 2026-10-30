package hex

import (
	"encoding/json"
	"net/http"
	"slices"
	"strings"
)

func (s *Server) listEventTypesHTTP(w http.ResponseWriter, r *http.Request) {
	if !eventOwner(w, r) {
		return
	}
	s.listOwnedDurableRecords(w, r, "event-types", func(document Document) (any, error) {
		var topic eventTopic
		if err := json.Unmarshal(document.Data, &topic); err != nil {
			return nil, err
		}
		return topic, nil
	})
}

// listSubscriptionsHTTP shows a consumer its own subscriptions, which are
// stored with each producer's event types. Stale subscriptions still pin an
// older automation revision and need refreshing after a redeploy.
func (s *Server) listSubscriptionsHTTP(w http.ResponseWriter, r *http.Request) {
	if !eventOwner(w, r) {
		return
	}
	site := r.PathValue("site")
	automations, err := s.config.Automations.ListSiteAutomations(r.Context(), site)
	if err != nil {
		writeServerError(w, err)
		return
	}
	type subscriptionStatus struct {
		EventSubscription
		Allowed bool `json:"allowed"`
		Stale   bool `json:"stale"`
	}
	items := []subscriptionStatus{}
	for after := ""; ; {
		documents, err := s.config.Database.(GuardedDatabase).ListMatching(r.Context(), systemSite, "event-types", ListOptions{Limit: 100, After: after}, nil)
		if err != nil {
			writeServerError(w, err)
			return
		}
		for _, document := range documents {
			var topic eventTopic
			if err := json.Unmarshal(document.Data, &topic); err != nil {
				writeServerError(w, err)
				return
			}
			for _, subscription := range topic.Subscriptions {
				if subscription.Site != site {
					continue
				}
				status := subscriptionStatus{EventSubscription: subscription, Allowed: s.eventSubscriberAllowed(r.Context(), subscription) == nil}
				index := slices.IndexFunc(automations, func(a ScheduledAutomation) bool { return a.Automation.Name == subscription.Automation })
				status.Stale = !subscription.Disabled && (index < 0 || automations[index].Revision != subscription.Revision)
				items = append(items, status)
			}
		}
		if len(documents) < 100 {
			break
		}
		after = documents[len(documents)-1].ID
	}
	slices.SortFunc(items, func(a, b subscriptionStatus) int { return strings.Compare(a.ID, b.ID) })
	writeJSON(w, 200, map[string]any{"items": items})
}

func (s *Server) listEventsHTTP(w http.ResponseWriter, r *http.Request) {
	if !eventOwner(w, r) {
		return
	}
	s.listOwnedDurableRecords(w, r, "events", func(document Document) (any, error) {
		var record eventRecord
		if err := json.Unmarshal(document.Data, &record); err != nil {
			return nil, err
		}
		return record, nil
	})
}

func (s *Server) listWorkflowsHTTP(w http.ResponseWriter, r *http.Request) {
	if !eventOwner(w, r) {
		return
	}
	s.listOwnedDurableRecords(w, r, "workflows", func(document Document) (any, error) {
		var workflow Workflow
		if err := json.Unmarshal(document.Data, &workflow); err != nil {
			return nil, err
		}
		return workflowSummary(workflow), nil
	})
}

func (s *Server) listOwnedDurableRecords(w http.ResponseWriter, r *http.Request, collection string, project func(Document) (any, error)) {
	after := r.URL.Query().Get("after")
	if after != "" && !namePattern.MatchString(after) {
		writeError(w, 400, "invalid cursor")
		return
	}
	site, _ := json.Marshal(r.PathValue("site"))
	documents, err := s.config.Database.(GuardedDatabase).ListMatching(r.Context(), systemSite, collection, ListOptions{Limit: 25, After: after}, &RecordCondition{Field: "data.site", Op: "eq", Value: site})
	if err != nil {
		writeServerError(w, err)
		return
	}
	items := make([]any, 0, len(documents))
	next := ""
	for _, document := range documents {
		item, err := project(document)
		if err != nil {
			writeServerError(w, err)
			return
		}
		items = append(items, item)
		next = document.ID
	}
	writeJSON(w, 200, map[string]any{"items": items, "after": next})
}

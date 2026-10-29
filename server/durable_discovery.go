package hex

import (
	"encoding/json"
	"net/http"
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

package hex

import (
	"context"
	"encoding/json"
)

func (s *Server) executeDeclaredRead(ctx context.Context, caller ActionContext, action DeclaredAction, input json.RawMessage) (any, error) {
	actionFilter, allowed := caller.authorization.resourceReadFilter("action", action.Name, "run")
	if !allowed {
		return nil, ErrForbidden
	}
	var request struct {
		After string `json:"after"`
		Limit int    `json:"limit"`
	}
	if err := json.Unmarshal(input, &request); err != nil {
		return nil, &ActionError{Message: "invalid read input"}
	}
	project := func(document Document) (any, error) {
		var data map[string]json.RawMessage
		if err := json.Unmarshal(document.Data, &data); err != nil {
			return nil, err
		}
		selected := map[string]json.RawMessage{}
		for _, field := range action.Select {
			if value, ok := data[field]; ok {
				selected[field] = value
			}
		}
		return map[string]any{"id": document.ID, "data": selected}, nil
	}
	if action.Operation == "get" {
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(input, &fields); err != nil {
			return nil, err
		}
		var id string
		if json.Unmarshal(fields[action.idField()], &id) != nil || !validCollectionName(id) {
			return nil, &ActionError{Message: "invalid document ID"}
		}
		document, err := s.config.Database.Get(ctx, caller.Site, action.Collection, id)
		if err != nil {
			return nil, err
		}
		if !caller.CanReadDocument(action.Collection, document) || !actionFilter.Matches(document) {
			return nil, ErrNotFound
		}
		return project(document)
	}
	if request.Limit == 0 {
		request.Limit = 100
	}
	if request.Limit < 1 || request.Limit > 100 || (request.After != "" && !validCollectionName(request.After)) {
		return nil, &ActionError{Message: "list requires a limit of 1–100 and a valid cursor"}
	}
	dataFilter, allowed := caller.authorization.readFilter(action.Collection)
	if !allowed {
		return nil, ErrForbidden
	}
	options := ListOptions{After: request.After, Limit: request.Limit, MaxDocumentBytes: MaxActionInputBytes}
	filter := andConditions(dataFilter, actionFilter)
	var documents []Document
	var err error
	if filter == nil {
		documents, err = s.config.Database.List(ctx, caller.Site, action.Collection, options)
	} else if database, supported := s.config.Database.(GuardedDatabase); supported {
		documents, err = database.ListMatching(ctx, caller.Site, action.Collection, options, filter)
	} else {
		return nil, ErrAuthorizationUnsupported
	}
	if err != nil {
		return nil, err
	}
	items := make([]any, 0, len(documents))
	after := ""
	for _, document := range documents {
		item, err := project(document)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
		after = document.ID
	}
	return map[string]any{"items": items, "after": after}, nil
}

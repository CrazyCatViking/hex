package main

import (
	"context"
	"crypto/rand"
	"encoding/json"

	hex "github.com/crazycatviking/hex/server"
)

// A consuming platform implements app operations in its backend. Publishing a
// static frontend alone does not register executable actions.
func appActions(database hex.Database) (*hex.ActionRegistry, error) {
	registry := new(hex.ActionRegistry)
	if database == nil {
		return registry, nil
	}
	err := registry.Register("my-app", hex.Action{
		Definition: hex.ActionDefinition{
			Name:        "create-task",
			Description: "Create a task in my-app for the current user.",
			InputSchema: json.RawMessage(`{
				"type": "object",
				"properties": {"title": {"type": "string", "minLength": 1, "maxLength": 200}},
				"required": ["title"],
				"additionalProperties": false
			}`),
			OutputSchema: json.RawMessage(`{
				"type": "object",
				"properties": {"id": {"type": "string"}},
				"required": ["id"],
				"additionalProperties": false
			}`),
		},
		Handler: func(ctx context.Context, caller hex.ActionContext, input json.RawMessage) (any, error) {
			options, err := caller.CollectionWriteOptions("tasks")
			if err != nil {
				return nil, err
			}
			document, err := database.Put(ctx, caller.Site, "tasks", rand.Text(), input, options)
			if err != nil {
				return nil, err
			}
			return map[string]string{"id": document.ID}, nil
		},
	})
	return registry, err
}

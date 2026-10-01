package hex_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	hex "github.com/crazycatviking/hex/server"
	"github.com/crazycatviking/hex/server/providers/memory"
)

func taskAction() hex.Action {
	return hex.Action{
		Definition: hex.ActionDefinition{
			Name: "create-task", Description: "Create a task for the current user.",
			InputSchema:  json.RawMessage(`{"type":"object","properties":{"title":{"type":"string","minLength":1,"maxLength":100},"priority":{"type":"integer","minimum":1,"maximum":3}},"required":["title"],"additionalProperties":false}`),
			OutputSchema: json.RawMessage(`{"type":"object","properties":{"id":{"type":"string"}},"required":["id"],"additionalProperties":false}`),
		},
		Handler: func(context.Context, hex.ActionContext, json.RawMessage) (any, error) {
			return map[string]string{"id": "task-1"}, nil
		},
	}
}

func TestActionContractExecution(t *testing.T) {
	registry := new(hex.ActionRegistry)
	action := taskAction()
	calls := 0
	action.Handler = func(ctx context.Context, caller hex.ActionContext, input json.RawMessage) (any, error) {
		calls++
		if caller.Site != "demo" || caller.Identity.ID != "alice" || caller.Role != "editor" {
			t.Fatalf("unexpected caller: %+v", caller)
		}
		return map[string]string{"id": "task-1"}, nil
	}
	if err := registry.Register("demo", action); err != nil {
		t.Fatal(err)
	}
	server := hex.New(hex.Config{Actions: registry, Identity: hex.StaticIdentity{Identity: hex.Identity{ID: "alice"}}})
	base := "/api/sites/demo/actions"
	listing := request(t, server, "GET", base, nil, 200)
	if !strings.Contains(listing.Body.String(), `"name":"create-task"`) {
		t.Fatal(listing.Body.String())
	}
	request(t, server, "GET", base+"/create-task", nil, 200)
	request(t, server, "GET", "/api/sites/other/actions", nil, 200)
	request(t, server, "POST", "/api/sites/other/actions/create-task", []byte(`{"title":"Task"}`), 404)
	request(t, server, "POST", base+"/unknown", []byte(`{}`), 404)
	for _, input := range []string{`{}`, `{"title":""}`, `{"title":"Task","priority":4}`, `{"title":"Task","priority":1.5}`, `{"title":"Task","extra":true}`, `{"title":123}`, `[]`, `{"title":"Task"} trailing`} {
		request(t, server, "POST", base+"/create-task", []byte(input), 400)
	}
	request(t, server, "POST", base+"/create-task", []byte(strings.Repeat(" ", hex.MaxActionInputBytes+1)), 400)
	if calls != 0 {
		t.Fatal("invalid input invoked the handler")
	}
	response := request(t, server, "POST", base+"/create-task", []byte(`{"title":"Task","priority":2}`), 200)
	if calls != 1 || !strings.Contains(response.Body.String(), `"id":"task-1"`) {
		t.Fatal(response.Body.String())
	}
	r := httptest.NewRequest(http.MethodPost, base+"/create-task", strings.NewReader(`{"title":"Task"}`))
	w := httptest.NewRecorder()
	server.ServeHTTP(w, r)
	if w.Code != 403 || calls != 1 {
		t.Fatal("execution bypassed request-origin checks")
	}
}

func TestActionAccessAndDataRules(t *testing.T) {
	access := memory.NewAccessStore()
	policy := hex.SiteAccess{
		Owners: []string{"user:owner"}, Editors: []string{"user:editor"}, Viewers: []string{"user:viewer"},
		Collections: map[string]hex.DataRule{"drafts": {Write: hex.Audience{Level: hex.LevelCreator}}, "locked": {Write: hex.Audience{Level: hex.LevelOwners}}},
		Files:       map[string]hex.DataRule{"private/": {Read: hex.Audience{Level: hex.LevelOwners}, Write: hex.Audience{Level: hex.LevelOwners}}},
	}
	if err := access.PutSiteAccess(context.Background(), "demo", policy); err != nil {
		t.Fatal(err)
	}
	registry := new(hex.ActionRegistry)
	action := taskAction()
	action.Handler = func(ctx context.Context, caller hex.ActionContext, input json.RawMessage) (any, error) {
		options, err := caller.CollectionWriteOptions("drafts")
		if err != nil || !options.CreatorOnly || options.Creator != "editor" {
			t.Fatalf("incorrect creator rule: %+v %v", options, err)
		}
		if _, err := caller.CollectionWriteOptions("locked"); !errors.Is(err, hex.ErrForbidden) {
			t.Fatalf("ignored collection rule: %v", err)
		}
		if caller.CanReadFile("private/file.txt") || caller.CanWriteFile("private/file.txt") {
			t.Fatal("ignored file rule")
		}
		return map[string]string{"id": "task-1"}, nil
	}
	if err := registry.Register("demo", action); err != nil {
		t.Fatal(err)
	}
	read := taskAction()
	read.Definition.Name = "read-summary"
	read.Audience = hex.Audience{Level: hex.LevelViewers}
	if err := registry.Register("demo", read); err != nil {
		t.Fatal(err)
	}
	for _, user := range []string{"editor", "viewer", "outsider"} {
		server := hex.New(hex.Config{Actions: registry, Access: access, Identity: hex.StaticIdentity{Identity: hex.Identity{ID: user}}})
		if user == "outsider" {
			request(t, server, "GET", "/api/sites/demo/actions", nil, 403)
			request(t, server, "POST", "/api/sites/demo/actions/create-task", []byte(`{"title":"Task"}`), 403)
			continue
		}
		list := request(t, server, "GET", "/api/sites/demo/actions", nil, 200).Body.String()
		if !strings.Contains(list, "read-summary") {
			t.Fatal(list)
		}
		if user == "viewer" {
			if strings.Contains(list, "create-task") {
				t.Fatal("exposed an unavailable action")
			}
			request(t, server, "GET", "/api/sites/demo/actions/create-task", nil, 403)
			request(t, server, "POST", "/api/sites/demo/actions/create-task", []byte(`{"title":"Task"}`), 403)
		} else {
			request(t, server, "POST", "/api/sites/demo/actions/create-task", []byte(`{"title":"Task"}`), 200)
		}
	}
}

func TestActionHandlerFailures(t *testing.T) {
	for _, test := range []struct {
		name   string
		result any
		err    error
		status int
	}{
		{"invalid-output", map[string]int{"id": 123}, nil, 500},
		{"business-rule", nil, &hex.ActionError{Message: "period is closed"}, 400},
		{"forbidden", nil, hex.ErrForbidden, 403},
		{"missing", nil, hex.ErrNotFound, 404},
		{"internal", nil, errors.New("private details"), 500},
	} {
		t.Run(test.name, func(t *testing.T) {
			registry := new(hex.ActionRegistry)
			action := taskAction()
			action.Handler = func(context.Context, hex.ActionContext, json.RawMessage) (any, error) { return test.result, test.err }
			if err := registry.Register("demo", action); err != nil {
				t.Fatal(err)
			}
			response := request(t, hex.New(hex.Config{Actions: registry}), "POST", "/api/sites/demo/actions/create-task", []byte(`{"title":"Task"}`), test.status)
			if strings.Contains(response.Body.String(), "private details") {
				t.Fatal("leaked an internal error")
			}
			if test.status == 400 && !strings.Contains(response.Body.String(), "period is closed") {
				t.Fatal(response.Body.String())
			}
		})
	}
}

func TestActionSchemaReferencesAndRegistration(t *testing.T) {
	registry := new(hex.ActionRegistry)
	action := taskAction()
	action.Definition.InputSchema = json.RawMessage(`{"$defs":{"title":{"type":"string","minLength":1}},"type":"object","properties":{"title":{"$ref":"#/$defs/title"}},"required":["title"],"additionalProperties":false}`)
	if err := registry.Register("demo", action); err != nil {
		t.Fatal(err)
	}
	if err := action.Definition.ValidateInput([]byte(`{"title":"Task"}`)); err != nil {
		t.Fatal(err)
	}
	if err := action.Definition.ValidateInput([]byte(`{"title":false}`)); err == nil {
		t.Fatal("ignored local reference")
	}
	if err := registry.Register("demo", action); err == nil {
		t.Fatal("accepted duplicate action")
	}
	for _, schema := range []string{`{"type":"invalid-type"}`, `{"$ref":"http://127.0.0.1:1/private"}`, `null`, `{"type":"object"} trailing`} {
		action.Definition.Name = "bad-schema"
		action.Definition.InputSchema = json.RawMessage(schema)
		if err := registry.Register("demo", action); err == nil {
			t.Fatalf("accepted %s", schema)
		}
	}
	// Large integers retain exact precision during schema validation.
	definition := hex.ActionDefinition{InputSchema: json.RawMessage(`{"type":"integer","const":9007199254740993}`)}
	if err := definition.ValidateInput([]byte(`9007199254740993`)); err != nil {
		t.Fatal(err)
	}
	if err := definition.ValidateInput([]byte(`9007199254740992`)); err == nil {
		t.Fatal("lost integer precision")
	}
}

type unresolvedActionIdentity struct{}

func (unresolvedActionIdentity) ResolveIdentity(*http.Request) (*hex.Identity, error) {
	return nil, errors.New("resolver unavailable")
}

func TestActionsRejectUnresolvedIdentity(t *testing.T) {
	registry := new(hex.ActionRegistry)
	if err := registry.Register("demo", taskAction()); err != nil {
		t.Fatal(err)
	}
	server := hex.New(hex.Config{Actions: registry, Identity: unresolvedActionIdentity{}})
	request(t, server, "GET", "/api/sites/demo/actions", nil, 401)
	request(t, server, "POST", "/api/sites/demo/actions/create-task", []byte(`{"title":"Task"}`), 401)
}

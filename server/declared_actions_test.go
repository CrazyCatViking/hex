package hex_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	hex "github.com/crazycatviking/hex/server"
	"github.com/crazycatviking/hex/server/providers/easyauth"
	"github.com/crazycatviking/hex/server/providers/local"
	"github.com/crazycatviking/hex/server/providers/memory"
)

const taskActions = `[
  {"name": "log-call", "description": "Log a customer call.", "operation": "create", "collection": "calls",
   "input": {"type": "object", "properties": {"customer": {"type": "string", "minLength": 1}, "notes": {"type": "string"}},
             "required": ["customer"], "additionalProperties": false}},
  {"name": "set-status", "description": "Change the status of a call.", "operation": "update", "collection": "calls",
   "input": {"type": "object", "properties": {"id": {"type": "string"}, "status": {"enum": ["open", "done"]}},
             "required": ["id", "status"], "additionalProperties": false}},
  {"name": "remove-call", "description": "Delete a call.", "operation": "delete", "collection": "calls", "idField": "call",
   "input": {"type": "object", "properties": {"call": {"type": "string"}}, "required": ["call"]}},
  {"name": "suggest", "description": "Suggest an improvement.", "operation": "create", "collection": "ideas", "audience": "viewers",
   "input": {"type": "object", "properties": {"text": {"type": "string"}}, "required": ["text"]}}
]`

// publishWithActions publishes a one-page site whose hex.json declares
// actions; an empty list publishes it without actions.
func publishWithActions(t *testing.T, server http.Handler, headers http.Header, site, actions string) {
	t.Helper()
	files := map[string]string{"index.html": "<h1>calls</h1>"}
	body := map[string]any{"files": manifest(files)}
	if actions != "" {
		body["actions"] = json.RawMessage(actions)
	}
	response := requestAs(t, server, headers, "POST", "/api/hex/sites/"+site+"/publish", encode(t, body), 200)
	var plan publishPlan
	if err := json.Unmarshal(response.Body.Bytes(), &plan); err != nil {
		t.Fatal(err)
	}
	for _, upload := range plan.Uploads {
		requestAs(t, server, headers, "PUT", upload.URL, []byte(files[upload.Path]), 204)
	}
	requestAs(t, server, headers, "POST", "/api/hex/sites/"+site+"/publish/complete", encode(t, body), 200)
}

func TestDeclaredActionsRunAsTheCaller(t *testing.T) {
	server, store := setupWithAccess(t)
	owner := principalHeaders("owner-id")
	editor := principalHeaders("editor-id")
	viewer := principalHeaders("viewer-id")

	if !strings.Contains(requestAs(t, server, owner, "GET", "/api/hex/capabilities", nil, 200).Body.String(), `"actions":true`) {
		t.Fatal("declared actions are not advertised")
	}
	publishWithActions(t, server, owner, "calls", taskActions)
	putPolicy(t, server, owner, "calls", `{"owners":["user:owner-id"],"editors":["user:editor-id","user:second-editor"],"viewers":["user:editor-id","user:second-editor","user:viewer-id"],
		"collections":{"calls":{"write":"creator"}}}`)
	if _, exists := readSiteFile(t, store, "calls", ".hex-actions.json"); !exists {
		t.Fatal("actions were not recorded with the site")
	}

	base := "/api/sites/calls/actions"
	list := requestAs(t, server, editor, "GET", base, nil, 200).Body.String()
	for _, name := range []string{"log-call", "set-status", "remove-call", "suggest"} {
		if !strings.Contains(list, `"name":"`+name+`"`) {
			t.Fatalf("%s missing from %s", name, list)
		}
	}
	if !strings.Contains(list, `"outputSchema":{"type":"object"`) {
		t.Fatalf("declared actions need an output contract: %s", list)
	}

	// Create stores the input as a new document created by the caller.
	created := requestAs(t, server, editor, "POST", base+"/log-call", []byte(`{"customer":"Acme","notes":"Called"}`), 200)
	var result struct{ ID string }
	if err := json.Unmarshal(created.Body.Bytes(), &result); err != nil || result.ID == "" {
		t.Fatalf("create returned no ID: %s", created.Body.String())
	}
	document := requestAs(t, server, editor, "GET", "/api/sites/calls/db/calls/"+result.ID, nil, 200).Body.String()
	if !strings.Contains(document, `"customer":"Acme"`) || !strings.Contains(document, `"createdBy":"editor-id"`) {
		t.Fatalf("unexpected document: %s", document)
	}

	// Input is validated against the declared schema before anything runs.
	for _, input := range []string{`{}`, `{"customer":""}`, `{"customer":"Acme","extra":1}`, `[]`} {
		requestAs(t, server, editor, "POST", base+"/log-call", []byte(input), 400)
	}

	// Update merges fields into the existing document and never creates one.
	requestAs(t, server, editor, "POST", base+"/set-status", []byte(`{"id":"`+result.ID+`","status":"done"}`), 200)
	document = requestAs(t, server, editor, "GET", "/api/sites/calls/db/calls/"+result.ID, nil, 200).Body.String()
	if !strings.Contains(document, `"customer":"Acme"`) || !strings.Contains(document, `"status":"done"`) || strings.Contains(document, `"id":"`+result.ID+`","status"`) {
		t.Fatalf("update did not merge: %s", document)
	}
	requestAs(t, server, editor, "POST", base+"/set-status", []byte(`{"id":"missing","status":"done"}`), 400)
	requestAs(t, server, editor, "POST", base+"/set-status", []byte(`{"id":"../x","status":"done"}`), 400)

	// The collection's creator-only rule applies: another editor cannot
	// change the editor's document.
	requestAs(t, server, principalHeaders("second-editor"), "POST", base+"/set-status", []byte(`{"id":"`+result.ID+`","status":"open"}`), 403)

	// Viewers see only actions open to viewers, and running one still needs
	// write access to its collection: "ideas" is writable by editors.
	list = requestAs(t, server, viewer, "GET", base, nil, 200).Body.String()
	if strings.Contains(list, "log-call") || !strings.Contains(list, "suggest") {
		t.Fatalf("viewer saw the wrong actions: %s", list)
	}
	requestAs(t, server, viewer, "POST", base+"/log-call", []byte(`{"customer":"Acme"}`), 403)
	requestAs(t, server, viewer, "POST", base+"/suggest", []byte(`{"text":"More coffee"}`), 403)
	requestAs(t, server, principalHeaders("stranger"), "GET", base, nil, 403)

	// Delete uses the configured ID field.
	requestAs(t, server, editor, "POST", base+"/remove-call", []byte(`{"call":"`+result.ID+`"}`), 200)
	requestAs(t, server, editor, "GET", "/api/sites/calls/db/calls/"+result.ID, nil, 404)
	requestAs(t, server, editor, "POST", base+"/remove-call", []byte(`{"call":"`+result.ID+`"}`), 400)

	// Republishing without actions removes them.
	publishWithActions(t, server, owner, "calls", "")
	if list := requestAs(t, server, editor, "GET", base, nil, 200).Body.String(); list != "[]\n" && list != "[]" {
		t.Fatalf("actions survived a publication without them: %s", list)
	}
	requestAs(t, server, editor, "POST", base+"/log-call", []byte(`{"customer":"Acme"}`), 404)
}

func TestDeclaredActionsAreValidatedBeforePublishing(t *testing.T) {
	server, store := setupWithAccess(t)
	owner := principalHeaders("owner-id")
	object := `"input":{"type":"object","properties":{"id":{"type":"string"}},"required":["id"]}`
	for name, actions := range map[string]string{
		"unknown operation":   `[{"name":"a","description":"A.","operation":"upsert","collection":"c",` + object + `}]`,
		"no description":      `[{"name":"a","description":" ","operation":"create","collection":"c",` + object + `}]`,
		"invalid name":        `[{"name":"a b","description":"A.","operation":"create","collection":"c",` + object + `}]`,
		"invalid collection":  `[{"name":"a","description":"A.","operation":"create","collection":"../c",` + object + `}]`,
		"not an object":       `[{"name":"a","description":"A.","operation":"create","collection":"c","input":{"type":"string"}}]`,
		"id not required":     `[{"name":"a","description":"A.","operation":"update","collection":"c","input":{"type":"object"}}]`,
		"idField on create":   `[{"name":"a","description":"A.","operation":"create","collection":"c","idField":"id",` + object + `}]`,
		"external reference":  `[{"name":"a","description":"A.","operation":"create","collection":"c","input":{"type":"object","$ref":"https://example.com/s.json"}}]`,
		"duplicate name":      `[{"name":"a","description":"A.","operation":"create","collection":"c",` + object + `},{"name":"a","description":"A.","operation":"delete","collection":"c",` + object + `}]`,
		"creator audience":    `[{"name":"a","description":"A.","operation":"create","collection":"c","audience":"creator",` + object + `}]`,
		"untyped principal":   `[{"name":"a","description":"A.","operation":"create","collection":"c","audience":["alex"],` + object + `}]`,
		"invalid JSON schema": `[{"name":"a","description":"A.","operation":"create","collection":"c","input":{"type":"object","minProperties":"x"}}]`,
	} {
		body := map[string]any{"files": manifest(map[string]string{"index.html": "x"}), "actions": json.RawMessage(actions)}
		requestAs(t, server, owner, "POST", "/api/hex/sites/bad/publish", encode(t, body), 400)
		if _, exists := readSiteFile(t, store, "bad", "index.html"); exists {
			t.Fatalf("%s: files were changed", name)
		}
	}

	many := make([]string, 33)
	for i := range many {
		many[i] = `{"name":"a` + strings.Repeat("x", i) + `","description":"A.","operation":"create","collection":"c",` + object + `}`
	}
	body := map[string]any{"files": manifest(map[string]string{"index.html": "x"}), "actions": json.RawMessage("[" + strings.Join(many, ",") + "]")}
	requestAs(t, server, owner, "POST", "/api/hex/sites/bad/publish", encode(t, body), 400)
}

func TestDeclaredActionsCannotShadowRegisteredActions(t *testing.T) {
	store, err := local.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	registry := new(hex.ActionRegistry)
	if err := registry.Register("calls", taskAction()); err != nil {
		t.Fatal(err)
	}
	server := hex.New(hex.Config{
		Sites: store, Publisher: store, Database: memory.NewDatabase(), Actions: registry,
		Identity: easyauth.Resolver{}, Access: memory.NewAccessStore(),
	})
	owner := principalHeaders("owner-id")
	shadow := `[{"name":"create-task","description":"Mine.","operation":"create","collection":"c","input":{"type":"object"}}]`
	body := map[string]any{"files": manifest(map[string]string{"index.html": "x"}), "actions": json.RawMessage(shadow)}
	requestAs(t, server, owner, "POST", "/api/hex/sites/calls/publish", encode(t, body), 400)

	publishWithActions(t, server, owner, "calls", taskActions)
	list := requestAs(t, server, owner, "GET", "/api/sites/calls/actions", nil, 200).Body.String()
	if !strings.Contains(list, "create-task") || !strings.Contains(list, "log-call") {
		t.Fatalf("registered and declared actions were not merged: %s", list)
	}
	requestAs(t, server, owner, "POST", "/api/sites/calls/actions/create-task", []byte(`{"title":"Task"}`), 200)
}

func TestDeclaredActionsNeedADatabase(t *testing.T) {
	store, err := local.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	server := hex.New(hex.Config{Sites: store, Publisher: store, Identity: easyauth.Resolver{}, Access: memory.NewAccessStore()})
	if strings.Contains(requestAs(t, server, principalHeaders("a"), "GET", "/api/hex/capabilities", nil, 200).Body.String(), `"actions":true`) {
		t.Fatal("actions advertised without a database")
	}
	body := map[string]any{"files": manifest(map[string]string{"index.html": "x"}), "actions": json.RawMessage(taskActions)}
	requestAs(t, server, principalHeaders("a"), "POST", "/api/hex/sites/calls/publish", encode(t, body), 400)
}

func TestPortalShowsDeclaredActions(t *testing.T) {
	server, _ := setupPortal(t)
	owner := signedIn(t, "owner-id", "Olivia Owner", "olivia@example.test")
	publishWithActions(t, server, owner, "calls", taskActions)
	page := requestAs(t, server, owner, "GET", "/manage/calls", nil, 200).Body.String()
	for _, text := range []string{"log-call", "Log a customer call.", "Adds a record to calls", "Changes a record in calls", "Removes a record from calls", "Everyone who can open it"} {
		if !strings.Contains(page, text) {
			t.Fatalf("site overview lacks %q", text)
		}
	}
}

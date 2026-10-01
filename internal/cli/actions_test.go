package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	hex "github.com/crazycatviking/hex/server"
	"github.com/crazycatviking/hex/server/providers/memory"
)

func TestActionsCLIPreservesHTMLHeavyInput(t *testing.T) {
	directory := t.TempDir()
	payload := []byte(`{"title":"` + strings.Repeat("<>&", (hex.MaxActionInputBytes-12)/3) + `"}`)
	if len(payload) > hex.MaxActionInputBytes || !json.Valid(payload) {
		t.Fatal("invalid regression payload")
	}
	expanded, err := json.Marshal(json.RawMessage(payload))
	if err != nil || len(expanded) <= hex.MaxActionInputBytes {
		t.Fatal("payload must exceed the server limit when HTML escaped")
	}
	registry := new(hex.ActionRegistry)
	calls := 0
	if err := registry.Register("demo", hex.Action{
		Definition: cliActionDefinition(),
		Handler: func(ctx context.Context, caller hex.ActionContext, input json.RawMessage) (any, error) {
			calls++
			if !bytes.Equal(input, payload) {
				t.Error("action input bytes changed")
			}
			return map[string]string{"id": "task-1"}, nil
		},
	}); err != nil {
		t.Fatal(err)
	}
	platform := httptest.NewServer(hex.New(hex.Config{Actions: registry}))
	defer platform.Close()
	saveReadProfile(t, directory, platform.URL)
	if err := os.WriteFile(filepath.Join(directory, "input.json"), payload, 0600); err != nil {
		t.Fatal(err)
	}
	got := run(t, directory, "actions", "run", "--site", "demo", "create-task", "--input", "@input.json")
	if calls != 1 || !strings.Contains(got, `"id": "task-1"`) {
		t.Fatalf("action did not execute: %d %s", calls, got)
	}
}

func cliActionDefinition() hex.ActionDefinition {
	return hex.ActionDefinition{
		Name: "create-task", Description: "Create a task.",
		InputSchema:  json.RawMessage(`{"type":"object","properties":{"title":{"type":"string","minLength":1}},"required":["title"],"additionalProperties":false}`),
		OutputSchema: json.RawMessage(`{"type":"object","properties":{"id":{"type":"string"}},"required":["id"],"additionalProperties":false}`),
	}
}

func TestActionsCLIValidatesBeforeExecution(t *testing.T) {
	directory := t.TempDir()
	t.Setenv("HEX_TOKEN", "test-token")
	definition := cliActionDefinition()
	posts := 0
	platform := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-token" || r.Header.Get("X-Hex-Request") != "1" {
			t.Error("missing authenticated request headers")
		}
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodPost {
			posts++
			var input map[string]string
			if err := json.NewDecoder(r.Body).Decode(&input); err != nil || input["title"] != "Test" {
				t.Errorf("invalid payload: %v %v", input, err)
			}
			_, _ = w.Write([]byte(`{"id":"task-1"}`))
			return
		}
		if err := json.NewEncoder(w).Encode(definition); err != nil {
			t.Error(err)
		}
	}))
	defer platform.Close()
	saveReadProfile(t, directory, platform.URL)
	inputPath := filepath.Join(directory, "input with spaces.json")
	for _, input := range []string{`{}`, `{"title":false}`, `{"title":"Test","extra":true}`, `{"title":"Test"} trailing`} {
		if err := os.WriteFile(inputPath, []byte(input), 0600); err != nil {
			t.Fatal(err)
		}
		app, _ := testApp(t, directory)
		if err := app.Execute(context.Background(), []string{"actions", "run", "--site", "demo", "create-task", "--input", "@input with spaces.json"}, "test"); err == nil {
			t.Fatalf("accepted %s", input)
		}
	}
	if posts != 0 {
		t.Fatal("invalid inputs reached execution")
	}
	if err := os.WriteFile(inputPath, []byte(`{"title":"Test"}`), 0600); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(){
		func() { definition.Name = "different-action" },
		func() {
			definition.Name = "create-task"
			definition.OutputSchema = json.RawMessage(`{"$ref":"http://127.0.0.1:1/schema"}`)
		},
	} {
		mutate()
		app, _ := testApp(t, directory)
		if err := app.Execute(context.Background(), []string{"actions", "run", "--site", "demo", "create-task", "--input", "@input with spaces.json"}, "test"); err == nil {
			t.Fatal("accepted invalid contract")
		}
	}
	if posts != 0 {
		t.Fatal("invalid contracts reached execution")
	}
	definition = cliActionDefinition()
	got := run(t, directory, "actions", "run", "--site", "demo", "create-task", "--input", "@input with spaces.json")
	if posts != 1 || !strings.Contains(got, `"id": "task-1"`) {
		t.Fatal(got)
	}
}

func TestActionsCLIAgainstPlatform(t *testing.T) {
	directory := t.TempDir()
	registry := new(hex.ActionRegistry)
	database := memory.NewDatabase()
	if err := registry.Register("demo", hex.Action{
		Definition: cliActionDefinition(),
		Handler: func(ctx context.Context, caller hex.ActionContext, input json.RawMessage) (any, error) {
			options, err := caller.CollectionWriteOptions("tasks")
			if err != nil {
				return nil, err
			}
			document, err := database.Put(ctx, caller.Site, "tasks", "task-1", input, options)
			if err != nil {
				return nil, err
			}
			return map[string]string{"id": document.ID}, nil
		},
	}); err != nil {
		t.Fatal(err)
	}
	platform := httptest.NewServer(hex.New(hex.Config{
		Actions: registry, Database: database,
		Identity: hex.StaticIdentity{Identity: hex.Identity{ID: "alice"}},
	}))
	defer platform.Close()
	saveReadProfile(t, directory, platform.URL)
	if got := run(t, directory, "actions", "list", "--site", "demo"); !strings.Contains(got, "create-task") {
		t.Fatal(got)
	}
	if got := run(t, directory, "actions", "describe", "--site", "demo", "create-task"); !strings.Contains(got, "inputSchema") {
		t.Fatal(got)
	}
	if err := os.WriteFile(filepath.Join(directory, "input.json"), []byte(`{"title":"Test"}`), 0600); err != nil {
		t.Fatal(err)
	}
	run(t, directory, "actions", "run", "--site", "demo", "create-task", "--input", "@input.json")
	got := run(t, directory, "data", "get", "--site", "demo", "--collection", "tasks", "--id", "task-1")
	if !strings.Contains(got, `"createdBy": "alice"`) || !strings.Contains(got, `"title": "Test"`) {
		t.Fatal(got)
	}
}

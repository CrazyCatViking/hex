package hex_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	hex "github.com/crazycatviking/hex/server"
	"github.com/crazycatviking/hex/server/providers/easyauth"
	"github.com/crazycatviking/hex/server/providers/local"
	"github.com/crazycatviking/hex/server/providers/memory"
)

func actionExecutionServer(t *testing.T, handler func(context.Context, hex.ActionContext, json.RawMessage) (any, error)) (*hex.Server, *memory.Database) {
	t.Helper()
	registry := new(hex.ActionRegistry)
	if err := registry.Register("demo", hex.Action{
		Definition: hex.ActionDefinition{
			Name: "check", Description: "Check an action's execution contract.",
			InputSchema:  json.RawMessage(`{"type":"object","properties":{"value":{"type":"string"}},"required":["value"],"additionalProperties":false}`),
			OutputSchema: json.RawMessage(`{"type":"object","properties":{"ok":{"type":"boolean"}},"required":["ok"],"additionalProperties":false}`),
		},
		Handler: handler,
	}); err != nil {
		t.Fatal(err)
	}
	sites, err := local.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := sites.Close(); err != nil {
			t.Error(err)
		}
	})
	access := memory.NewAccessStore()
	if err := access.PutSiteAccess(t.Context(), "demo", hex.SiteAccess{
		Owners: []string{"user:owner"}, Editors: []string{"user:editor"},
	}); err != nil {
		t.Fatal(err)
	}
	database := memory.NewDatabase()
	return hex.New(hex.Config{
		Actions: registry, Publisher: sites, Sites: sites, Database: database,
		Access: access, Identity: easyauth.Resolver{}, Automations: memory.NewAutomationStore(),
	}), database
}

type failingActionResult struct {
	err error
}

func (r failingActionResult) MarshalJSON() ([]byte, error) {
	return nil, r.err
}

func TestSharedActionExecutionTransportContracts(t *testing.T) {
	for _, test := range []struct {
		name       string
		result     any
		err        error
		httpStatus int
		runError   string
	}{
		{"valid", map[string]bool{"ok": true}, nil, http.StatusOK, ""},
		{"invalid output schema", map[string]int{"ok": 1}, nil, http.StatusInternalServerError, "the request failed; see the platform log"},
		{"unserializable output", make(chan int), nil, http.StatusInternalServerError, "the request failed; see the platform log"},
		{"output marshaler business error", failingActionResult{err: &hex.ActionError{Message: "private output details"}}, nil, http.StatusInternalServerError, "the request failed; see the platform log"},
		{"output marshaler forbidden", failingActionResult{err: hex.ErrForbidden}, nil, http.StatusInternalServerError, "the request failed; see the platform log"},
		{"output marshaler not found", failingActionResult{err: hex.ErrNotFound}, nil, http.StatusInternalServerError, "the request failed; see the platform log"},
		{"handler business error", nil, fmt.Errorf("wrapped: %w", &hex.ActionError{Message: "period is closed"}), http.StatusBadRequest, "period is closed"},
		{"handler forbidden", nil, fmt.Errorf("wrapped: %w", hex.ErrForbidden), http.StatusForbidden, "not permitted"},
		{"handler not found", nil, hex.ErrNotFound, http.StatusNotFound, "the request failed; see the platform log"},
		{"handler internal error", nil, errors.New("private handler details"), http.StatusInternalServerError, "the request failed; see the platform log"},
	} {
		t.Run(test.name, func(t *testing.T) {
			var calls atomic.Int32
			server, _ := actionExecutionServer(t, func(ctx context.Context, caller hex.ActionContext, input json.RawMessage) (any, error) {
				calls.Add(1)
				if ctx.Err() != nil || caller.Site != "demo" || caller.Identity == nil {
					t.Errorf("missing caller context: %+v, %v", caller, ctx.Err())
				} else if caller.Identity.Provider == "automation" {
					if caller.Identity.ID != "automation:action-contract" || caller.Role != "owner" {
						t.Errorf("incorrect automation caller: %+v", caller)
					}
				} else if caller.Identity.ID != "editor" || caller.Role != "editor" {
					t.Errorf("incorrect HTTP caller: %+v", caller)
				}
				if string(input) != `{"value":"valid"}` {
					t.Errorf("unexpected action input: %s", input)
				}
				return test.result, test.err
			})
			response := requestAs(t, server, roleHeaders("editor"), http.MethodPost,
				"/api/sites/demo/actions/check", []byte(`{"value":"valid"}`), test.httpStatus)
			run := runScriptAutomation(t, server, `export default hex => hex.action("check", {value:"valid"});`, false)
			if calls.Load() != 2 {
				t.Fatalf("expected one HTTP and one automation invocation, got %d", calls.Load())
			}
			if len(run.Operations) != 1 {
				t.Fatalf("unexpected action operations: %+v", run)
			}
			if test.runError == "" {
				if run.Status != hex.RunSucceeded || run.Operations[0].Status != hex.OperationSucceeded || string(run.Output) != `{"ok":true}` {
					t.Fatalf("valid automation output disagrees with HTTP: %+v", run)
				}
				var output map[string]bool
				if err := json.Unmarshal(response.Body.Bytes(), &output); err != nil || !output["ok"] {
					t.Fatalf("invalid HTTP output: %s, %v", response.Body.String(), err)
				}
			} else if run.Status != hex.RunFailed || run.Operations[0].Status != hex.OperationFailed || run.Operations[0].Error != test.runError {
				t.Fatalf("automation did not enforce the action contract or map its error: %+v", run)
			}
			if strings.Contains(response.Body.String(), "private") || strings.Contains(run.Error, "private") {
				t.Fatal("an internal action failure leaked private details")
			}
			if test.name == "handler business error" && !strings.Contains(response.Body.String(), test.runError) {
				t.Fatal("HTTP lost the action's business-rule error")
			}
		})
	}
}

func TestSharedActionExecutionRejectsOversizedScriptInput(t *testing.T) {
	for _, dryRun := range []bool{false, true} {
		t.Run(fmt.Sprintf("dryRun=%t", dryRun), func(t *testing.T) {
			var calls atomic.Int32
			server, database := actionExecutionServer(t, func(context.Context, hex.ActionContext, json.RawMessage) (any, error) {
				calls.Add(1)
				return map[string]bool{"ok": true}, nil
			})
			value := strings.Repeat("x", hex.MaxActionInputBytes/2+64)
			document, err := json.Marshal(map[string]string{"value": value})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := database.Put(t.Context(), "demo", "source", "one", document, hex.WriteOptions{}); err != nil {
				t.Fatal(err)
			}
			input, err := json.Marshal(map[string]string{"value": value + value})
			if err != nil {
				t.Fatal(err)
			}
			requestAs(t, server, roleHeaders("editor"), http.MethodPost,
				"/api/sites/demo/actions/check", input, http.StatusBadRequest)
			run := runScriptAutomation(t, server, `export default async hex => {
			  const source = await hex.db.query("source", {limit:1});
			  return await hex.action("check", {value:source[0].data.value.repeat(2)});
			}`, dryRun)
			if calls.Load() != 0 {
				t.Fatal("oversized action input invoked the handler")
			}
			if run.Status != hex.RunFailed || !strings.Contains(run.Error, "input exceeds") {
				t.Fatalf("oversized script input was not rejected: %+v", run)
			}
		})
	}
}

func TestSharedActionExecutionValidatesDryRunInputWithoutInvocation(t *testing.T) {
	var calls atomic.Int32
	server, _ := actionExecutionServer(t, func(context.Context, hex.ActionContext, json.RawMessage) (any, error) {
		calls.Add(1)
		return nil, errors.New("dry runs must not invoke this write")
	})
	for _, input := range []string{`{}`, `{"value":123}`} {
		requestAs(t, server, roleHeaders("editor"), http.MethodPost,
			"/api/sites/demo/actions/check", []byte(input), http.StatusBadRequest)
		for _, dryRun := range []bool{false, true} {
			run := runScriptAutomation(t, server, "export default hex => hex.action('check', "+input+");", dryRun)
			if run.Status != hex.RunFailed || !strings.Contains(run.Error, "invalid input for action check") {
				t.Fatalf("invalid action input passed validation (dryRun=%t): %+v", dryRun, run)
			}
		}
	}
	run := runScriptAutomation(t, server, `export default hex => hex.action("check", {value:"valid"});`, true)
	if run.Status != hex.RunSucceeded || len(run.Operations) != 1 || run.Operations[0].Status != hex.OperationDryRun {
		t.Fatalf("valid dry run did not return a preview: %+v", run)
	}
	var preview struct {
		WouldRun string            `json:"wouldRun"`
		Input    map[string]string `json:"input"`
	}
	if err := json.Unmarshal(run.Output, &preview); err != nil || preview.WouldRun != "check" || preview.Input["value"] != "valid" {
		t.Fatalf("incorrect action preview: %s, %v", run.Output, err)
	}
	if calls.Load() != 0 {
		t.Fatal("invalid inputs or a valid dry run invoked the action handler")
	}
}

func TestSharedActionExecutionAcceptsInputAtSizeLimit(t *testing.T) {
	var calls atomic.Int32
	server, database := actionExecutionServer(t, func(context.Context, hex.ActionContext, json.RawMessage) (any, error) {
		calls.Add(1)
		return map[string]bool{"ok": true}, nil
	})
	value := strings.Repeat("x", (hex.MaxActionInputBytes-len(`{"value":""}`))/2)
	document, err := json.Marshal(map[string]string{"value": value})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.Put(t.Context(), "demo", "source", "one", document, hex.WriteOptions{}); err != nil {
		t.Fatal(err)
	}
	input, err := json.Marshal(map[string]string{"value": value + value})
	if err != nil {
		t.Fatal(err)
	}
	if len(input) != hex.MaxActionInputBytes {
		t.Fatalf("fixture has %d bytes, want %d", len(input), hex.MaxActionInputBytes)
	}
	requestAs(t, server, roleHeaders("editor"), http.MethodPost,
		"/api/sites/demo/actions/check", input, http.StatusOK)
	// The bridge also caps its complete request envelope at 1 MiB.
	bridgeValue := strings.Repeat("x", (hex.MaxActionInputBytes-len(`{"name":"check","input":{"value":""}}`))/2)
	bridgeDocument, err := json.Marshal(map[string]string{"value": bridgeValue})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.Put(t.Context(), "demo", "source", "one", bridgeDocument, hex.WriteOptions{}); err != nil {
		t.Fatal(err)
	}
	run := runScriptAutomation(t, server, `export default async hex => {
	  const source = await hex.db.query("source", {limit:1});
	  return await hex.action("check", {value:source[0].data.value.repeat(2)+"x"});
	}`, false)
	if run.Status != hex.RunSucceeded || calls.Load() != 2 {
		t.Fatalf("input at the limit was rejected: calls %d, run %+v", calls.Load(), run)
	}
}

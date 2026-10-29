package hex_test

import (
	"context"
	"encoding/json"
	"sync/atomic"
	"testing"
	"time"

	hex "github.com/crazycatviking/hex/server"
	"github.com/crazycatviking/hex/server/providers/easyauth"
	"github.com/crazycatviking/hex/server/providers/memory"
)

func TestWorkflowCompositionResumesCheckpointsAndChildren(t *testing.T) {
	ctx := context.Background()
	database := memory.NewDatabase()
	access := memory.NewAccessStore()
	automations := memory.NewAutomationStore()
	registry := &hex.ActionRegistry{}
	var calls atomic.Int32
	if err := registry.Register("demo", hex.Action{Definition: hex.ActionDefinition{Name: "counter", Description: "Count", InputSchema: json.RawMessage(`{"type":"object"}`), OutputSchema: json.RawMessage(`{"type":"integer"}`)}, Handler: func(context.Context, hex.ActionContext, json.RawMessage) (any, error) { return calls.Add(1), nil }}); err != nil {
		t.Fatal(err)
	}
	policy := hex.SiteAccess{Version: 2, Owners: []string{"user:owner"}, Admission: hex.Subjects{Owners: true}, Grants: map[string]hex.PermissionGrant{
		"workflow": {Kind: "workflow", Resource: "*", Operations: []string{"run"}, Subjects: hex.Subjects{Owners: true, Principals: []string{"automation:main"}}},
		"action":   {Kind: "action", Resource: "counter", Operations: []string{"run"}, Subjects: hex.Subjects{Principals: []string{"automation:main"}}},
	}}
	if err := access.PutSiteAccess(ctx, "demo", policy); err != nil {
		t.Fatal(err)
	}
	definitions := []hex.ScheduledAutomation{
		{Site: "demo", Automation: hex.Automation{Name: "main", Input: json.RawMessage(`{"type":"object","required":["value"]}`), Script: &hex.AutomationScript{Source: `export default async hex => {
 const count = await hex.workflow.step("count", () => hex.action("counter"));
 const child = await hex.workflow.start("child", hex.input, {idempotencyKey:hex.workflowId});
 const result = await hex.workflow.wait(child.id);
 return {count, value:result.value};
 }`}}},
		{Site: "demo", Automation: hex.Automation{Name: "child", Script: &hex.AutomationScript{Source: `export default async hex => { await hex.workflow.sleep("delay", 1); return hex.input; }`}}},
	}
	if err := automations.ReplaceSiteAutomations(ctx, "demo", definitions); err != nil {
		t.Fatal(err)
	}
	config := hex.Config{Database: database, Access: access, Automations: automations, Actions: registry, Identity: easyauth.Resolver{}}
	server := hex.New(config)
	headers := roleHeaders("owner")
	body := []byte(`{"name":"main","input":{"value":42},"idempotencyKey":"one"}`)
	response := requestAs(t, server, headers, "POST", "/api/sites/demo/workflows", body, 202)
	var started struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &started); err != nil {
		t.Fatal(err)
	}
	requestAs(t, server, headers, "POST", "/api/sites/demo/workflows", body, 202)
	requestAs(t, server, headers, "POST", "/api/sites/demo/workflows", []byte(`{"name":"main","input":{},"idempotencyKey":"bad"}`), 400)
	for range 8 {
		server = hex.New(config)
		if err := server.DispatchWorkflows(ctx); err != nil {
			t.Fatal(err)
		}
		jobs, err := database.List(ctx, ".hex-system", "workflows", hex.ListOptions{Limit: 100})
		if err != nil {
			t.Fatal(err)
		}
		for _, job := range jobs {
			if _, err := database.Mutate(ctx, ".hex-system", "workflows", job.ID, "", func(current *hex.Document) (json.RawMessage, error) {
				var workflow hex.Workflow
				if err := json.Unmarshal(current.Data, &workflow); err != nil {
					return nil, err
				}
				workflow.NextAttempt = time.Now().Add(-time.Second)
				for name, step := range workflow.Steps {
					if !step.WakeAt.IsZero() {
						step.WakeAt = time.Now().Add(-time.Second)
						workflow.Steps[name] = step
					}
				}
				return json.Marshal(workflow)
			}); err != nil {
				t.Fatal(err)
			}
		}
	}
	document, err := database.Get(ctx, ".hex-system", "workflows", started.ID)
	if err != nil {
		t.Fatal(err)
	}
	var workflow hex.Workflow
	if err := json.Unmarshal(document.Data, &workflow); err != nil {
		t.Fatal(err)
	}
	if workflow.Status != "succeeded" || string(workflow.Output) != `{"count":1,"value":42}` || calls.Load() != 1 {
		t.Fatalf("workflow failed: %+v calls=%d", workflow, calls.Load())
	}
	requestAs(t, server, principalHeaders("outsider"), "GET", "/api/sites/demo/workflows/"+started.ID, nil, 403)
}

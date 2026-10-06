package hex_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	hex "github.com/crazycatviking/hex/server"
	"github.com/crazycatviking/hex/server/providers/easyauth"
	"github.com/crazycatviking/hex/server/providers/memory"
	"golang.org/x/oauth2"
)

func runScriptAutomation(t *testing.T, server *hex.Server, source string, dryRun bool) hex.AutomationRun {
	t.Helper()
	definition, err := json.Marshal(hex.Automation{Name: "action-contract", Script: &hex.AutomationScript{File: "report.js", Source: source}})
	if err != nil {
		t.Fatal(err)
	}
	response := requestAs(t, server, roleHeaders("owner"), http.MethodPost,
		fmt.Sprintf("/api/hex/sites/demo/automations/test?dryRun=%t", dryRun), definition, http.StatusAccepted)
	var started hex.AutomationRun
	if err := json.Unmarshal(response.Body.Bytes(), &started); err != nil {
		t.Fatal(err)
	}
	return waitForRun(t, server, "/api/hex/sites/demo/automation-runs/"+started.ID)
}

func TestAutomationScriptOrchestratesSiteOperations(t *testing.T) {
	server, _, database, posted := setupAutomations(t, demoGrant)
	source := `export default async function(hex) {
	  const counts = await hex.call("defects.counts", {period: "7d"});
	  for (const [level, count] of Object.entries(counts.byLevel)) {
	    if (count < 3) continue;
	    await hex.call("chat.post", {text: level + ": " + count});
	  }
	  await hex.db.save("reports", {total: counts.total, literal: "{{ site }}"}, {id: "weekly"});
	  const reports = await hex.db.query("reports", {where: {literal: "{{ site }}"}});
	  hex.log("summary", {total: counts.total});
	  return {reports: reports.length, total: counts.total, site: hex.site};
	}`
	run := runScriptAutomation(t, server, source, false)
	if run.Status != hex.RunSucceeded || string(run.Output) != `{"reports":1,"total":7,"site":"demo"}` || len(posted.all()) != 1 {
		t.Fatalf("unexpected script run: %+v messages=%v", run, posted.all())
	}
	if len(run.Logs) != 1 || run.Logs[0].Message != "summary" || len(run.Operations) != 4 {
		t.Fatalf("missing script trace: %+v", run)
	}
	documents, err := database.List(t.Context(), "demo", "reports", hex.ListOptions{Limit: 10})
	if err != nil || len(documents) != 1 || documents[0].CreatedBy != "automation:action-contract" || !strings.Contains(string(documents[0].Data), `{{ site }}`) {
		t.Fatalf("script data was templated or had the wrong creator: %+v %v", documents, err)
	}
	page := requestAs(t, server, roleHeaders("owner"), "GET", "/api/hex/manage/sites/demo/automation-runs/"+run.ID, nil, 200).Body.String()
	if !strings.Contains(page, "Script logs") || !strings.Contains(page, "chat.post") || !strings.Contains(page, "summary") {
		t.Fatal("portal omitted script diagnostics")
	}
}

func TestAutomationScriptCannotBypassGrantsOrDryRun(t *testing.T) {
	for _, dryRun := range []bool{false, true} {
		server, _, _, posted := setupAutomations(t, hex.IntegrationGrant{Principal: "user:owner", Permissions: []string{"*"}})
		run := runScriptAutomation(t, server, `export default async hex => {
		  try { hex.run.dryRun = false; } catch {}
		  return await hex.call("chat.post", {text: "denied"});
		}`, dryRun)
		if run.Status != hex.RunFailed || !strings.Contains(run.Error, "chat.post permission") || len(posted.all()) != 0 {
			t.Fatalf("script bypassed site grants: %+v", run)
		}
	}
	server, _, database, posted := setupAutomations(t, demoGrant)
	run := runScriptAutomation(t, server, `export default async hex => {
	  await hex.call("defects.counts", {period:"7d"});
	  await hex.call("chat.post", {text:"dry"});
	  return await hex.db.save("reports", {total:7}, {id:"dry"});
	}`, true)
	documents, err := database.List(t.Context(), "demo", "reports", hex.ListOptions{Limit: 10})
	if err != nil || run.Status != hex.RunSucceeded || len(posted.all()) != 0 || len(documents) != 0 {
		t.Fatalf("dry script performed writes: %+v %v", run, err)
	}
	if len(run.Operations) != 3 || run.Operations[1].Status != hex.OperationDryRun || run.Operations[2].Status != hex.OperationDryRun {
		t.Fatalf("missing dry-run previews: %+v", run)
	}
	run = runScriptAutomation(t, server, `export default hex => hex.call("chat.post", {});`, true)
	if run.Status != hex.RunFailed || !strings.Contains(run.Error, "invalid input") {
		t.Fatalf("dry script bypassed endpoint schema: %+v", run)
	}
	run = runScriptAutomation(t, server, `export default hex => hex.db.query("reports", {site:"other"});`, false)
	if run.Status != hex.RunFailed || !strings.Contains(run.Error, "unknown field") {
		t.Fatalf("script accepted another site: %+v", run)
	}
}

func TestAutomationScriptActionsAndLimits(t *testing.T) {
	var calls atomic.Int32
	server, _ := actionExecutionServer(t, func(_ context.Context, caller hex.ActionContext, input json.RawMessage) (any, error) {
		calls.Add(1)
		if caller.Site != "demo" || caller.Role != "owner" || caller.Identity.ID != "automation:action-contract" {
			t.Errorf("wrong script identity: %+v", caller)
		}
		if string(input) != `{"value":"{{ site }}"}` {
			t.Errorf("script input was interpreted as a template: %s", input)
		}
		return map[string]bool{"ok": true}, nil
	})
	for _, dryRun := range []bool{true, false} {
		run := runScriptAutomation(t, server, `export default hex => hex.action("check", {value:"{{ site }}"});`, dryRun)
		if run.Status != hex.RunSucceeded {
			t.Fatalf("action failed: %+v", run)
		}
	}
	if calls.Load() != 1 {
		t.Fatal("dry script executed an action")
	}
	run := runScriptAutomation(t, server, `export default hex => hex.action("check", {value:1});`, true)
	if run.Status != hex.RunFailed || calls.Load() != 1 {
		t.Fatalf("action input contract bypassed: %+v", run)
	}
	run = runScriptAutomation(t, server, `export default hex => {
	  for (let i=0; i<110; i++) { try { hex.log("line", i); } catch {} }
	  return "caught";
	}`, false)
	if run.Status != hex.RunFailed || !strings.Contains(run.Error, "100-operation") || len(run.Logs) != 100 {
		t.Fatalf("caught exceptions bypassed operation limit: %+v", run)
	}
	for _, source := range []string{
		`export default hex => hex.db.query("reports", {limit:1001});`,
		`export default hex => hex.db.save("../other", {});`,
		`export default hex => { throw new Error("broken logic"); };`,
	} {
		run := runScriptAutomation(t, server, source, false)
		if run.Status != hex.RunFailed {
			t.Fatalf("invalid script succeeded: %+v", run)
		}
	}
}

func TestAutomationScriptMetadataAndRevision(t *testing.T) {
	server, _, _, _ := setupAutomations(t, demoGrant)
	definition := hex.Automation{Name: "metadata", Script: &hex.AutomationScript{Source: `export default hex => {
	  if ("steps" in hex || "input" in hex || "item" in hex || "index" in hex) throw new Error("legacy workflow state");
	  const prior = [2,3];
	  return prior.map((item,index) => ({value:item*2,index,prior,trigger:hex.run.trigger}));
	};`}}
	encoded, err := json.Marshal([]hex.Automation{definition})
	if err != nil {
		t.Fatal(err)
	}
	requestAs(t, server, roleHeaders("owner"), "PUT", "/api/hex/sites/demo/automations", encoded, 200)
	response := requestAs(t, server, roleHeaders("owner"), "POST", "/api/hex/sites/demo/automations/metadata/run", nil, 202)
	var started hex.AutomationRun
	if err := json.Unmarshal(response.Body.Bytes(), &started); err != nil {
		t.Fatal(err)
	}
	run := waitForRun(t, server, "/api/hex/sites/demo/automation-runs/"+started.ID)
	if run.Status != hex.RunSucceeded || run.Revision == "" || len(run.SourceHash) != 64 || string(run.Output) != `[{"value":4,"index":0,"prior":[2,3],"trigger":"manual"},{"value":6,"index":1,"prior":[2,3],"trigger":"manual"}]` {
		t.Fatalf("wrong script metadata: %+v", run)
	}
}

func TestAutomationMetadataRequiresScriptAndRejectsWorkflowFields(t *testing.T) {
	server, store, _, _ := setupAutomations(t, demoGrant)
	for _, definition := range []string{
		`{"name":"invalid"}`,
		`{"name":"invalid","script":null}`,
		`{"name":"invalid","disabled":true}`,
		`{"name":"invalid","script":{"file":"server-file.js"}}`,
		`{"name":"invalid","steps":[{"id":"read","call":"defects.counts"}]}`,
		`{"name":"invalid","script":{"source":"export default () => 42;"},"steps":[]}`,
		`{"name":"invalid","script":{"source":"export default () => 42;"},"input":{"minimum":5}}`,
		`{"name":"invalid","script":{"source":"export default () => 42;"},"if":"true"}`,
		`{"name":"invalid","script":{"source":"export default () => 42;"},"forEach":[1,2]}`,
	} {
		requestAs(t, server, roleHeaders("owner"), "POST", "/api/hex/sites/demo/automations/test", []byte(definition), 400)
		requestAs(t, server, roleHeaders("owner"), "PUT", "/api/hex/sites/demo/automations", []byte("["+definition+"]"), 400)
	}
	manifest := `{"files":[{"path":"index.html","size":1,"md5":"AAAAAAAAAAAAAAAAAAAAAA=="}],"automations":[{"name":"invalid","script":{"source":"export default () => 42;"},"steps":[]}]}`
	requestAs(t, server, roleHeaders("owner"), "POST", "/api/hex/sites/demo/publish", []byte(manifest), 400)
	definitions, err := store.ListSiteAutomations(t.Context(), "demo")
	if err != nil || len(definitions) != 0 {
		t.Fatalf("invalid metadata was deployed: %+v %v", definitions, err)
	}
}

func TestAutomationScriptQueriesUseLiteralTypedJSON(t *testing.T) {
	server, _, database, _ := setupAutomations(t, demoGrant)
	for id, data := range map[string]string{
		"number":  `{"value":1,"nested":{"label":"{{ site }}"}}`,
		"string":  `{"value":"1","nested":{"label":"{{ site }}"}}`,
		"null":    `{"value":null}`,
		"missing": `{}`,
	} {
		if _, err := database.Put(t.Context(), "demo", "values", id, json.RawMessage(data), hex.WriteOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	run := runScriptAutomation(t, server, `export default async hex => ({
	  number:(await hex.db.query("values",{where:{value:1,nested:{label:"{{ site }}"}}})).map(row=>row.id),
	  null:(await hex.db.query("values",{where:{value:null}})).map(row=>row.id)
	});`, false)
	if run.Status != hex.RunSucceeded || string(run.Output) != `{"number":["number"],"null":["null"]}` {
		t.Fatalf("query interpreted or coerced script data: %+v", run)
	}
}

func TestAutomationScriptApprovalsAndConnectedAccounts(t *testing.T) {
	registry := new(hex.IntegrationRegistry)
	if err := registry.RegisterConnector(hex.Connector{Name: "person", Title: "Person", OAuth2: oauth2.Config{ClientID: "client", Endpoint: oauth2.Endpoint{AuthURL: "https://example.test/authorize", TokenURL: "https://example.test/token"}}}); err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	endpoint := hex.IntegrationEndpoint{Name: "read", Description: "Read records.", InputSchema: objectSchema, OutputSchema: anyResult,
		Handler: func(context.Context, hex.IntegrationCall, json.RawMessage) (any, error) {
			calls.Add(1)
			return nil, nil
		},
	}
	for _, integration := range []hex.Integration{
		{Name: "approval", Title: "Approval", RequiresApproval: true, Endpoints: []hex.IntegrationEndpoint{endpoint}},
		{Name: "connected", Title: "Connected", Connector: "person", Endpoints: []hex.IntegrationEndpoint{endpoint}},
	} {
		if err := registry.Register(integration); err != nil {
			t.Fatal(err)
		}
	}
	access := memory.NewAccessStore()
	if err := access.PutSiteAccess(t.Context(), "demo", hex.SiteAccess{Owners: []string{"user:owner"}}); err != nil {
		t.Fatal(err)
	}
	store := memory.NewIntegrationStore()
	server := hex.New(hex.Config{Automations: memory.NewAutomationStore(), Access: access, Identity: easyauth.Resolver{}, Integrations: registry, IntegrationStore: store, IntegrationGrants: []hex.IntegrationGrant{{Principal: "site:demo", Permissions: []string{"*"}}}})
	run := runScriptAutomation(t, server, `export default hex => hex.call("approval.read");`, false)
	if run.Status != hex.RunFailed || !strings.Contains(run.Error, "approve") || calls.Load() != 0 {
		t.Fatalf("script bypassed approval: %+v", run)
	}
	if err := store.PutIntegrationApproval(t.Context(), hex.IntegrationApproval{Site: "demo", Integration: "approval", Status: hex.ApprovalApproved}); err != nil {
		t.Fatal(err)
	}
	run = runScriptAutomation(t, server, `export default hex => hex.call("approval.read");`, false)
	if run.Status != hex.RunSucceeded || calls.Load() != 1 {
		t.Fatalf("approved call failed: %+v", run)
	}
	run = runScriptAutomation(t, server, `export default hex => hex.call("connected.read");`, false)
	if run.Status != hex.RunFailed || !strings.Contains(run.Error, "connected account") || calls.Load() != 1 {
		t.Fatalf("script used a personal account: %+v", run)
	}
}

func TestAutomationScriptAIUsesSiteBudgetAndSimulatesTools(t *testing.T) {
	for _, granted := range []bool{false, true} {
		registry := new(hex.IntegrationRegistry)
		var writes atomic.Int32
		if err := registry.Register(hex.Integration{Name: "chat", Title: "Chat", Endpoints: []hex.IntegrationEndpoint{{Name: "post", Description: "Post.", Write: true, InputSchema: objectSchema, OutputSchema: anyResult,
			Handler: func(context.Context, hex.IntegrationCall, json.RawMessage) (any, error) {
				writes.Add(1)
				return map[string]bool{"posted": true}, nil
			},
		}}}); err != nil {
			t.Fatal(err)
		}
		provider := &scriptedProvider{turns: [][]hex.AIEvent{
			assistantTurn(hex.StopToolUse, hex.AIContent{Type: hex.ContentToolCall, ToolCallID: "call", Name: "chat__post", Input: json.RawMessage(`{}`)}),
			assistantTurn(hex.StopEndTurn, hex.AIContent{Type: hex.ContentText, Text: "preview"}),
		}}
		access := memory.NewAccessStore()
		if err := access.PutSiteAccess(t.Context(), "demo", hex.SiteAccess{Owners: []string{"user:owner"}}); err != nil {
			t.Fatal(err)
		}
		permissions := []string{"chat.post"}
		if granted {
			permissions = append(permissions, "ai")
		}
		usage := memory.NewAIUsageStore()
		server := hex.New(hex.Config{Automations: memory.NewAutomationStore(), Access: access, Identity: easyauth.Resolver{}, Integrations: registry, IntegrationGrants: []hex.IntegrationGrant{{Principal: "site:demo", Permissions: permissions}}, AI: &hex.AIConfig{Provider: provider}, AIUsage: usage})
		run := runScriptAutomation(t, server, `export default hex => hex.ai.complete({model:"general", prompt:"Post {{ site }}", tools:["chat.post"]});`, true)
		if !granted {
			if run.Status != hex.RunFailed || len(provider.requests) != 0 {
				t.Fatalf("script bypassed AI grants: %+v", run)
			}
			continue
		}
		if run.Status != hex.RunSucceeded || writes.Load() != 0 || len(provider.requests) != 2 {
			t.Fatalf("AI dry-run escaped: %+v", run)
		}
		if provider.requests[0].Messages[0].Content[0].Text != "Post {{ site }}" || !strings.Contains(provider.requests[1].Messages[2].Content[0].Text, "wouldCall") {
			t.Fatal("script AI templated its input or executed a write tool")
		}
		denied := runScriptAutomation(t, server, `export default hex => hex.ai.complete({model:"premium", prompt:"Use restricted model"});`, false)
		if denied.Status != hex.RunFailed || !strings.Contains(denied.Error, "not enabled") || len(provider.requests) != 2 {
			t.Fatalf("script bypassed model restriction: %+v", denied)
		}
		if err := usage.PutAIBudget(t.Context(), hex.AIBudget{Scope: hex.BudgetSite, Subject: "demo", Disabled: true}); err != nil {
			t.Fatal(err)
		}
		denied = runScriptAutomation(t, server, `export default hex => hex.ai.complete({model:"general", prompt:"Use disabled AI"});`, false)
		if denied.Status != hex.RunFailed || !strings.Contains(denied.Error, "turned off") || len(provider.requests) != 2 {
			t.Fatalf("script bypassed site AI budget: %+v", denied)
		}
	}
}

func TestAutomationAdmissionLimitsManualRuns(t *testing.T) {
	registry := new(hex.IntegrationRegistry)
	started := make(chan struct{}, 4)
	release := make(chan struct{})
	t.Cleanup(func() {
		select {
		case <-release:
		default:
			close(release)
		}
	})
	if err := registry.Register(hex.Integration{Name: "wait", Title: "Wait", Endpoints: []hex.IntegrationEndpoint{{
		Name: "read", Description: "Wait for a test signal.", InputSchema: json.RawMessage(`{"type":"object"}`), OutputSchema: anyResult,
		Handler: func(ctx context.Context, _ hex.IntegrationCall, _ json.RawMessage) (any, error) {
			started <- struct{}{}
			select {
			case <-release:
				return nil, nil
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		},
	}}}); err != nil {
		t.Fatal(err)
	}
	access := memory.NewAccessStore()
	if err := access.PutSiteAccess(t.Context(), "demo", hex.SiteAccess{Owners: []string{"user:owner"}}); err != nil {
		t.Fatal(err)
	}
	server := hex.New(hex.Config{Automations: memory.NewAutomationStore(), Access: access, Identity: easyauth.Resolver{}, Integrations: registry, IntegrationGrants: []hex.IntegrationGrant{{Principal: "site:demo", Permissions: []string{"wait.read"}}}})
	definition := []byte(`{"name":"wait","script":{"source":"export default hex => hex.call('wait.read');"}}`)
	var runs []hex.AutomationRun
	for range 4 {
		response := requestAs(t, server, roleHeaders("owner"), "POST", "/api/hex/sites/demo/automations/test", definition, 202)
		var run hex.AutomationRun
		if err := json.Unmarshal(response.Body.Bytes(), &run); err != nil {
			t.Fatal(err)
		}
		runs = append(runs, run)
	}
	for range 4 {
		select {
		case <-started:
		case <-time.After(time.Second):
			t.Fatal("run was not started")
		}
	}
	requestAs(t, server, roleHeaders("owner"), "POST", "/api/hex/sites/demo/automations/test", definition, http.StatusServiceUnavailable)
	close(release)
	for _, run := range runs {
		waitForRun(t, server, "/api/hex/sites/demo/automation-runs/"+run.ID)
	}
	requestAs(t, server, roleHeaders("owner"), "POST", "/api/hex/sites/demo/automations/test", definition, 202)
}

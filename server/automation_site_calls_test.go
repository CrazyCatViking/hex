package hex_test

import (
	"context"
	"encoding/json"
	"sync/atomic"
	"testing"

	hex "github.com/crazycatviking/hex/server"
	"github.com/crazycatviking/hex/server/providers/easyauth"
	"github.com/crazycatviking/hex/server/providers/memory"
)

func TestAutomationSiteCallsUseSiteAuthorityAndDryRun(t *testing.T) {
	t.Run("selected site", func(t *testing.T) { testAutomationSiteCalls(t, false) })
	t.Run("workspace public", func(t *testing.T) { testAutomationSiteCalls(t, true) })
}

func testAutomationSiteCalls(t *testing.T, public bool) {
	ctx := context.Background()
	policies := memory.NewAccessStore()
	database := memory.NewDatabase()
	registry := &hex.ActionRegistry{}
	var calls atomic.Int32
	if err := registry.Register("target", hex.Action{Definition: hex.ActionDefinition{Name: "submit", Description: "Submit", Version: 1, Exported: true, InputSchema: json.RawMessage(`{"type":"object"}`), OutputSchema: json.RawMessage(`{"type":"object"}`)}, Handler: func(ctx context.Context, caller hex.ActionContext, input json.RawMessage) (any, error) {
		if caller.Identity.Provider != "site" || caller.SourceSite != "demo" {
			t.Error("not site authority", caller.Identity)
		}
		calls.Add(1)
		return map[string]bool{"ok": true}, nil
	}}); err != nil {
		t.Fatal(err)
	}
	source := hex.SiteAccess{Version: 2, Owners: []string{"user:owner"}, Admission: hex.Subjects{SignedIn: true, Owners: true}}
	if err := policies.PutSiteAccess(ctx, "demo", source); err != nil {
		t.Fatal(err)
	}
	server := hex.New(hex.Config{Database: database, Access: policies, Identity: easyauth.Resolver{}, Actions: registry, Automations: memory.NewAutomationStore()})
	response := requestAs(t, server, roleHeaders("owner"), "GET", "/api/sites/demo/site-api/identity", nil, 200)
	var principal hex.Identity
	if err := json.Unmarshal(response.Body.Bytes(), &principal); err != nil {
		t.Fatal(err)
	}
	target := hex.SiteAccess{Version: 2, Grants: map[string]hex.PermissionGrant{
		"consumer": {Kind: "action-consumer", Resource: "submit", Operations: []string{"run"}, Subjects: hex.Subjects{Principals: []string{principal.ID}}},
		"action":   {Kind: "action", Resource: "submit", Operations: []string{"run"}, Subjects: hex.Subjects{Principals: []string{principal.ID}}},
	}}
	if public {
		for id, grant := range target.Grants {
			grant.Subjects = hex.Subjects{Workspace: true}
			target.Grants[id] = grant
		}
	}
	if err := policies.PutSiteAccess(ctx, "target", target); err != nil {
		t.Fatal(err)
	}
	script := `export default async hex => hex.sites.call("target", "submit", {}, {version:1,idempotencyKey:"business-1"});`
	dry := runScriptAutomation(t, server, script, true)
	if dry.Status != hex.RunSucceeded || calls.Load() != 0 {
		t.Fatal(dry)
	}
	for range 2 {
		run := runScriptAutomation(t, server, script, false)
		if run.Status != hex.RunSucceeded {
			t.Fatal(run)
		}
	}
	if calls.Load() != 1 {
		t.Fatal("repeated mutation", calls.Load())
	}
	target.Grants["action"] = hex.PermissionGrant{Kind: "action", Resource: "submit", Operations: []string{"run"}, Subjects: hex.Subjects{SignedIn: true}}
	if err := policies.PutSiteAccess(ctx, "target", target); err != nil {
		t.Fatal(err)
	}
	denied := runScriptAutomation(t, server, script, false)
	if denied.Status != hex.RunFailed {
		t.Fatal("site matched signedIn", denied)
	}
}

package hex_test

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	hex "github.com/crazycatviking/hex/server"
	"github.com/crazycatviking/hex/server/providers/easyauth"
	"github.com/crazycatviking/hex/server/providers/local"
	"github.com/crazycatviking/hex/server/providers/memory"
)

func TestActionConnectionModesPreservePublishedPermissions(t *testing.T) {
	files, err := local.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { files.Close() })
	policies, database := memory.NewAccessStore(), memory.NewDatabase()
	server := hex.New(hex.Config{Identity: easyauth.Resolver{}, Access: policies, Database: database, Sites: files, Files: files, Publisher: files, Automations: memory.NewAutomationStore()})
	owner := roleHeaders("owner")
	policy := hex.SiteAccess{Version: 2, Owners: []string{"user:owner"}, Admission: hex.Subjects{SignedIn: true}, Grants: map[string]hex.PermissionGrant{
		"legacy-data": {Kind: "collection", Resource: "expenses", Operations: []string{"read"}, Subjects: hex.Subjects{Principals: []string{"user:alice"}}},
	}}
	baseline := hex.CloneSiteAccess(policy)
	policy.Published = &baseline
	if err := policies.PutSiteAccess(t.Context(), "target", policy); err != nil {
		t.Fatal(err)
	}
	source := hex.SiteAccess{Version: 2, Owners: []string{"user:owner"}, Admission: hex.Subjects{SignedIn: true}}
	sourceBaseline := hex.CloneSiteAccess(source)
	source.Published = &sourceBaseline
	if err := policies.PutSiteAccess(t.Context(), "demo", source); err != nil {
		t.Fatal(err)
	}
	actions := `[{"name":"read-expenses","description":"Read expense summaries","operation":"list","collection":"expenses","version":1,"exported":true,"select":["project"],"output":{"type":"object"},"input":{"type":"object","properties":{"limit":{"type":"integer"}},"additionalProperties":false}},
	{"name":"edit-expense","description":"Edit an expense note","operation":"update","collection":"expenses","version":1,"exported":true,"input":{"type":"object","properties":{"id":{"type":"string"},"note":{"type":"string"},"secret":{"type":"string"}},"required":["id","note"],"additionalProperties":false}}]`
	publishWithActions(t, server, owner, "target", actions)
	publishWithActions(t, server, owner, "demo", `[]`)
	if err := policies.PutSiteAccess(t.Context(), "target", policy); err != nil {
		t.Fatal(err)
	}
	if err := policies.PutSiteAccess(t.Context(), "demo", source); err != nil {
		t.Fatal(err)
	}
	for id, project := range map[string]string{"a": "B", "b": "A", "c": "A"} {
		if _, err := database.Put(t.Context(), "target", "expenses", id, json.RawMessage(`{"project":"`+project+`","secret":"hidden"}`), hex.WriteOptions{Creator: "human"}); err != nil {
			t.Fatal(err)
		}
	}
	endpoint := "/api/hex/manage/sites/target/actions/read-expenses/access"
	load := func() map[string]any {
		var result map[string]any
		if err := json.Unmarshal(requestAs(t, server, owner, "GET", endpoint, nil, 200).Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		return result
	}
	initial := load()
	requestAs(t, server, principalHeaders("alice"), "GET", endpoint, nil, 403)
	changes := []map[string]any{{"source": "demo", "user": true, "site": true, "where": map[string]any{"field": "data.project", "op": "eq", "value": "A"}}}
	requestAs(t, server, owner, "PUT", endpoint, encode(t, map[string]any{"base": initial["base"], "connections": changes}), 200)
	saved, err := policies.GetSiteAccess(t.Context(), "target")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(saved.Published, policy.Published) || !reflect.DeepEqual(saved.Grants["legacy-data"], policy.Grants["legacy-data"]) || !reflect.DeepEqual(saved.Admission, policy.Admission) {
		t.Fatal("connection edit changed existing permissions or baseline")
	}
	// User mode never falls back to the app's broader execution grant.
	call := []byte(`{"target":"target","action":"read-expenses","version":1,"input":{"limit":1}}`)
	requestAs(t, server, principalHeaders("alice"), "POST", "/api/sites/demo/site-api/call", call, 403)
	run := runScriptAutomation(t, server, `export default hex => hex.sites.call("target","read-expenses",{limit:1},{version:1});`, false)
	if run.Status != hex.RunSucceeded || !strings.Contains(string(run.Output), `"id":"b"`) || strings.Contains(string(run.Output), "hidden") {
		t.Fatal("scoped site-mode read failed", run)
	}
	// Unchanged source publication preserves the portal group and execution grant.
	requestAs(t, server, owner, "POST", "/api/hex/sites/target/publish", encode(t, map[string]any{"files": manifest(map[string]string{"index.html": "updated"}), "actions": json.RawMessage(actions), "access": baseline}), 200)
	after, err := policies.GetSiteAccess(t.Context(), "target")
	if err != nil || !reflect.DeepEqual(saved.Grants, after.Grants) || !reflect.DeepEqual(saved.Roles, after.Roles) || !reflect.DeepEqual(saved.Bindings, after.Bindings) {
		t.Fatal("republishing lost portal app permissions", err)
	}
	mutationEndpoint := "/api/hex/manage/sites/target/actions/edit-expense/access"
	var mutationSettings map[string]any
	if err := json.Unmarshal(requestAs(t, server, owner, "GET", mutationEndpoint, nil, 200).Body.Bytes(), &mutationSettings); err != nil {
		t.Fatal(err)
	}
	requestAs(t, server, owner, "PUT", mutationEndpoint, encode(t, map[string]any{"base": mutationSettings["base"], "connections": []map[string]any{{"source": "demo", "site": true, "fields": []string{"note"}, "where": map[string]any{"field": "data.project", "op": "eq", "value": "A"}}}}), 200)
	for _, sample := range []struct {
		input   string
		key     string
		allowed bool
	}{
		{`{id:"b",note:"approved"}`, "valid", true},
		{`{id:"a",note:"outside scope"}`, "scope", false},
		{`{id:"b",note:"bad",secret:"changed"}`, "fields", false},
	} {
		run := runScriptAutomation(t, server, `export default hex => hex.sites.call("target","edit-expense",`+sample.input+`,{version:1,idempotencyKey:"`+sample.key+`"});`, false)
		if (run.Status == hex.RunSucceeded) != sample.allowed {
			t.Fatalf("site mutation %s: %+v", sample.key, run)
		}
	}
	record, err := database.Get(t.Context(), "target", "expenses", "b")
	if err != nil || !strings.Contains(string(record.Data), `"note":"approved"`) || !strings.Contains(string(record.Data), `"secret":"hidden"`) {
		t.Fatal("scoped mutation or denied-field isolation failed", err, string(record.Data))
	}
	requestAs(t, server, principalHeaders("alice"), "POST", "/api/sites/demo/site-api/call", []byte(`{"target":"target","action":"edit-expense","version":1,"input":{"id":"b","note":"escalated"},"idempotencyKey":"user"}`), 403)
	// Stale edits cannot resurrect a concurrent revocation.
	loaded := load()
	requestAs(t, server, owner, "PUT", endpoint, encode(t, map[string]any{"base": loaded["base"], "connections": []map[string]any{{"source": "demo", "user": true, "site": false}}}), 200)
	changes[0]["where"] = map[string]any{"field": "data.project", "op": "eq", "value": "B"}
	requestAs(t, server, owner, "PUT", endpoint, encode(t, map[string]any{"base": loaded["base"], "connections": changes}), 409)
	run = runScriptAutomation(t, server, `export default hex => hex.sites.call("target","read-expenses",{}, {version:1});`, false)
	if run.Status != hex.RunFailed {
		t.Fatal("revoked site mode still allowed", run)
	}
}

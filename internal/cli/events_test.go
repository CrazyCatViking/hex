package cli

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	hex "github.com/crazycatviking/hex/server"
	"github.com/crazycatviking/hex/server/providers/memory"
)

func TestEventsAndWorkflowsCLIAgainstPlatform(t *testing.T) {
	ctx := context.Background()
	directory := t.TempDir()
	access, automations := memory.NewAccessStore(), memory.NewAutomationStore()
	server := hex.New(hex.Config{Database: memory.NewDatabase(), Access: access, Automations: automations, Actions: &hex.ActionRegistry{}, Identity: hex.StaticIdentity{Identity: hex.Identity{ID: "alice"}}})
	platform := httptest.NewServer(server)
	defer platform.Close()
	saveReadProfile(t, directory, platform.URL)
	owner := hex.SiteAccess{Version: 2, Owners: []string{"user:alice"}, Admission: hex.Subjects{Owners: true}}
	producer := owner
	producer.Grants = map[string]hex.PermissionGrant{
		"publish":   {Kind: "event", Resource: "approved", Operations: []string{"publish"}, Subjects: hex.Subjects{Owners: true}},
		"subscribe": {Kind: "event", Resource: "approved", Operations: []string{"subscribe"}, Subjects: hex.Subjects{Workspace: true}},
	}
	consumer := owner
	consumer.Grants = map[string]hex.PermissionGrant{"start": {Kind: "workflow", Resource: "receive", Operations: []string{"run"}, Subjects: hex.Subjects{Owners: true}}}
	for site, policy := range map[string]hex.SiteAccess{"producer": producer, "consumer": consumer} {
		if err := access.PutSiteAccess(ctx, site, policy); err != nil {
			t.Fatal(err)
		}
	}
	receive := hex.ScheduledAutomation{Site: "consumer", Automation: hex.Automation{Name: "receive", Script: &hex.AutomationScript{Source: `export default async hex => hex.input;`}}, UpdatedAt: time.Now()}
	if err := automations.ReplaceSiteAutomations(ctx, "consumer", []hex.ScheduledAutomation{receive}); err != nil {
		t.Fatal(err)
	}
	for name, content := range map[string]string{
		"definition.json": `{"version":1,"description":"Approved order","schema":{"type":"object","properties":{"amount":{"type":"integer"}},"additionalProperties":false}}`,
		"where.json":      `{"field":"data.amount","op":"ne","value":0}`,
	} {
		if err := os.WriteFile(filepath.Join(directory, name), []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	run(t, directory, "events", "define", "--site", "producer", "approved", "--input", "@definition.json")
	if got := run(t, directory, "events", "types", "--site", "producer"); !strings.Contains(got, `"Approved order"`) {
		t.Fatal(got)
	}
	run(t, directory, "events", "subscribe", "--site", "consumer", "orders", "--producer", "producer", "--event", "approved", "--version", "1", "--automation", "receive", "--where", "@where.json")
	got := run(t, directory, "events", "subscriptions", "--site", "consumer")
	if !strings.Contains(got, `"allowed": true`) || !strings.Contains(got, `"stale": false`) || !strings.Contains(got, `"data.amount"`) {
		t.Fatal(got)
	}
	var event hex.DurableEvent
	if err := json.Unmarshal([]byte(run(t, directory, "events", "publish", "--site", "producer", "approved", "--version", "1", "--input", `{"amount":3}`, "--idempotency-key", "order-1")), &event); err != nil || event.ID == "" {
		t.Fatal(event, err)
	}
	if got := run(t, directory, "events", "inspect", "--site", "producer", event.ID); !strings.Contains(got, `"orders"`) {
		t.Fatal(got)
	}
	if got := run(t, directory, "events", "list", "--site", "producer"); !strings.Contains(got, event.ID) {
		t.Fatal(got)
	}
	var workflow struct{ ID, Status string }
	if err := json.Unmarshal([]byte(run(t, directory, "workflows", "start", "--site", "consumer", "receive", "--input", `{"order":"o1"}`, "--idempotency-key", "o1")), &workflow); err != nil || workflow.ID == "" {
		t.Fatal(workflow, err)
	}
	if got := run(t, directory, "workflows", "get", "--site", "consumer", workflow.ID); !strings.Contains(got, `"automation": "receive"`) {
		t.Fatal(got)
	}
	app, _ := testApp(t, directory)
	if err := app.Execute(ctx, []string{"events", "subscribe", "--site", "consumer", "orders"}, "test"); err == nil || !strings.Contains(err.Error(), "--producer") {
		t.Fatal("incomplete subscription accepted:", err)
	}
}

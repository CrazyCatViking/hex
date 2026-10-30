package hex_test

import (
	"context"
	"testing"
	"time"

	hex "github.com/crazycatviking/hex/server"
	"github.com/crazycatviking/hex/server/providers/easyauth"
	"github.com/crazycatviking/hex/server/providers/local"
	"github.com/crazycatviking/hex/server/providers/memory"
)

func TestEventCatalogVersionsAndConsumerSubscriptions(t *testing.T) {
	ctx := context.Background()
	store, err := local.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	access, automations := memory.NewAccessStore(), memory.NewAutomationStore()
	server := hex.New(hex.Config{Sites: store, Publisher: store, Database: memory.NewDatabase(), Access: access, Automations: automations, Actions: &hex.ActionRegistry{}, Identity: easyauth.Resolver{}, SiteBaseURL: "http://example.com"})
	owner, visitor := principalHeaders("owner"), principalHeaders("visitor")
	for _, site := range []string{"producer", "consumer"} {
		publishWithActions(t, server, owner, site, `[]`)
	}
	producer := hex.SiteAccess{Version: 2, Owners: []string{"user:owner"}, Admission: hex.Subjects{Owners: true}, Grants: map[string]hex.PermissionGrant{
		"subscribe": {Kind: "event", Resource: "approved", Operations: []string{"subscribe"}, Subjects: hex.Subjects{Workspace: true}},
	}}
	if err := access.PutSiteAccess(ctx, "producer", producer); err != nil {
		t.Fatal(err)
	}
	if err := access.PutSiteAccess(ctx, "consumer", hex.SiteAccess{Version: 2, Owners: []string{"user:owner"}, Admission: hex.Subjects{Owners: true}}); err != nil {
		t.Fatal(err)
	}
	deploy := func() {
		t.Helper()
		definition := hex.ScheduledAutomation{Site: "consumer", Automation: hex.Automation{Name: "receive", Script: &hex.AutomationScript{Source: `export default async hex => hex.event.id;`}}, UpdatedAt: time.Now()}
		if err := automations.ReplaceSiteAutomations(ctx, "consumer", []hex.ScheduledAutomation{definition}); err != nil {
			t.Fatal(err)
		}
	}
	deploy()
	for _, version := range []string{"2", "1"} {
		definition := `{"version":` + version + `,"description":"Approved order","schema":{"type":"object","properties":{"amount":{"type":"integer"}}}}`
		requestAs(t, server, owner, "PUT", "/api/sites/producer/events/types/approved", []byte(definition), 200)
	}
	var catalog []struct {
		Kind, Name, Sharing string
		Versions            []hex.EventType
	}
	decodeResponse(t, requestAs(t, server, visitor, "GET", "/api/hex/workspace-apis", nil, 200), &catalog)
	if len(catalog) != 1 || catalog[0].Kind != "event" || catalog[0].Sharing != "workspace" || len(catalog[0].Versions) != 2 || catalog[0].Versions[0].Version != 1 || len(catalog[0].Versions[1].Schema) == 0 {
		t.Fatalf("catalog = %+v", catalog)
	}
	requestAs(t, server, owner, "PUT", "/api/sites/consumer/events/subscriptions/orders", []byte(`{"producer":"producer","event":"approved","version":1,"automation":"receive"}`), 200)
	type listing struct {
		Items []struct {
			ID, Producer   string
			Allowed, Stale bool
		}
	}
	subscriptions := func() listing {
		t.Helper()
		var result listing
		decodeResponse(t, requestAs(t, server, owner, "GET", "/api/sites/consumer/events/subscriptions", nil, 200), &result)
		if len(result.Items) != 1 || result.Items[0].ID != "orders" || result.Items[0].Producer != "producer" {
			t.Fatalf("subscriptions = %+v", result)
		}
		return result
	}
	if got := subscriptions().Items[0]; !got.Allowed || got.Stale {
		t.Fatalf("fresh subscription = %+v", got)
	}
	deploy()
	if got := subscriptions().Items[0]; !got.Stale {
		t.Fatal("redeployed handler not reported as stale")
	}
	delete(producer.Grants, "subscribe")
	if err := access.PutSiteAccess(ctx, "producer", producer); err != nil {
		t.Fatal(err)
	}
	if got := subscriptions().Items[0]; got.Allowed {
		t.Fatal("revoked subscription still reported as allowed")
	}
	requestAs(t, server, visitor, "GET", "/api/sites/consumer/events/subscriptions", nil, 403)
}

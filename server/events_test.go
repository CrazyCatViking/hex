package hex_test

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	hex "github.com/crazycatviking/hex/server"
	"github.com/crazycatviking/hex/server/providers/easyauth"
	"github.com/crazycatviking/hex/server/providers/memory"
)

func TestDurableEventsFanoutRecoveryRevocationAndReplay(t *testing.T) {
	ctx := context.Background()
	database := memory.NewDatabase()
	access := memory.NewAccessStore()
	automations := memory.NewAutomationStore()
	config := hex.Config{Database: database, Access: access, Automations: automations, Actions: &hex.ActionRegistry{}, Identity: easyauth.Resolver{}}
	server := hex.New(config)
	producer := hex.SiteAccess{Version: 2, Owners: []string{"user:owner"}, Admission: hex.Subjects{Owners: true}, Grants: map[string]hex.PermissionGrant{
		"publish": {Kind: "event", Resource: "approved", Operations: []string{"publish"}, Subjects: hex.Subjects{Owners: true}},
	}}
	consumer := hex.SiteAccess{Version: 2, Owners: []string{"user:owner"}, Admission: hex.Subjects{Owners: true}, Grants: map[string]hex.PermissionGrant{
		"save": {Kind: "collection", Resource: "received", Operations: []string{"create", "update"}, Subjects: hex.Subjects{Principals: []string{"automation:receive"}}},
	}}
	for site, policy := range map[string]hex.SiteAccess{"producer": producer, "consumer": consumer} {
		if err := access.PutSiteAccess(ctx, site, policy); err != nil {
			t.Fatal(err)
		}
	}
	headers := roleHeaders("owner")
	response := requestAs(t, server, headers, "GET", "/api/sites/consumer/site-api/identity", nil, 200)
	var principal hex.Identity
	if err := json.Unmarshal(response.Body.Bytes(), &principal); err != nil {
		t.Fatal(err)
	}
	producer.Grants["subscribe"] = hex.PermissionGrant{Kind: "event", Resource: "approved", Operations: []string{"subscribe"}, Subjects: hex.Subjects{Principals: []string{principal.ID}}}
	if err := access.PutSiteAccess(ctx, "producer", producer); err != nil {
		t.Fatal(err)
	}
	definition := hex.ScheduledAutomation{Site: "consumer", Automation: hex.Automation{Name: "receive", Script: &hex.AutomationScript{Source: `export default async hex => { return await hex.db.save("received", {amount:hex.event.data.amount}, {id:hex.event.id}); }`}}, UpdatedAt: time.Now()}
	if err := automations.ReplaceSiteAutomations(ctx, "consumer", []hex.ScheduledAutomation{definition}); err != nil {
		t.Fatal(err)
	}
	schema := []byte(`{"version":1,"description":"Approved order","schema":{"type":"object","required":["amount"],"properties":{"amount":{"type":"integer"}},"additionalProperties":false}}`)
	requestAs(t, server, headers, "PUT", "/api/sites/producer/events/types/approved", schema, 200)
	subscription := []byte(`{"producer":"producer","event":"approved","version":1,"automation":"receive"}`)
	requestAs(t, server, headers, "PUT", "/api/sites/consumer/events/subscriptions/orders", subscription, 200)
	publish := func(key string) hex.DurableEvent {
		t.Helper()
		body := []byte(`{"version":1,"data":{"amount":12},"idempotencyKey":"` + key + `"}`)
		response := requestAs(t, server, headers, "POST", "/api/sites/producer/events/publish/approved", body, 202)
		var event hex.DurableEvent
		if err := json.Unmarshal(response.Body.Bytes(), &event); err != nil {
			t.Fatal(err)
		}
		return event
	}
	event := publish("one")
	if publish("one").ID != event.ID {
		t.Fatal("publication not deduplicated")
	}
	requestAs(t, server, headers, "POST", "/api/sites/producer/events/publish/approved", []byte(`{"version":1,"data":{"amount":13},"idempotencyKey":"one"}`), 409)
	requestAs(t, server, headers, "POST", "/api/sites/producer/events/publish/approved", []byte(`{"version":1,"data":{"secret":13},"idempotencyKey":"bad"}`), 400)
	// Independent instances race on the same durable queue after restart.
	var workers sync.WaitGroup
	for range 4 {
		workers.Go(func() {
			if err := hex.New(config).DispatchEvents(ctx); err != nil {
				t.Error(err)
			}
		})
	}
	workers.Wait()
	runs, err := automations.ListAutomationRuns(ctx, "consumer", "receive", 50)
	if err != nil || len(runs) != 1 || runs[0].Status != hex.RunSucceeded {
		t.Fatal(runs, err)
	}
	if _, err := database.Get(ctx, "consumer", "received", event.ID); err != nil {
		t.Fatal(err)
	}
	blocked := publish("two")
	delete(producer.Grants, "subscribe")
	if err := access.PutSiteAccess(ctx, "producer", producer); err != nil {
		t.Fatal(err)
	}
	if err := server.DispatchEvents(ctx); err != nil {
		t.Fatal(err)
	}
	response = requestAs(t, server, headers, "GET", "/api/sites/producer/events/records/"+blocked.ID, nil, 200)
	if !strings.Contains(response.Body.String(), `"status":"dead"`) {
		t.Fatal(response.Body.String())
	}
	requestAs(t, server, headers, "POST", "/api/sites/consumer/events/records/"+blocked.ID+"/replay/orders", []byte(`{}`), 403)
	producer.Grants["subscribe"] = hex.PermissionGrant{Kind: "event", Resource: "approved", Operations: []string{"subscribe"}, Subjects: hex.Subjects{Principals: []string{principal.ID}}}
	if err := access.PutSiteAccess(ctx, "producer", producer); err != nil {
		t.Fatal(err)
	}
	requestAs(t, server, headers, "POST", "/api/sites/consumer/events/records/"+blocked.ID+"/replay/orders", []byte(`{}`), 202)
	if err := server.DispatchEvents(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Get(ctx, "consumer", "received", blocked.ID); err != nil {
		t.Fatal(err)
	}
}

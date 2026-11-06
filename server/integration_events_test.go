package hex_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	hex "github.com/crazycatviking/hex/server"
	"github.com/crazycatviking/hex/server/providers/easyauth"
)

const chatMessageSchemaText = `{
	"type": "object",
	"required": ["channel", "text"],
	"properties": {"channel": {"type": "string"}, "text": {"type": "string"}},
	"additionalProperties": false
}`

var chatMessageSchema = json.RawMessage(chatMessageSchemaText)

func chatEventIntegration(t *testing.T) *hex.IntegrationRegistry {
	t.Helper()
	registry := new(hex.IntegrationRegistry)
	err := registry.Register(hex.Integration{
		Name: "chat", Title: "Chat",
		Events: []hex.IntegrationEvent{{
			Name: "message", Version: 1, Description: "A message in a channel.", Schema: chatMessageSchema,
			// Only the receiver site may see messages in the secret channel.
			Visible: func(site string, data json.RawMessage) bool {
				return !strings.Contains(string(data), `"secret"`) || site == "receiver"
			},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return registry
}

func TestIntegrationEventRegistration(t *testing.T) {
	registry := new(hex.IntegrationRegistry)
	handler := func(context.Context, hex.IntegrationCall, json.RawMessage) (any, error) { return nil, nil }
	endpoint := hex.IntegrationEndpoint{Name: "message", Description: "Send.", InputSchema: json.RawMessage(`{"type":"object"}`), OutputSchema: json.RawMessage(`{"type":"object"}`), Handler: handler}
	cases := map[string]hex.Integration{
		"collides with endpoint": {Name: "a", Title: "A", Endpoints: []hex.IntegrationEndpoint{endpoint}, Events: []hex.IntegrationEvent{{Name: "message", Version: 1, Description: "x", Schema: chatMessageSchema}}},
		"no version":             {Name: "b", Title: "B", Events: []hex.IntegrationEvent{{Name: "message", Description: "x", Schema: chatMessageSchema}}},
		"no schema":              {Name: "c", Title: "C", Events: []hex.IntegrationEvent{{Name: "message", Version: 1, Description: "x"}}},
		"duplicate":              {Name: "d", Title: "D", Events: []hex.IntegrationEvent{{Name: "m", Version: 1, Description: "x", Schema: chatMessageSchema}, {Name: "m", Version: 1, Description: "y", Schema: chatMessageSchema}}},
	}
	for name, integration := range cases {
		if err := registry.Register(integration); err == nil {
			t.Errorf("%s: registered", name)
		}
	}
	if err := registry.Register(hex.Integration{Name: "e", Title: "E", Events: []hex.IntegrationEvent{
		{Name: "m", Version: 1, Description: "x", Schema: chatMessageSchema},
		{Name: "m", Version: 2, Description: "y", Schema: chatMessageSchema},
	}}); err != nil {
		t.Fatalf("events-only integration with two versions: %v", err)
	}
}

func TestIntegrationEventsReachPermittedSubscribers(t *testing.T) {
	ctx := context.Background()
	database, access, automations := durableTestStores(t)
	config := hex.Config{
		Database: database, Access: access, Automations: automations, Actions: &hex.ActionRegistry{}, Identity: easyauth.Resolver{},
		Integrations: chatEventIntegration(t),
		IntegrationGrants: []hex.IntegrationGrant{
			{Principal: "site:receiver", Permissions: []string{"chat.events.message"}},
			{Principal: "site:outsider", Permissions: []string{"chat.*"}},
		},
	}
	server := hex.New(config)
	headers := roleHeaders("owner")

	receiverPolicy := func(withIntegrationGrant bool) hex.SiteAccess {
		policy := hex.SiteAccess{Version: 2, Owners: []string{"user:owner"}, Admission: hex.Subjects{Owners: true}, Grants: map[string]hex.PermissionGrant{
			"save": {Kind: "collection", Resource: "messages", Operations: []string{"create", "update"}, Subjects: hex.Subjects{Principals: []string{"automation:on-message"}}},
		}}
		if withIntegrationGrant {
			policy.Grants["chat"] = hex.PermissionGrant{Kind: "integration", Resource: "chat.message", Operations: []string{"read"}, Subjects: hex.Subjects{Principals: []string{"automation:on-message"}}}
		}
		return policy
	}
	sites := map[string]hex.SiteAccess{"receiver": receiverPolicy(false), "outsider": receiverPolicy(true), "ungranted": receiverPolicy(true)}
	script := `export default async hex => { await hex.db.save("messages", {site: hex.event.site, text: hex.event.data.text}, {id: hex.event.id}); }`
	for site, policy := range sites {
		if err := access.PutSiteAccess(ctx, site, policy); err != nil {
			t.Fatal(err)
		}
		definition := hex.ScheduledAutomation{Site: site, Automation: hex.Automation{Name: "on-message", Script: &hex.AutomationScript{Source: script}}, UpdatedAt: time.Now()}
		if err := automations.ReplaceSiteAutomations(ctx, site, []hex.ScheduledAutomation{definition}); err != nil {
			t.Fatal(err)
		}
	}

	// Nobody subscribes yet: nothing is stored.
	unreceived, err := server.PublishIntegrationEvent(ctx, "chat", "message", 1, "early", map[string]string{"channel": "general", "text": "hi"})
	if err != nil || unreceived.ID != "" {
		t.Fatalf("unreceived event: %+v %v", unreceived, err)
	}

	subscription := []byte(`{"producer":"integration:chat","event":"message","version":1,"automation":"on-message"}`)
	// The receiver's v2 policy lacks the integration grant.
	response := requestAs(t, server, headers, "PUT", "/api/sites/receiver/events/subscriptions/chat", subscription, 403)
	if !strings.Contains(response.Body.String(), "chat.message") {
		t.Fatal(response.Body.String())
	}
	if err := access.PutSiteAccess(ctx, "receiver", receiverPolicy(true)); err != nil {
		t.Fatal(err)
	}
	requestAs(t, server, headers, "PUT", "/api/sites/receiver/events/subscriptions/chat", subscription, 200)
	requestAs(t, server, headers, "PUT", "/api/sites/outsider/events/subscriptions/chat", subscription, 200)
	// No platform grant for this site.
	response = requestAs(t, server, headers, "PUT", "/api/sites/ungranted/events/subscriptions/chat", subscription, 403)
	if !strings.Contains(response.Body.String(), "chat.events.message") {
		t.Fatal(response.Body.String())
	}
	requestAs(t, server, headers, "PUT", "/api/sites/receiver/events/subscriptions/nope", []byte(`{"producer":"integration:chat","event":"nope","version":1,"automation":"on-message"}`), 404)

	if _, err := server.PublishIntegrationEvent(ctx, "chat", "message", 1, "bad", map[string]any{"channel": "general"}); err == nil {
		t.Fatal("published data that does not match the schema")
	}
	public, err := server.PublishIntegrationEvent(ctx, "chat", "message", 1, "m1", map[string]string{"channel": "general", "text": "hello"})
	if err != nil {
		t.Fatal(err)
	}
	if again, err := server.PublishIntegrationEvent(ctx, "chat", "message", 1, "m1", map[string]string{"channel": "general", "text": "hello"}); err != nil || again.ID != public.ID {
		t.Fatalf("provider retry not deduplicated: %+v %v", again, err)
	}
	secret, err := server.PublishIntegrationEvent(ctx, "chat", "message", 1, "m2", json.RawMessage(`{"channel":"secret","text":"psst"}`))
	if err != nil {
		t.Fatal(err)
	}
	if public.Site != "integration:chat" || public.Actor != "integration:chat" {
		t.Fatalf("producer: %+v", public)
	}
	for range 3 {
		if err := server.DispatchEvents(ctx); err != nil {
			t.Fatal(err)
		}
	}

	for _, check := range []struct {
		site, event string
		want        bool
	}{
		{"receiver", public.ID, true},
		{"receiver", secret.ID, true},
		{"outsider", public.ID, true},
		{"outsider", secret.ID, false},
	} {
		document, err := database.Get(ctx, check.site, "messages", check.event)
		if got := err == nil; got != check.want {
			t.Errorf("%s received %s: %v (%v)", check.site, check.event, got, err)
		}
		if err == nil && !strings.Contains(string(document.Data), `"site":"integration:chat"`) {
			t.Errorf("hex.event.site: %s", document.Data)
		}
		if err != nil && !errors.Is(err, hex.ErrNotFound) {
			t.Fatal(err)
		}
	}

	// Consumers inspect their deliveries of integration events.
	response = requestAs(t, server, headers, "GET", "/api/sites/outsider/events/records/"+public.ID, nil, 200)
	if strings.Contains(response.Body.String(), `"site":"receiver"`) || !strings.Contains(response.Body.String(), `"status":"succeeded"`) {
		t.Fatal(response.Body.String())
	}

	// Revoking the platform grant stops later deliveries.
	config.IntegrationGrants = config.IntegrationGrants[:1]
	revoked := hex.New(config)
	later, err := revoked.PublishIntegrationEvent(ctx, "chat", "message", 1, "m3", map[string]string{"channel": "general", "text": "later"})
	if err != nil {
		t.Fatal(err)
	}
	if err := revoked.DispatchEvents(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Get(ctx, "outsider", "messages", later.ID); !errors.Is(err, hex.ErrNotFound) {
		t.Fatalf("delivered after revocation: %v", err)
	}
	if _, err := database.Get(ctx, "receiver", "messages", later.ID); err != nil {
		t.Fatal(err)
	}

	// Sites discover events and whether their automations may receive them.
	response = requestAs(t, server, headers, "GET", "/api/sites/receiver/integrations", nil, 200)
	var integrations []hex.SiteIntegration
	if err := json.Unmarshal(response.Body.Bytes(), &integrations); err != nil {
		t.Fatal(err)
	}
	if len(integrations) != 1 || len(integrations[0].Events) != 1 || !integrations[0].Events[0].Allowed || integrations[0].Events[0].Producer != "integration:chat" {
		t.Fatalf("%+v", integrations)
	}
	response = requestAs(t, server, headers, "GET", "/api/sites/ungranted/integrations", nil, 200)
	if strings.Contains(response.Body.String(), `"allowed":true`) {
		t.Fatal(response.Body.String())
	}

	// Reading stored topics must never write into the integration's own
	// schema bytes (json.RawMessage reuses its backing array).
	if string(chatMessageSchema) != chatMessageSchemaText {
		t.Fatalf("registered schema was overwritten: %s", chatMessageSchema)
	}
}

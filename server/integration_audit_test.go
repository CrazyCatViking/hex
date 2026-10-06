package hex_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	hex "github.com/crazycatviking/hex/server"
	"github.com/crazycatviking/hex/server/providers/easyauth"
	"github.com/crazycatviking/hex/server/providers/memory"
)

func ticketIntegration(calls *atomic.Int32) hex.Integration {
	return hex.Integration{
		Name: "helpdesk", Title: "Helpdesk", Audit: true,
		Endpoints: []hex.IntegrationEndpoint{
			{
				Name: "tickets", Description: "List tickets.", CacheTTL: time.Minute,
				InputSchema: objectSchema, OutputSchema: anyResult,
				Handler: func(context.Context, hex.IntegrationCall, json.RawMessage) (any, error) {
					calls.Add(1)
					return map[string]any{"tickets": []map[string]string{{"id": "1"}, {"id": "2"}, {"id": "1"}}}, nil
				},
				AuditRecords: func(output json.RawMessage) []string {
					var result struct {
						Tickets []struct {
							ID string `json:"id"`
						} `json:"tickets"`
					}
					if err := json.Unmarshal(output, &result); err != nil {
						return nil
					}
					ids := make([]string, 0, len(result.Tickets))
					for _, ticket := range result.Tickets {
						ids = append(ids, "ticket:"+ticket.ID)
					}
					return ids
				},
			},
			{
				Name: "broken", Description: "Always fails.", InputSchema: objectSchema, OutputSchema: anyResult,
				Handler: func(context.Context, hex.IntegrationCall, json.RawMessage) (any, error) {
					return nil, errors.New("helpdesk is down")
				},
			},
		},
	}
}

func chatIntegration() hex.Integration {
	return hex.Integration{
		Name: "chat", Title: "Chat",
		Endpoints: []hex.IntegrationEndpoint{{
			Name: "channels", Description: "List channels.", InputSchema: objectSchema, OutputSchema: anyResult,
			Handler: func(context.Context, hex.IntegrationCall, json.RawMessage) (any, error) {
				return map[string]any{"channels": []string{}}, nil
			},
		}},
	}
}

func setupAudit(t *testing.T, audit hex.IntegrationAuditStore, integrations ...hex.Integration) *hex.Server {
	t.Helper()
	registry := new(hex.IntegrationRegistry)
	for _, integration := range integrations {
		if err := registry.Register(integration); err != nil {
			t.Fatal(err)
		}
	}
	access := memory.NewAccessStore()
	if err := access.PutSiteAccess(context.Background(), "demo", hex.SiteAccess{
		Owners: []string{"user:owner"}, Viewers: []string{"user:alice", "user:bob"},
	}); err != nil {
		t.Fatal(err)
	}
	return hex.New(hex.Config{
		Identity: easyauth.Resolver{}, Access: access, AdminGroups: []string{"admin-group"},
		SiteBaseURL: "http://example.com", Integrations: registry,
		IntegrationGrants: []hex.IntegrationGrant{{Principal: "user:alice", Permissions: []string{"*"}}, {Principal: "user:bob", Permissions: []string{"*"}}},
		IntegrationStore:  memory.NewIntegrationStore(), IntegrationAudit: audit,
	})
}

func listAudit(t *testing.T, store hex.IntegrationAuditStore, filter hex.IntegrationAuditFilter) []hex.IntegrationAuditRecord {
	t.Helper()
	records, err := store.ListIntegrationAudit(context.Background(), filter)
	if err != nil {
		t.Fatal(err)
	}
	return records
}

func TestAuditedIntegrationCallsAreRecorded(t *testing.T) {
	var calls atomic.Int32
	store := memory.NewIntegrationAuditStore()
	server := setupAudit(t, store, ticketIntegration(&calls), chatIntegration())
	tickets := "/api/sites/demo/integrations/helpdesk/tickets"

	requestAs(t, server, roleHeaders("alice"), "POST", tickets, []byte(`{ "status": "open" }`), 200)
	cached := requestAs(t, server, roleHeaders("bob"), "POST", tickets, []byte(`{"status":"open"}`), 200)
	if cached.Header().Get("X-Hex-Cache") != "hit" || calls.Load() != 1 {
		t.Fatal("the second call was not served from the cache")
	}
	requestAs(t, server, roleHeaders("alice"), "POST", "/api/sites/demo/integrations/helpdesk/broken", nil, 502)
	requestAs(t, server, roleHeaders("alice"), "POST", "/api/sites/demo/integrations/chat/channels", nil, 200)
	requestAs(t, server, roleHeaders("stranger"), "POST", tickets, nil, 403)

	records := listAudit(t, store, hex.IntegrationAuditFilter{})
	if len(records) != 3 {
		t.Fatalf("expected three audit records, got %+v", records)
	}
	failed, hit, fresh := records[0], records[1], records[2]
	if fresh.Caller != "user:alice" || fresh.CallerName != "alice" || fresh.Site != "demo" || fresh.Integration != "helpdesk" ||
		fresh.Endpoint != "tickets" || fresh.Cached || fresh.Failed || string(fresh.Input) != `{"status":"open"}` ||
		strings.Join(fresh.Records, ",") != "ticket:1,ticket:2" || fresh.ID == "" || fresh.At.IsZero() {
		t.Fatalf("unexpected record of the fresh call %+v", fresh)
	}
	if hit.Caller != "user:bob" || !hit.Cached || strings.Join(hit.Records, ",") != "ticket:1,ticket:2" {
		t.Fatalf("unexpected record of the cache hit %+v", hit)
	}
	if failed.Endpoint != "broken" || !failed.Failed || len(failed.Records) != 0 || string(failed.Input) != `{}` {
		t.Fatalf("unexpected record of the failed call %+v", failed)
	}

	byRecord := listAudit(t, store, hex.IntegrationAuditFilter{Record: "ticket:2", Caller: "user:bob"})
	if len(byRecord) != 1 || byRecord[0].ID != hit.ID {
		t.Fatalf("unexpected records of ticket 2 for bob %+v", byRecord)
	}
}

// failingAuditStore cannot write records.
type failingAuditStore struct {
	*memory.IntegrationAuditStore
}

func (failingAuditStore) RecordIntegrationAudit(context.Context, hex.IntegrationAuditRecord) error {
	return errors.New("the database is gone")
}

func TestAuditedIntegrationCallsFailClosed(t *testing.T) {
	var calls atomic.Int32
	server := setupAudit(t, failingAuditStore{memory.NewIntegrationAuditStore()}, ticketIntegration(&calls), chatIntegration())
	tickets := "/api/sites/demo/integrations/helpdesk/tickets"

	response := requestAs(t, server, roleHeaders("alice"), "POST", tickets, nil, 503)
	if strings.Contains(response.Body.String(), "ticket") || calls.Load() != 1 {
		t.Fatalf("an unrecorded call returned data: %s", response.Body.String())
	}
	requestAs(t, server, roleHeaders("alice"), "POST", tickets, nil, 503)
	if calls.Load() != 2 {
		t.Fatal("an unrecorded result was cached")
	}
	requestAs(t, server, roleHeaders("alice"), "POST", "/api/sites/demo/integrations/chat/channels", nil, 200)

	unconfigured := setupAudit(t, nil, ticketIntegration(&calls), chatIntegration())
	missing := requestAs(t, unconfigured, roleHeaders("alice"), "POST", tickets, nil, 503)
	if !strings.Contains(missing.Body.String(), "cannot be audited") || calls.Load() != 2 {
		t.Fatalf("an audited integration ran without an audit store: %s", missing.Body.String())
	}
	requestAs(t, unconfigured, roleHeaders("alice"), "POST", "/api/sites/demo/integrations/chat/channels", nil, 200)
	requestAs(t, unconfigured, principalHeaders("admin", "admin-group"), "GET", "/admin/integration-audit", nil, 404)
}

func TestIntegrationAuditAdminAccess(t *testing.T) {
	var calls atomic.Int32
	store := memory.NewIntegrationAuditStore()
	server := setupAudit(t, store, ticketIntegration(&calls), chatIntegration())
	old := hex.IntegrationAuditRecord{
		ID: "old", At: time.Date(2026, time.January, 5, 12, 0, 0, 0, time.UTC), Site: "legacy", Integration: "helpdesk",
		Endpoint: "tickets", Caller: "automation:legacy/sync", CallerName: "automation:legacy/sync",
		Input: json.RawMessage(`{}`), Records: []string{"ticket:9"},
	}
	if err := store.RecordIntegrationAudit(context.Background(), old); err != nil {
		t.Fatal(err)
	}
	requestAs(t, server, roleHeaders("alice"), "POST", "/api/sites/demo/integrations/helpdesk/tickets", nil, 200)
	requestAs(t, server, roleHeaders("bob"), "POST", "/api/sites/demo/integrations/helpdesk/broken", nil, 502)
	admin := principalHeaders("admin", "admin-group")
	api := "/api/hex/manage/integration-audit"

	requestAs(t, server, roleHeaders("alice"), "GET", api, nil, 403)
	requestAs(t, server, roleHeaders("alice"), "GET", "/admin/integration-audit", nil, 403)
	requestAs(t, server, nil, "GET", api, nil, 401)

	for query, want := range map[string][]string{
		"":                                   {"bob", "alice", "old"},
		"?limit=1":                           {"bob"},
		"?caller=user:alice":                 {"alice"},
		"?caller=alice":                      {"alice"},
		"?site=legacy":                       {"old"},
		"?endpoint=helpdesk.broken":          {"bob"},
		"?endpoint=helpdesk":                 {"bob", "alice", "old"},
		"?endpoint=chat.*":                   {},
		"?record=ticket:9":                   {"old"},
		"?since=2026-01-05&until=2026-01-05": {"old"},
		"?until=2026-01-05T12:00:00Z":        {},
	} {
		var records []hex.IntegrationAuditRecord
		if err := json.Unmarshal(requestAs(t, server, admin, "GET", api+query, nil, 200).Body.Bytes(), &records); err != nil {
			t.Fatal(err)
		}
		var got []string
		for _, record := range records {
			switch {
			case record.ID == "old":
				got = append(got, "old")
			default:
				got = append(got, strings.TrimPrefix(record.Caller, "user:"))
			}
		}
		if strings.Join(got, ",") != strings.Join(want, ",") {
			t.Errorf("%s: got %v, want %v", query, got, want)
		}
	}
	requestAs(t, server, admin, "GET", api+"?limit=1001", nil, 400)
	requestAs(t, server, admin, "GET", api+"?since=yesterday", nil, 400)

	page := requestAs(t, server, admin, "GET", "/admin/integration-audit?endpoint=helpdesk.tickets", nil, 200).Body.String()
	for _, want := range []string{"Integration audit", "helpdesk.tickets", "ticket:1", "ticket:9", "sync", "Automation"} {
		if !strings.Contains(page, want) {
			t.Errorf("the audit page lacks %q", want)
		}
	}
	if strings.Contains(page, ">Failed<") {
		t.Error("the audit page ignored the endpoint filter")
	}
	invalid := requestAs(t, server, admin, "GET", "/admin/integration-audit?since=soon", nil, 200).Body.String()
	if !strings.Contains(invalid, "banner-error") {
		t.Error("the audit page did not explain an invalid filter")
	}
	export := requestAs(t, server, admin, "GET", "/admin/integration-audit?format=csv&caller=user:alice", nil, 200)
	if export.Header().Get("Content-Type") != "text/csv; charset=utf-8" ||
		!strings.Contains(export.Body.String(), "helpdesk,tickets,user:alice,alice,false,false,ticket:1 ticket:2,{}") ||
		strings.Contains(export.Body.String(), "broken") {
		t.Fatalf("unexpected export %s", export.Body.String())
	}
}

func TestIntegrationAuditRetention(t *testing.T) {
	store := memory.NewIntegrationAuditStore()
	server := setupAudit(t, store, chatIntegration())
	now := time.Now().UTC()
	for _, record := range []hex.IntegrationAuditRecord{
		{ID: "expired", At: now.AddDate(-1, 0, -1), Integration: "helpdesk", Endpoint: "tickets"},
		{ID: "kept", At: now.AddDate(0, -11, 0), Integration: "helpdesk", Endpoint: "tickets"},
	} {
		if err := store.RecordIntegrationAudit(context.Background(), record); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	server.RunBackground(ctx)
	records := listAudit(t, store, hex.IntegrationAuditFilter{})
	if len(records) != 1 || records[0].ID != "kept" {
		t.Fatalf("the default retention kept the wrong records: %+v", records)
	}
}

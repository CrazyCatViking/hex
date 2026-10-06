package memory

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	hex "github.com/crazycatviking/hex/server"
)

func TestIntegrationAuditFiltersAndRetention(t *testing.T) {
	store := NewIntegrationAuditStore()
	ctx := context.Background()
	day := time.Date(2026, time.October, 3, 12, 0, 0, 0, time.UTC)
	for _, record := range []hex.IntegrationAuditRecord{
		{ID: "a", At: day, Site: "demo", Integration: "crm", Endpoint: "tickets", Caller: "user:alice",
			Input: json.RawMessage(`{}`), Records: []string{"ticket:1", "ticket:2"}},
		{ID: "b", At: day.Add(time.Hour), Site: "demo", Integration: "crm", Endpoint: "contacts", Caller: "user:bob",
			Input: json.RawMessage(`{}`), Records: []string{"contact:7"}, Cached: true},
		{ID: "c", At: day.AddDate(0, 0, 1), Site: "other", Integration: "chat", Endpoint: "channels", Caller: "user:alice",
			Input: json.RawMessage(`{}`), Failed: true},
	} {
		if err := store.RecordIntegrationAudit(ctx, record); err != nil {
			t.Fatal(err)
		}
	}

	for name, entry := range map[string]struct {
		filter hex.IntegrationAuditFilter
		want   string
	}{
		"newest first": {hex.IntegrationAuditFilter{}, "cba"},
		"limit":        {hex.IntegrationAuditFilter{Limit: 2}, "cb"},
		"range":        {hex.IntegrationAuditFilter{Since: day, Until: day.Add(time.Hour)}, "a"},
		"site":         {hex.IntegrationAuditFilter{Site: "demo"}, "ba"},
		"caller":       {hex.IntegrationAuditFilter{Caller: "user:alice"}, "ca"},
		"endpoint":     {hex.IntegrationAuditFilter{Endpoint: "crm.tickets"}, "a"},
		"integration":  {hex.IntegrationAuditFilter{Endpoint: "crm.*"}, "ba"},
		"record":       {hex.IntegrationAuditFilter{Record: "ticket:2"}, "a"},
		"no match":     {hex.IntegrationAuditFilter{Record: "ticket:"}, ""},
	} {
		records, err := store.ListIntegrationAudit(ctx, entry.filter)
		if err != nil {
			t.Fatal(err)
		}
		got := ""
		for _, record := range records {
			got += record.ID
		}
		if got != entry.want {
			t.Errorf("%s: got %q, want %q", name, got, entry.want)
		}
	}

	removed, err := store.DeleteIntegrationAuditBefore(ctx, day.Add(time.Hour))
	if err != nil || removed != 1 {
		t.Fatalf("unexpected removal %d %v", removed, err)
	}
	if records, _ := store.ListIntegrationAudit(ctx, hex.IntegrationAuditFilter{}); len(records) != 2 || !records[1].Cached {
		t.Fatalf("unexpected records after removal %+v", records)
	}
}

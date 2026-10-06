package postgres

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	hex "github.com/crazycatviking/hex/server"
)

func openTestDatabase(t *testing.T) (*Database, context.Context) {
	t.Helper()
	connection := os.Getenv("HEX_TEST_POSTGRES_URL")
	if connection == "" {
		t.Skip("set HEX_TEST_POSTGRES_URL to run against PostgreSQL")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	database, err := New(ctx, connection)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(database.Close)
	if err := database.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	return database, ctx
}

func TestIntegrationStoreAgainstPostgres(t *testing.T) {
	database, ctx := openTestDatabase(t)
	site := "test-" + rand.Text()

	if _, err := database.GetIntegrationApproval(ctx, site, "crm"); !errors.Is(err, hex.ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
	approval := hex.IntegrationApproval{Site: site, Integration: "crm", Status: hex.ApprovalRequested, Reason: "dashboard"}
	if err := database.PutIntegrationApproval(ctx, approval); err != nil {
		t.Fatal(err)
	}
	approval.Status = hex.ApprovalApproved
	if err := database.PutIntegrationApproval(ctx, approval); err != nil {
		t.Fatal(err)
	}
	stored, err := database.GetIntegrationApproval(ctx, site, "crm")
	if err != nil || stored.Status != hex.ApprovalApproved || stored.Reason != "dashboard" {
		t.Fatalf("unexpected approval %+v %v", stored, err)
	}
	if err := database.DeleteIntegrationApproval(ctx, site, "crm"); err != nil {
		t.Fatal(err)
	}
	if err := database.DeleteIntegrationApproval(ctx, site, "crm"); !errors.Is(err, hex.ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}

	owner := "owner-" + rand.Text()
	connected := time.Now().UTC().Add(-time.Hour).Truncate(time.Microsecond)
	record := hex.CredentialRecord{Owner: owner, Connector: "docs", Sealed: []byte{1, 2, 3}, Account: "a@example.test", ConnectedAt: connected, LastUsedAt: connected}
	if err := database.PutCredential(ctx, record); err != nil {
		t.Fatal(err)
	}
	record.Sealed = []byte{4, 5}
	if err := database.PutCredential(ctx, record); err != nil {
		t.Fatal(err)
	}
	credential, err := database.GetCredential(ctx, owner, "docs")
	if err != nil || len(credential.Sealed) != 2 || credential.Sealed[0] != 4 || credential.Account != "a@example.test" || !credential.ConnectedAt.Equal(connected) {
		t.Fatalf("unexpected credential %+v %v", credential, err)
	}
	used := time.Now().UTC().Truncate(time.Microsecond)
	if err := database.TouchCredential(ctx, owner, "docs", used); err != nil {
		t.Fatal(err)
	}
	listed, err := database.ListCredentials(ctx, owner)
	if err != nil || len(listed) != 1 || listed[0].Sealed != nil || !listed[0].LastUsedAt.Equal(used) {
		t.Fatalf("unexpected listing %+v %v", listed, err)
	}

	stale := hex.CredentialRecord{Owner: owner, Connector: "old", Sealed: []byte{9}, ConnectedAt: connected.Add(-200 * 24 * time.Hour), LastUsedAt: connected.Add(-200 * 24 * time.Hour)}
	if err := database.PutCredential(ctx, stale); err != nil {
		t.Fatal(err)
	}
	if _, err := database.DeleteCredentialsUnusedSince(ctx, time.Now().Add(-90*24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := database.GetCredential(ctx, owner, "old"); !errors.Is(err, hex.ErrNotFound) {
		t.Fatalf("an unused credential survived cleanup: %v", err)
	}
	if err := database.DeleteCredential(ctx, owner, "docs"); err != nil {
		t.Fatal(err)
	}
	if _, err := database.GetCredential(ctx, owner, "docs"); !errors.Is(err, hex.ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestAutomationStoreAgainstPostgres(t *testing.T) {
	database, ctx := openTestDatabase(t)
	site := "test-" + rand.Text()
	due := time.Now().UTC().Add(-time.Minute).Truncate(time.Microsecond)
	automation := hex.Automation{Name: "report", Schedule: "@daily", Steps: []hex.AutomationStep{
		{ID: "a", Query: &hex.AutomationQuery{Collection: "people"}},
	}}
	if err := database.ReplaceSiteAutomations(ctx, site, []hex.ScheduledAutomation{
		{Site: site, Automation: automation, NextRun: due},
		{Site: site, Automation: hex.Automation{Name: "manual", Steps: automation.Steps}},
	}); err != nil {
		t.Fatal(err)
	}
	listed, err := database.ListSiteAutomations(ctx, site)
	if err != nil || len(listed) != 2 || listed[0].Automation.Name != "manual" || !listed[0].NextRun.IsZero() {
		t.Fatalf("unexpected automations %+v %v", listed, err)
	}

	dueNow, err := database.DueAutomations(ctx, time.Now().UTC(), 1000)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, entry := range dueNow {
		found = found || entry.Site == site && entry.Automation.Name == "report" && entry.NextRun.Equal(due)
	}
	if !found {
		t.Fatalf("the due automation was not listed: %+v", dueNow)
	}

	// Concurrent claims of the same occurrence: exactly one wins.
	next := due.Add(24 * time.Hour)
	var wins atomic.Int32
	var group sync.WaitGroup
	for range 8 {
		group.Add(1)
		go func() {
			defer group.Done()
			won, err := database.ClaimAutomation(ctx, site, "report", due, next)
			if err != nil {
				t.Error(err)
			}
			if won {
				wins.Add(1)
			}
		}()
	}
	group.Wait()
	if wins.Load() != 1 {
		t.Fatalf("%d claims won", wins.Load())
	}

	for index := range 55 {
		run := hex.AutomationRun{
			ID: rand.Text(), Site: site, Automation: "report", Status: hex.RunSucceeded,
			StartedAt: time.Now().UTC().Add(time.Duration(index) * time.Second), Steps: []hex.AutomationStepRun{},
		}
		if err := database.RecordAutomationRun(ctx, run); err != nil {
			t.Fatal(err)
		}
	}
	runs, err := database.ListAutomationRuns(ctx, site, "report", 100)
	if err != nil || len(runs) != maxRunsPerAutomation || runs[0].StartedAt.Before(runs[1].StartedAt) {
		t.Fatalf("expected %d newest-first runs, got %d: %v", maxRunsPerAutomation, len(runs), err)
	}
	fetched, err := database.GetAutomationRun(ctx, site, runs[0].ID)
	if err != nil || fetched.ID != runs[0].ID {
		t.Fatalf("unexpected run %+v %v", fetched, err)
	}
	if _, err := database.GetAutomationRun(ctx, "other-site", runs[0].ID); !errors.Is(err, hex.ErrNotFound) {
		t.Fatalf("a run was readable from another site: %v", err)
	}

	if err := database.ReplaceSiteAutomations(ctx, site, nil); err != nil {
		t.Fatal(err)
	}
	if listed, err := database.ListSiteAutomations(ctx, site); err != nil || len(listed) != 0 {
		t.Fatalf("automations were not removed: %+v %v", listed, err)
	}
}

func TestAIUsageStoreAgainstPostgres(t *testing.T) {
	database, ctx := openTestDatabase(t)
	site := "test-" + rand.Text()
	day := time.Date(2026, time.October, 3, 12, 0, 0, 0, time.UTC)
	records := []hex.AIUsageRecord{
		{ID: rand.Text(), At: day, Site: site, Caller: "user:a", CallerName: "Old Name", Model: "sonnet", CostMicros: 100,
			Usage: hex.AIUsage{InputTokens: 10, CachedInputTokens: 20, CacheWriteTokens: 5, OutputTokens: 3}},
		{ID: rand.Text(), At: day.Add(time.Hour), Site: site, Caller: "user:a", CallerName: "New Name", Model: "sonnet", CostMicros: 50,
			Usage: hex.AIUsage{InputTokens: 1, OutputTokens: 1, Estimated: true}},
		{ID: rand.Text(), At: day.AddDate(0, 0, 1), Site: site, Caller: "automation:" + site + "/report", Model: "haiku", CostMicros: 7},
		{ID: rand.Text(), At: day.AddDate(0, 1, 0), Site: site, Caller: "user:a", Model: "sonnet", CostMicros: 1000},
	}
	for _, record := range records {
		if err := database.RecordAIUsage(ctx, record); err != nil {
			t.Fatal(err)
		}
	}
	october := hex.AIUsageFilter{Since: time.Date(2026, time.October, 1, 0, 0, 0, 0, time.UTC), Until: time.Date(2026, time.November, 1, 0, 0, 0, 0, time.UTC), Site: site}
	if total, err := database.SumAICost(ctx, october); err != nil || total != 157 {
		t.Fatalf("unexpected October total %d %v", total, err)
	}
	person := october
	person.Caller = "user:a"
	if total, err := database.SumAICost(ctx, person); err != nil || total != 150 {
		t.Fatalf("unexpected person total %d %v", total, err)
	}

	callers, err := database.AIUsageTotals(ctx, october, hex.GroupByCaller)
	if err != nil || len(callers) != 2 || callers[0].Key != "user:a" || callers[0].Name != "New Name" ||
		callers[0].Calls != 2 || callers[0].CachedInputTokens != 20 || callers[0].CacheWriteTokens != 5 || callers[0].Estimated != 1 {
		t.Fatalf("unexpected caller totals %+v %v", callers, err)
	}
	days, err := database.AIUsageTotals(ctx, october, hex.GroupByDay)
	if err != nil || len(days) != 2 || days[0].Key != "2026-10-03" || days[0].CostMicros != 150 {
		t.Fatalf("unexpected daily totals %+v %v", days, err)
	}
	if _, err := database.AIUsageTotals(ctx, october, "colour"); err == nil {
		t.Fatal("an unknown grouping was accepted")
	}

	limit := int64(5_000_000)
	budget := hex.AIBudget{Scope: hex.BudgetSite, Subject: site, LimitMicros: &limit, Disabled: true}
	if err := database.PutAIBudget(ctx, budget); err != nil {
		t.Fatal(err)
	}
	budgets, err := database.ListAIBudgets(ctx)
	found := false
	for _, stored := range budgets {
		found = found || stored.Subject == site && stored.Disabled && *stored.LimitMicros == limit
	}
	if err != nil || !found {
		t.Fatalf("the budget was not stored: %+v %v", budgets, err)
	}
	if err := database.DeleteAIBudget(ctx, hex.BudgetSite, site); err != nil {
		t.Fatal(err)
	}
	if err := database.DeleteAIBudget(ctx, hex.BudgetSite, site); !errors.Is(err, hex.ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestIntegrationAuditStoreAgainstPostgres(t *testing.T) {
	database, ctx := openTestDatabase(t)
	site := "test-" + rand.Text()
	day := time.Date(2026, time.October, 3, 12, 0, 0, 0, time.UTC)
	records := []hex.IntegrationAuditRecord{
		{ID: rand.Text(), At: day, Site: site, Integration: "crm", Endpoint: "tickets", Caller: "user:alice", CallerName: "Alice",
			Input: json.RawMessage(`{"status":"open"}`), Records: []string{"ticket:1", "ticket:2"}},
		{ID: rand.Text(), At: day.Add(time.Hour), Site: site, Integration: "crm", Endpoint: "contacts", Caller: "user:bob",
			Input: json.RawMessage(`{}`), Records: []string{"contact:7"}, Cached: true},
		{ID: rand.Text(), At: day.AddDate(0, 0, 1), Site: site, Integration: "chat", Endpoint: "channels", Caller: "user:alice",
			Input: json.RawMessage(`{}`), Failed: true},
	}
	for _, record := range records {
		if err := database.RecordIntegrationAudit(ctx, record); err != nil {
			t.Fatal(err)
		}
	}

	for name, entry := range map[string]struct {
		filter hex.IntegrationAuditFilter
		want   []int
	}{
		"newest first": {hex.IntegrationAuditFilter{Site: site}, []int{2, 1, 0}},
		"limit":        {hex.IntegrationAuditFilter{Site: site, Limit: 2}, []int{2, 1}},
		"range":        {hex.IntegrationAuditFilter{Site: site, Since: day, Until: day.Add(time.Hour)}, []int{0}},
		"caller":       {hex.IntegrationAuditFilter{Site: site, Caller: "user:alice"}, []int{2, 0}},
		"endpoint":     {hex.IntegrationAuditFilter{Site: site, Endpoint: "crm.tickets"}, []int{0}},
		"integration":  {hex.IntegrationAuditFilter{Site: site, Endpoint: "crm.*"}, []int{1, 0}},
		"record":       {hex.IntegrationAuditFilter{Site: site, Record: "ticket:2"}, []int{0}},
	} {
		listed, err := database.ListIntegrationAudit(ctx, entry.filter)
		if err != nil {
			t.Fatal(err)
		}
		if len(listed) != len(entry.want) {
			t.Errorf("%s: got %d records, want %d", name, len(listed), len(entry.want))
			continue
		}
		for index, want := range entry.want {
			if listed[index].ID != records[want].ID {
				t.Errorf("%s: record %d is %s, want %s", name, index, listed[index].ID, records[want].ID)
			}
		}
	}
	listed, err := database.ListIntegrationAudit(ctx, hex.IntegrationAuditFilter{Site: site, Endpoint: "crm.tickets"})
	if err != nil || len(listed) != 1 || listed[0].CallerName != "Alice" || string(listed[0].Input) != `{"status": "open"}` ||
		len(listed[0].Records) != 2 || !listed[0].At.Equal(day) {
		t.Fatalf("unexpected stored record %+v %v", listed, err)
	}

	if _, err := database.DeleteIntegrationAuditBefore(ctx, day.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	remaining, err := database.ListIntegrationAudit(ctx, hex.IntegrationAuditFilter{Site: site})
	if err != nil || len(remaining) != 2 || !remaining[1].Cached || !remaining[0].Failed {
		t.Fatalf("unexpected records after removal %+v %v", remaining, err)
	}
}

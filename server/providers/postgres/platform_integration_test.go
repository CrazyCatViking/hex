package postgres

import (
	"context"
	"crypto/rand"
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

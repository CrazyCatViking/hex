package hex_test

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"os"
	"sync"
	"testing"
	"time"

	hex "github.com/crazycatviking/hex/server"
	"github.com/crazycatviking/hex/server/providers/memory"
	"github.com/crazycatviking/hex/server/providers/postgres"
)

func TestAutomationStoreReplacementAndClaims(t *testing.T) {
	providers := []struct {
		name string
		open func(*testing.T, context.Context) hex.AutomationStore
	}{
		{"memory", func(*testing.T, context.Context) hex.AutomationStore { return memory.NewAutomationStore() }},
		{"postgres", func(t *testing.T, ctx context.Context) hex.AutomationStore {
			connection := os.Getenv("HEX_TEST_POSTGRES_URL")
			if connection == "" {
				t.Skip("set HEX_TEST_POSTGRES_URL to run against PostgreSQL")
			}
			store, err := postgres.New(ctx, connection)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(store.Close)
			if err := store.Migrate(ctx); err != nil {
				t.Fatal(err)
			}
			return store
		}},
	}
	for _, provider := range providers {
		t.Run(provider.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			store := provider.open(t, ctx)
			for _, test := range []struct {
				name string
				run  func(*testing.T, context.Context, hex.AutomationStore, string)
			}{
				{"claim before stale replacement", testClaimBeforeAutomationReplacement},
				{"replacement before stale definition claim", testAutomationReplacementBeforeClaim},
				{"delete and recreate", testAutomationDeleteAndRecreate},
				{"definition ABA", testAutomationDefinitionABA},
				{"schedule change and zero run", testAutomationScheduleChanges},
				{"detached definition snapshots", testAutomationDefinitionSnapshots},
				{"competing revision claims", testCompetingAutomationClaims},
			} {
				t.Run(test.name, func(t *testing.T) {
					site := "scheduling-test-" + rand.Text()
					t.Cleanup(func() {
						if err := store.ReplaceSiteAutomations(ctx, site, nil); err != nil {
							t.Error(err)
						}
					})
					test.run(t, ctx, store, site)
				})
			}
		})
	}
}

func automationStoreFixture(site string) hex.ScheduledAutomation {
	return hex.ScheduledAutomation{
		Site: site, NextRun: time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC),
		Automation: hex.Automation{Name: "report", Schedule: "@daily", Steps: []hex.AutomationStep{
			{ID: "save", Save: &hex.AutomationSave{Collection: "reports", Data: json.RawMessage(`{"version":"A"}`)}},
		}},
	}
}

func replaceStoredAutomation(t *testing.T, ctx context.Context, store hex.AutomationStore, site string, entry hex.ScheduledAutomation) {
	t.Helper()
	if err := store.ReplaceSiteAutomations(ctx, site, []hex.ScheduledAutomation{entry}); err != nil {
		t.Fatal(err)
	}
}

func storedAutomation(t *testing.T, ctx context.Context, store hex.AutomationStore, site string) hex.ScheduledAutomation {
	t.Helper()
	entries, err := store.ListSiteAutomations(ctx, site)
	if err != nil || len(entries) != 1 {
		t.Fatalf("expected one stored automation: %+v %v", entries, err)
	}
	if entries[0].Revision == "" {
		t.Fatal("replacement did not assign a revision")
	}
	return entries[0]
}

func claimStoredAutomation(t *testing.T, ctx context.Context, store hex.AutomationStore, entry hex.ScheduledAutomation, next time.Time, want bool) {
	t.Helper()
	won, err := store.ClaimAutomation(ctx, entry.Site, entry.Automation.Name, entry.Revision, entry.NextRun, next)
	if err != nil || won != want {
		t.Fatalf("claim won=%v, want %v: %v", won, want, err)
	}
}

func testClaimBeforeAutomationReplacement(t *testing.T, ctx context.Context, store hex.AutomationStore, site string) {
	replaceStoredAutomation(t, ctx, store, site, automationStoreFixture(site))
	stale := storedAutomation(t, ctx, store, site)
	next := stale.NextRun.Add(24 * time.Hour)
	// A replacement has already read/prepared its proposal when the scheduler
	// wins. Applying that stale proposal must not undo the successful claim.
	proposalReady := make(chan struct{})
	claimed := make(chan struct{})
	replaced := make(chan error, 1)
	go func() {
		proposal := stale
		close(proposalReady)
		<-claimed
		replaced <- store.ReplaceSiteAutomations(ctx, site, []hex.ScheduledAutomation{proposal})
	}()
	<-proposalReady
	won, claimErr := store.ClaimAutomation(ctx, stale.Site, stale.Automation.Name, stale.Revision, stale.NextRun, next)
	close(claimed)
	if err := <-replaced; err != nil {
		t.Fatal(err)
	}
	if claimErr != nil || !won {
		t.Fatalf("initial claim did not win: %v", claimErr)
	}
	current := storedAutomation(t, ctx, store, site)
	if !current.NextRun.Equal(next) || current.Revision == stale.Revision {
		t.Fatalf("replacement rewound a claim or reused its revision: %+v", current)
	}
	claimStoredAutomation(t, ctx, store, stale, next, false)
	claimStoredAutomation(t, ctx, store, current, next.Add(24*time.Hour), true)
}

func testAutomationReplacementBeforeClaim(t *testing.T, ctx context.Context, store hex.AutomationStore, site string) {
	replaceStoredAutomation(t, ctx, store, site, automationStoreFixture(site))
	stale := storedAutomation(t, ctx, store, site)
	changed := automationStoreFixture(site)
	changed.Automation.Steps[0].Save.Data = json.RawMessage(`{"version":"B"}`)
	changed.NextRun = stale.NextRun.Add(24 * time.Hour)
	// Keep the schedule, change the executable definition, then let the
	// scheduler try the snapshot it obtained before the replacement.
	replaceStoredAutomation(t, ctx, store, site, changed)
	current := storedAutomation(t, ctx, store, site)
	if !current.NextRun.Equal(stale.NextRun) || current.Revision == stale.Revision {
		t.Fatalf("definition replacement changed the occurrence or reused revision: %+v", current)
	}
	claimStoredAutomation(t, ctx, store, stale, changed.NextRun, false)
	claimStoredAutomation(t, ctx, store, current, changed.NextRun, true)
}

func testAutomationDeleteAndRecreate(t *testing.T, ctx context.Context, store hex.AutomationStore, site string) {
	replaceStoredAutomation(t, ctx, store, site, automationStoreFixture(site))
	stale := storedAutomation(t, ctx, store, site)
	if err := store.ReplaceSiteAutomations(ctx, site, nil); err != nil {
		t.Fatal(err)
	}
	// Even reusing the entire old proposal cannot resurrect its claim token.
	replaceStoredAutomation(t, ctx, store, site, stale)
	current := storedAutomation(t, ctx, store, site)
	if current.Revision == stale.Revision {
		t.Fatal("delete/recreate reused an old revision")
	}
	claimStoredAutomation(t, ctx, store, stale, stale.NextRun.Add(24*time.Hour), false)
}

func testAutomationDefinitionABA(t *testing.T, ctx context.Context, store hex.AutomationStore, site string) {
	replaceStoredAutomation(t, ctx, store, site, automationStoreFixture(site))
	stale := storedAutomation(t, ctx, store, site)
	changed := automationStoreFixture(site)
	changed.Automation.Description = "B"
	replaceStoredAutomation(t, ctx, store, site, changed)
	replaceStoredAutomation(t, ctx, store, site, stale)
	current := storedAutomation(t, ctx, store, site)
	if current.Revision == stale.Revision || !current.NextRun.Equal(stale.NextRun) {
		t.Fatal("A -> B -> A restored a stale claim token")
	}
	claimStoredAutomation(t, ctx, store, stale, stale.NextRun.Add(24*time.Hour), false)
}

func testAutomationScheduleChanges(t *testing.T, ctx context.Context, store hex.AutomationStore, site string) {
	replaceStoredAutomation(t, ctx, store, site, automationStoreFixture(site))
	previous := storedAutomation(t, ctx, store, site)
	changed := automationStoreFixture(site)
	changed.Automation.Schedule = "@weekly"
	changed.NextRun = previous.NextRun.Add(7 * 24 * time.Hour)
	replaceStoredAutomation(t, ctx, store, site, changed)
	if current := storedAutomation(t, ctx, store, site); !current.NextRun.Equal(changed.NextRun) {
		t.Fatal("changed schedule did not use its proposed next run")
	}
	changed.Automation.Disabled = true
	changed.NextRun = time.Time{}
	replaceStoredAutomation(t, ctx, store, site, changed)
	current := storedAutomation(t, ctx, store, site)
	changed.NextRun = previous.NextRun // stale nonzero proposal must stay disabled
	replaceStoredAutomation(t, ctx, store, site, changed)
	if latest := storedAutomation(t, ctx, store, site); !latest.NextRun.IsZero() {
		t.Fatal("unchanged disabled schedule did not preserve zero NextRun")
	}
	claimStoredAutomation(t, ctx, store, current, previous.NextRun, false)
}

func testAutomationDefinitionSnapshots(t *testing.T, ctx context.Context, store hex.AutomationStore, site string) {
	input := automationStoreFixture(site)
	replaceStoredAutomation(t, ctx, store, site, input)
	input.Automation.Steps[0].Save.Data = json.RawMessage(`{"version":"mutated input"}`)
	listed := storedAutomation(t, ctx, store, site)
	listed.Automation.Steps[0].Save.Data = json.RawMessage(`{"version":"mutated listing"}`)
	due, err := store.DueAutomations(ctx, input.NextRun, 10000)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, entry := range due {
		if entry.Site == site {
			found = true
			entry.Automation.Steps[0].Save.Data = json.RawMessage(`{"version":"mutated due snapshot"}`)
		}
	}
	if !found {
		t.Fatal("the automation did not appear in due snapshots")
	}
	current := storedAutomation(t, ctx, store, site)
	var data map[string]string
	if err := json.Unmarshal(current.Automation.Steps[0].Save.Data, &data); err != nil || data["version"] != "A" {
		t.Fatalf("a detached snapshot changed the stored definition: %+v %v", current, err)
	}
}

func testCompetingAutomationClaims(t *testing.T, ctx context.Context, store hex.AutomationStore, site string) {
	replaceStoredAutomation(t, ctx, store, site, automationStoreFixture(site))
	snapshot := storedAutomation(t, ctx, store, site)
	start := make(chan struct{})
	results := make(chan bool, 8)
	var group sync.WaitGroup
	for range 8 {
		group.Add(1)
		go func() {
			defer group.Done()
			<-start
			won, err := store.ClaimAutomation(ctx, site, snapshot.Automation.Name, snapshot.Revision, snapshot.NextRun, snapshot.NextRun.Add(24*time.Hour))
			if err != nil {
				t.Error(err)
			}
			results <- won
		}()
	}
	close(start)
	group.Wait()
	close(results)
	wins := 0
	for won := range results {
		if won {
			wins++
		}
	}
	if wins != 1 {
		t.Fatalf("%d claims won the same revision/occurrence, want 1", wins)
	}
}

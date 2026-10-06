package postgres

import (
	"context"
	"crypto/rand"
	"errors"
	"testing"
	"time"

	hex "github.com/crazycatviking/hex/server"
	"github.com/jackc/pgx/v5"
)

func TestAutomationReplacementSerializesWithClaimsAgainstPostgres(t *testing.T) {
	for _, first := range []string{"claim", "replacement"} {
		t.Run(first+" first", func(t *testing.T) {
			database, ctx := openTestDatabase(t)
			site := "automation-lock-test-" + rand.Text()
			due := time.Now().UTC().Add(-time.Minute).Truncate(time.Microsecond)
			next := due.Add(24 * time.Hour)
			entry := hex.ScheduledAutomation{
				Site: site, NextRun: due,
				Automation: hex.Automation{Name: "report", Schedule: "@daily"},
			}
			if err := database.ReplaceSiteAutomations(ctx, site, []hex.ScheduledAutomation{entry}); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := database.ReplaceSiteAutomations(ctx, site, nil); err != nil {
					t.Error(err)
				}
			})
			stored, err := database.ListSiteAutomations(ctx, site)
			if err != nil || len(stored) != 1 {
				t.Fatalf("read initial automation: %+v %v", stored, err)
			}
			snapshot := stored[0]
			entry.Automation.Description = "replacement"

			// Hold a row lock so the first store operation remains in flight
			// after acquiring its site lock. Observe PostgreSQL's lock catalogue
			// before launching the second operation; no timing assumption is used.
			blocker, err := database.pool.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := blocker.Rollback(ctx); err != nil && !errors.Is(err, pgx.ErrTxClosed) {
					t.Error(err)
				}
			})
			if _, err := blocker.Exec(ctx, `SELECT next_run FROM hex_automations WHERE site = $1 AND name = 'report' FOR UPDATE`, site); err != nil {
				t.Fatal(err)
			}
			type claimResult struct {
				won bool
				err error
			}
			claimed := make(chan claimResult, 1)
			replaced := make(chan error, 1)
			claim := func() {
				won, err := database.ClaimAutomation(ctx, site, "report", snapshot.Revision, due, next)
				claimed <- claimResult{won, err}
			}
			replace := func() {
				replaced <- database.ReplaceSiteAutomations(ctx, site, []hex.ScheduledAutomation{entry})
			}
			if first == "claim" {
				go claim()
			} else {
				go replace()
			}
			waitForAutomationSiteLock(t, ctx, database, site, true)
			if first == "claim" {
				go replace()
			} else {
				go claim()
			}
			waitForAutomationSiteLock(t, ctx, database, site, false)
			if err := blocker.Rollback(ctx); err != nil {
				t.Fatal(err)
			}
			result := <-claimed
			if err := <-replaced; err != nil {
				t.Fatal(err)
			}
			if result.err != nil || result.won != (first == "claim") {
				t.Fatalf("claim won=%v after %s went first: %v", result.won, first, result.err)
			}
			stored, err = database.ListSiteAutomations(ctx, site)
			if err != nil || len(stored) != 1 {
				t.Fatalf("read final automation: %+v %v", stored, err)
			}
			wantNext := due
			if first == "claim" {
				wantNext = next
			}
			if !stored[0].NextRun.Equal(wantNext) || stored[0].Revision == snapshot.Revision || stored[0].Automation.Description != "replacement" {
				t.Fatalf("replacement/claim was not atomic: %+v", stored[0])
			}
		})
	}
}

func waitForAutomationSiteLock(t *testing.T, ctx context.Context, database *Database, site string, granted bool) {
	t.Helper()
	const query = `
		WITH lock_key AS (SELECT hashtextextended('hex-automations:' || $1, 0) AS value)
		SELECT EXISTS (
			SELECT 1 FROM pg_locks, lock_key
			WHERE locktype = 'advisory' AND objsubid = 1 AND granted = $2
			AND classid::bigint = ((value >> 32) & 4294967295)
			AND objid::bigint = (value & 4294967295))`
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	for {
		var exists bool
		if err := database.pool.QueryRow(ctx, query, site, granted).Scan(&exists); err != nil {
			t.Fatal(err)
		}
		if exists {
			return
		}
		select {
		case <-ticker.C:
		case <-ctx.Done():
			t.Fatalf("did not observe automation site lock granted=%v: %v", granted, ctx.Err())
		}
	}
}

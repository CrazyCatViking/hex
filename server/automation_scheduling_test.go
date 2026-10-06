package hex

import (
	"context"
	"errors"
	"testing"
	"time"
)

type automationReplacementProbe struct {
	AutomationStore
	entries []ScheduledAutomation
}

func (*automationReplacementProbe) ListSiteAutomations(context.Context, string) ([]ScheduledAutomation, error) {
	return nil, errors.New("replacement must not read a stale schedule before writing")
}

func (store *automationReplacementProbe) ReplaceSiteAutomations(_ context.Context, _ string, entries []ScheduledAutomation) error {
	store.entries = entries
	return nil
}

type schedulingPublisher struct{ SitePublisher }

func TestAutomationReplacementProposesSchedulesWithoutReading(t *testing.T) {
	store := new(automationReplacementProbe)
	server := &Server{config: Config{Automations: store, Publisher: schedulingPublisher{}}}
	before := time.Now().UTC()
	if err := server.replaceAutomations(context.Background(), "demo", []Automation{{Name: "report", Schedule: "@daily"}}, nil); err != nil {
		t.Fatal(err)
	}
	if len(store.entries) != 1 || !store.entries[0].NextRun.After(before) {
		t.Fatalf("replacement did not propose the new schedule: %+v", store.entries)
	}
}

type schedulerClaimProbe struct {
	AutomationStore
	entry          ScheduledAutomation
	claims         int
	claimedVersion string
}

func (store *schedulerClaimProbe) DueAutomations(context.Context, time.Time, int) ([]ScheduledAutomation, error) {
	return []ScheduledAutomation{store.entry}, nil
}

func (store *schedulerClaimProbe) ClaimAutomation(_ context.Context, _, _, revision string, _, _ time.Time) (bool, error) {
	store.claims++
	store.claimedVersion = revision
	return false, nil
}

func TestSchedulerClaimsSnapshotRevisionAndReleasesLostSlot(t *testing.T) {
	store := &schedulerClaimProbe{entry: ScheduledAutomation{Revision: "snapshot-revision", Automation: Automation{Name: "report", Schedule: "@daily"}}}
	server := &Server{config: Config{Automations: store}}
	slots := make(chan struct{}, 1)
	server.startDueAutomations(context.Background(), slots)
	if store.claims != 1 || store.claimedVersion != store.entry.Revision || len(slots) != 0 {
		t.Fatalf("claim lost its revision or leaked a slot: %+v slots=%d", store, len(slots))
	}
}

func TestSchedulerCancellationBeforeSlotDoesNotClaim(t *testing.T) {
	store := &schedulerClaimProbe{entry: ScheduledAutomation{Revision: "snapshot-revision"}}
	server := &Server{config: Config{Automations: store}}
	slots := make(chan struct{}, 1)
	slots <- struct{}{}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	server.startDueAutomations(ctx, slots)
	if store.claims != 0 || len(slots) != 1 {
		t.Fatalf("cancelled wait advanced an occurrence: claims=%d slots=%d", store.claims, len(slots))
	}
}

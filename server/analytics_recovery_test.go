package hex

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
	"time"
)

type analyticsReadOnlySites struct {
	records map[string]string
}

func (s analyticsReadOnlySites) ListSites(context.Context) ([]string, error) {
	return []string{"demo"}, nil
}
func (s analyticsReadOnlySites) ReadSiteFile(_ context.Context, site, path string) (io.ReadCloser, error) {
	value, exists := s.records[path]
	if !exists {
		return nil, ErrNotFound
	}
	return io.NopCloser(strings.NewReader(value)), nil
}

type recoveryAnalytics struct {
	events map[string]SiteEvent
	err    error
}

func (a *recoveryAnalytics) ObservePerson(context.Context, Person, time.Time) error { return nil }
func (a *recoveryAnalytics) RecordTraffic(context.Context, []TrafficEvent) error    { return nil }
func (a *recoveryAnalytics) QueryAnalytics(context.Context, AnalyticsQuery) (AnalyticsReport, error) {
	return AnalyticsReport{}, nil
}
func (a *recoveryAnalytics) RecordSiteEvents(_ context.Context, events []SiteEvent) error {
	if a.err != nil {
		return a.err
	}
	for _, event := range events {
		a.events[event.ID] = event
	}
	return nil
}

func TestRecoverFullPublicationHistoryWithoutPublisherOrDashboard(t *testing.T) {
	ctx := context.Background()
	at := time.Now().UTC().Truncate(time.Second)
	actor := &Person{ID: "owner", Name: "Original Owner"}
	metadata := SiteMetadata{Title: "Demo", CreatedAt: at.Add(-3 * time.Hour), CreatedBy: actor, PublishedAt: at, PublishedBy: actor}
	history := []Publication{{PublishedAt: at, PublishedBy: actor}, {PublishedAt: at.Add(-time.Hour), PublishedBy: actor}, {PublishedAt: at.Add(-2 * time.Hour), PublishedBy: actor}}
	encodedMetadata, _ := json.Marshal(metadata)
	encodedHistory, _ := json.Marshal(history)
	sites := analyticsReadOnlySites{records: map[string]string{siteMetadataFile: string(encodedMetadata), siteHistoryFile: string(encodedHistory)}}
	analytics := &recoveryAnalytics{events: make(map[string]SiteEvent)}
	server := New(Config{Sites: sites, Analytics: analytics})
	for range 2 {
		if err := server.recoverPublicationHistory(ctx, "demo"); err != nil {
			t.Fatal(err)
		}
	}
	if len(analytics.events) != 4 {
		t.Fatalf("expected creation and all three publications exactly once: %+v", analytics.events)
	}
	for _, event := range analytics.events {
		if event.Title != "Demo" || event.Actor == nil || event.Actor.ID != "owner" {
			t.Fatalf("lost historical metadata: %+v", event)
		}
	}
	analytics.err = errors.New("storage unavailable")
	if err := server.recoverPublicationHistory(ctx, "demo"); !errors.Is(err, analytics.err) {
		t.Fatalf("deletion caller must receive storage failure: %v", err)
	}
	analytics.err = nil
	sites.records[siteHistoryFile] = "invalid JSON"
	if err := server.recoverPublicationHistory(ctx, "demo"); err == nil {
		t.Fatal("unreadable history must block recovery")
	}
}

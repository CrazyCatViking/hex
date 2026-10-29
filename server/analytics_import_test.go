package hex

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type historyImportSites struct {
	mu      sync.Mutex
	reads   map[string]int
	fail    bool
	block   bool
	active  atomic.Int64
	peak    atomic.Int64
	started chan struct{}
}

func (*historyImportSites) ListSites(context.Context) ([]string, error) {
	names := make([]string, 12)
	for index := range names {
		names[index] = fmt.Sprintf("app-%02d", index)
	}
	return names, nil
}

func (sites *historyImportSites) ReadSiteMetadata(ctx context.Context, site string) (*SiteMetadata, error) {
	active := sites.active.Add(1)
	defer sites.active.Add(-1)
	for peak := sites.peak.Load(); active > peak; peak = sites.peak.Load() {
		if sites.peak.CompareAndSwap(peak, active) {
			break
		}
	}
	if sites.started != nil {
		select {
		case sites.started <- struct{}{}:
		default:
		}
	}
	if sites.block {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	time.Sleep(5 * time.Millisecond)
	sites.mu.Lock()
	defer sites.mu.Unlock()
	sites.reads[site]++
	if sites.fail && site == "app-00" {
		return nil, errors.New("metadata unavailable")
	}
	return &SiteMetadata{}, nil
}

func TestHistoryImportBoundsWorkersAndRetriesOnlyFailedSites(t *testing.T) {
	sites := &historyImportSites{reads: make(map[string]int), fail: true}
	server := New(Config{Sites: sites, Analytics: &recoveryAnalytics{}})
	completed := make(map[string]bool)
	if server.importAnalyticsHistoryPass(t.Context(), completed) || len(completed) != 11 {
		t.Fatalf("partial failure was marked complete: %v", completed)
	}
	if peak := sites.peak.Load(); peak < 2 || peak > 4 {
		t.Fatalf("unexpected history import concurrency: %d", peak)
	}
	sites.fail = false
	if !server.importAnalyticsHistoryPass(t.Context(), completed) || len(completed) != 12 {
		t.Fatal("failed site was not retried")
	}
	for site, reads := range sites.reads {
		want := 1
		if site == "app-00" {
			want = 2
		}
		if reads != want {
			t.Fatalf("%s was imported %d times, want %d", site, reads, want)
		}
	}
	server.importAnalyticsHistory(t.Context())
	if server.analyticsImportPending.Load() {
		t.Fatal("completed import still reported as pending")
	}
}

func TestBackgroundHistoryImportStopsOnHostCancellation(t *testing.T) {
	sites := &historyImportSites{block: true, started: make(chan struct{}, 1)}
	server := New(Config{Sites: sites, Analytics: &recoveryAnalytics{}})
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan struct{})
	go func() {
		server.RunBackground(ctx)
		close(done)
	}()
	select {
	case <-sites.started:
	case <-time.After(time.Second):
		t.Fatal("background import did not start")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("background import retained a detached request or retry timer")
	}
	if sites.active.Load() != 0 {
		t.Fatal("history import left active workers")
	}
}

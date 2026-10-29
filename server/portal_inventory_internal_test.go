package hex

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"
)

type blockedPortalInventory struct {
	listed  atomic.Int64
	read    atomic.Int64
	started chan struct{}
	release chan struct{}
}

func (d *blockedPortalInventory) ListSites(context.Context) ([]string, error) {
	d.listed.Add(1)
	names := make([]string, 16)
	for index := range names {
		names[index] = fmt.Sprintf("app-%02d", index)
	}
	return names, nil
}

func (d *blockedPortalInventory) ReadSiteMetadata(ctx context.Context, name string) (*SiteMetadata, error) {
	d.read.Add(1)
	d.started <- struct{}{}
	select {
	case <-d.release:
		return &SiteMetadata{Title: name}, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func TestPortalInventoryCoalescesRefreshWithoutSharingCancellation(t *testing.T) {
	directory := &blockedPortalInventory{started: make(chan struct{}, 64), release: make(chan struct{})}
	server := New(Config{Sites: directory, SiteBaseURL: "http://example.com"})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	first := make(chan error, 1)
	second := make(chan error, 1)
	go func() { _, err := server.portalSites(ctx); first <- err }()
	for range 8 {
		select {
		case <-directory.started:
		case <-time.After(time.Second):
			close(directory.release)
			t.Fatal("metadata reads were serialized")
		}
	}
	if directory.read.Load() != 8 {
		close(directory.release)
		t.Fatal("metadata concurrency is not bounded to eight workers")
	}
	go func() { _, err := server.portalSites(context.Background()); second <- err }()
	cancel()
	if err := <-first; !errors.Is(err, context.Canceled) {
		close(directory.release)
		t.Fatalf("cancellation was ignored: %v", err)
	}
	close(directory.release)
	if err := <-second; err != nil {
		t.Fatal("one search canceled another viewer's refresh:", err)
	}
	if directory.listed.Load() != 1 || directory.read.Load() != 16 {
		t.Fatal("concurrent viewers duplicated the descriptive inventory")
	}
	if _, err := server.portalSites(context.Background()); err != nil {
		t.Fatal(err)
	}
	if directory.listed.Load() != 1 {
		t.Fatal("warm cache was not reused")
	}
	server.portalInventory.mu.Lock()
	server.portalInventory.expires = time.Now().Add(-time.Second)
	server.portalInventory.mu.Unlock()
	if _, err := server.portalSites(context.Background()); err != nil {
		t.Fatal(err)
	}
	if directory.listed.Load() != 2 {
		t.Fatal("expired inventory was not refreshed")
	}
	server.invalidatePortalInventory()
	if _, err := server.portalSites(context.Background()); err != nil {
		t.Fatal(err)
	}
	if directory.listed.Load() != 3 {
		t.Fatal("publication invalidation was ignored")
	}
}

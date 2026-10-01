package hex

import (
	"context"
	"testing"
	"time"
)

type barrierAccessStore struct {
	AccessStore
	started chan struct{}
	release chan struct{}
	reads   int
}

func (s *barrierAccessStore) GetSiteAccess(context.Context, string) (SiteAccess, error) {
	s.reads++
	if s.reads == 1 {
		close(s.started)
		<-s.release
		return SiteAccess{Owners: []string{"user:old"}}, nil
	}
	return SiteAccess{Owners: []string{"user:new"}}, nil
}

func TestPolicyCacheDiscardsFillAfterInvalidation(t *testing.T) {
	cache := newPolicyCache(time.Hour)
	cache.entries["demo"] = cachedPolicy{access: SiteAccess{Owners: []string{"user:old"}}, exists: true}
	store := &barrierAccessStore{started: make(chan struct{}), release: make(chan struct{})}
	type result struct {
		access SiteAccess
		exists bool
		err    error
	}
	done := make(chan result, 1)
	go func() {
		access, exists, err := cache.get(context.Background(), store, "demo")
		done <- result{access, exists, err}
	}()
	<-store.started
	cache.invalidate("demo")
	close(store.release)
	old := <-done
	if old.err != nil || !old.exists || old.access.Owners[0] != "user:old" {
		t.Fatalf("in-flight read: %+v", old)
	}
	if _, ok := cache.entries["demo"]; ok {
		t.Fatal("in-flight read resurrected the invalidated entry")
	}
	access, exists, err := cache.get(context.Background(), store, "demo")
	if err != nil || !exists || len(access.Owners) != 1 || access.Owners[0] != "user:new" || store.reads != 2 {
		t.Fatalf("next read did not fetch updated policy: %+v, %v, %v, reads=%d", access, exists, err, store.reads)
	}
	if _, _, err := cache.get(context.Background(), store, "demo"); err != nil || store.reads != 2 {
		t.Fatalf("updated policy was not cached: %v, reads=%d", err, store.reads)
	}
}

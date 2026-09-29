package memory

import (
	"context"
	"sync"

	hex "github.com/crazycatviking/hex/server"
)

// AccessStore keeps site access policies in process memory. Policies disappear
// on restart, so it is only suitable for local development and tests.
type AccessStore struct {
	mu      sync.RWMutex
	entries map[string]hex.SiteAccess
}

func NewAccessStore() *AccessStore {
	return &AccessStore{entries: make(map[string]hex.SiteAccess)}
}

func (s *AccessStore) GetSiteAccess(ctx context.Context, site string) (hex.SiteAccess, error) {
	if err := ctx.Err(); err != nil {
		return hex.SiteAccess{}, err
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	access, ok := s.entries[site]
	if !ok {
		return hex.SiteAccess{}, hex.ErrNotFound
	}

	return hex.CloneSiteAccess(access), nil
}

func (s *AccessStore) PutSiteAccess(ctx context.Context, site string, access hex.SiteAccess) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	s.entries[site] = hex.CloneSiteAccess(access)
	return nil
}

func (s *AccessStore) DeleteSiteAccess(ctx context.Context, site string) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if _, ok := s.entries[site]; !ok {
		return hex.ErrNotFound
	}

	delete(s.entries, site)
	return nil
}

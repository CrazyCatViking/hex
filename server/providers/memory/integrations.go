package memory

import (
	"context"
	"slices"
	"strings"
	"sync"
	"time"

	hex "github.com/crazycatviking/hex/server"
)

// IntegrationStore keeps integration approvals and connected-account
// credentials in process memory, for local development and tests.
type IntegrationStore struct {
	mu          sync.RWMutex
	approvals   map[string]hex.IntegrationApproval
	credentials map[string]hex.CredentialRecord
}

func NewIntegrationStore() *IntegrationStore {
	return &IntegrationStore{
		approvals:   make(map[string]hex.IntegrationApproval),
		credentials: make(map[string]hex.CredentialRecord),
	}
}

func pairKey(first, second string) string {
	return first + "\x00" + second
}

func (s *IntegrationStore) GetIntegrationApproval(ctx context.Context, site, integration string) (hex.IntegrationApproval, error) {
	if err := ctx.Err(); err != nil {
		return hex.IntegrationApproval{}, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	approval, exists := s.approvals[pairKey(site, integration)]
	if !exists {
		return hex.IntegrationApproval{}, hex.ErrNotFound
	}
	return approval, nil
}

func (s *IntegrationStore) PutIntegrationApproval(ctx context.Context, approval hex.IntegrationApproval) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.approvals[pairKey(approval.Site, approval.Integration)] = approval
	return nil
}

func (s *IntegrationStore) DeleteIntegrationApproval(ctx context.Context, site, integration string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	key := pairKey(site, integration)
	if _, exists := s.approvals[key]; !exists {
		return hex.ErrNotFound
	}
	delete(s.approvals, key)
	return nil
}

func (s *IntegrationStore) ListIntegrationApprovals(ctx context.Context) ([]hex.IntegrationApproval, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	approvals := make([]hex.IntegrationApproval, 0, len(s.approvals))
	for _, approval := range s.approvals {
		approvals = append(approvals, approval)
	}
	slices.SortFunc(approvals, func(a, b hex.IntegrationApproval) int {
		return strings.Compare(pairKey(a.Site, a.Integration), pairKey(b.Site, b.Integration))
	})
	return approvals, nil
}

func (s *IntegrationStore) GetCredential(ctx context.Context, owner, connector string) (hex.CredentialRecord, error) {
	if err := ctx.Err(); err != nil {
		return hex.CredentialRecord{}, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	record, exists := s.credentials[pairKey(owner, connector)]
	if !exists {
		return hex.CredentialRecord{}, hex.ErrNotFound
	}
	record.Sealed = slices.Clone(record.Sealed)
	return record, nil
}

func (s *IntegrationStore) PutCredential(ctx context.Context, record hex.CredentialRecord) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	record.Sealed = slices.Clone(record.Sealed)
	s.credentials[pairKey(record.Owner, record.Connector)] = record
	return nil
}

func (s *IntegrationStore) TouchCredential(ctx context.Context, owner, connector string, usedAt time.Time) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	key := pairKey(owner, connector)
	record, exists := s.credentials[key]
	if !exists {
		return hex.ErrNotFound
	}
	record.LastUsedAt = usedAt
	s.credentials[key] = record
	return nil
}

func (s *IntegrationStore) DeleteCredential(ctx context.Context, owner, connector string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	key := pairKey(owner, connector)
	if _, exists := s.credentials[key]; !exists {
		return hex.ErrNotFound
	}
	delete(s.credentials, key)
	return nil
}

func (s *IntegrationStore) ListCredentials(ctx context.Context, owner string) ([]hex.CredentialRecord, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	records := make([]hex.CredentialRecord, 0)
	for _, record := range s.credentials {
		if owner != "" && record.Owner != owner {
			continue
		}
		record.Sealed = nil
		records = append(records, record)
	}
	slices.SortFunc(records, func(a, b hex.CredentialRecord) int {
		return strings.Compare(pairKey(a.Owner, a.Connector), pairKey(b.Owner, b.Connector))
	})
	return records, nil
}

func (s *IntegrationStore) DeleteCredentialsUnusedSince(ctx context.Context, cutoff time.Time) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	removed := 0
	for key, record := range s.credentials {
		if record.LastUsedAt.Before(cutoff) {
			delete(s.credentials, key)
			removed++
		}
	}
	return removed, nil
}

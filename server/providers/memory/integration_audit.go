package memory

import (
	"context"
	"slices"
	"strings"
	"sync"
	"time"

	hex "github.com/crazycatviking/hex/server"
)

// IntegrationAuditStore keeps the integration audit log in process memory,
// for local development and tests.
type IntegrationAuditStore struct {
	mu      sync.Mutex
	records []hex.IntegrationAuditRecord
}

func NewIntegrationAuditStore() *IntegrationAuditStore {
	return &IntegrationAuditStore{}
}

func (s *IntegrationAuditStore) RecordIntegrationAudit(ctx context.Context, record hex.IntegrationAuditRecord) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	record.Input = slices.Clone(record.Input)
	record.Records = slices.Clone(record.Records)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.records = append(s.records, record)
	return nil
}

func auditMatches(record hex.IntegrationAuditRecord, filter hex.IntegrationAuditFilter) bool {
	switch {
	case !filter.Since.IsZero() && record.At.Before(filter.Since):
		return false
	case !filter.Until.IsZero() && !record.At.Before(filter.Until):
		return false
	case filter.Site != "" && record.Site != filter.Site:
		return false
	case filter.Caller != "" && record.Caller != filter.Caller:
		return false
	case filter.Endpoint != "" && !auditEndpointMatches(record, filter.Endpoint):
		return false
	case filter.Record != "" && !slices.Contains(record.Records, filter.Record):
		return false
	}
	return true
}

// auditEndpointMatches compares "<integration>.<endpoint>", where the
// endpoint may be "*".
func auditEndpointMatches(record hex.IntegrationAuditRecord, endpoint string) bool {
	integration, name, _ := strings.Cut(endpoint, ".")
	if record.Integration != integration {
		return false
	}
	return name == "*" || record.Endpoint == name
}

func (s *IntegrationAuditStore) ListIntegrationAudit(ctx context.Context, filter hex.IntegrationAuditFilter) ([]hex.IntegrationAuditRecord, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	result := make([]hex.IntegrationAuditRecord, 0)
	for _, record := range slices.Backward(s.records) {
		if auditMatches(record, filter) {
			record.Input = slices.Clone(record.Input)
			record.Records = slices.Clone(record.Records)
			result = append(result, record)
		}
	}
	slices.SortStableFunc(result, func(a, b hex.IntegrationAuditRecord) int {
		return b.At.Compare(a.At)
	})
	if filter.Limit > 0 && len(result) > filter.Limit {
		result = result[:filter.Limit]
	}
	return result, nil
}

func (s *IntegrationAuditStore) DeleteIntegrationAuditBefore(ctx context.Context, cutoff time.Time) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	kept := s.records[:0]
	for _, record := range s.records {
		if !record.At.Before(cutoff) {
			kept = append(kept, record)
		}
	}
	removed := len(s.records) - len(kept)
	clear(s.records[len(kept):])
	s.records = kept
	return removed, nil
}

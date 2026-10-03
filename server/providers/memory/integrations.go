package memory

import (
	"context"
	"slices"
	"strings"
	"sync"
	"time"

	hex "github.com/crazycatviking/hex/server"
)

type integrationCall struct {
	audit   hex.ToolCallAudit
	counted bool
	pending bool
	bytes   int64
	records int64
}

// IntegrationState is ephemeral development storage. PostgreSQL provides shared
// budgets and audit retention across production processes and restarts.
type IntegrationState struct {
	mu    sync.Mutex
	calls map[string]integrationCall
}

func NewIntegrationState() *IntegrationState {
	return &IntegrationState{calls: make(map[string]integrationCall)}
}

func (s *IntegrationState) ReserveToolCall(ctx context.Context, reservation hex.ToolCallReservation) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var calls, bytes, records, active int64
	cutoff := reservation.Audit.StartedAt.Add(-time.Hour)
	for _, call := range s.calls {
		if call.audit.UserID != reservation.Audit.UserID || !call.counted {
			continue
		}
		if !call.audit.StartedAt.Before(cutoff) {
			calls++
			bytes += call.bytes
			records += call.records
		}
		if call.pending && call.audit.LeaseUntil.After(reservation.Audit.StartedAt) {
			active++
		}
	}
	budget := reservation.Budget
	if calls >= budget.CallsPerHour || bytes+reservation.OutputBytes > budget.OutputBytesPerHour || records+reservation.Records > budget.RecordsPerHour || active >= budget.ConcurrentCalls {
		return hex.ErrIntegrationBudget
	}
	if _, exists := s.calls[reservation.Audit.ID]; exists {
		return hex.ErrForbidden
	}
	s.calls[reservation.Audit.ID] = integrationCall{audit: reservation.Audit, counted: true, pending: true, bytes: reservation.OutputBytes, records: reservation.Records}
	return nil
}

func (s *IntegrationState) FinishToolCall(ctx context.Context, audit hex.ToolCallAudit) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	call, exists := s.calls[audit.ID]
	if !exists || call.audit.UserID != audit.UserID {
		return hex.ErrNotFound
	}
	if !call.pending {
		return nil
	}
	if audit.OutputBytes < 0 || audit.OutputBytes > call.bytes || audit.Records < 0 || audit.Records > call.records {
		return hex.ErrForbidden
	}
	call.audit, call.pending, call.bytes, call.records = audit, false, audit.OutputBytes, audit.Records
	s.calls[audit.ID] = call
	return nil
}

func (s *IntegrationState) RecordToolAudit(ctx context.Context, audit hex.ToolCallAudit) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.calls[audit.ID]; !exists {
		s.calls[audit.ID] = integrationCall{audit: audit}
	}
	return nil
}

func (s *IntegrationState) ListToolAudit(ctx context.Context, query hex.ToolAuditQuery) ([]hex.ToolCallAudit, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	result := []hex.ToolCallAudit{}
	for _, call := range s.calls {
		audit := call.audit
		if query.UserID != "" && audit.UserID != query.UserID || query.Tool != "" && audit.Tool != query.Tool || !query.Before.IsZero() && !audit.StartedAt.Before(query.Before) {
			continue
		}
		result = append(result, audit)
	}
	slices.SortFunc(result, func(a, b hex.ToolCallAudit) int {
		if !a.StartedAt.Equal(b.StartedAt) {
			return b.StartedAt.Compare(a.StartedAt)
		}
		return strings.Compare(b.ID, a.ID)
	})
	limit := query.Limit
	if limit < 1 || limit > 100 {
		limit = 50
	}
	if len(result) > limit {
		result = result[:limit]
	}
	return result, nil
}

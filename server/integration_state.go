package hex

import (
	"context"
	"errors"
	"time"
)

var ErrIntegrationBudget = errors.New("integration budget exhausted")

// IntegrationBudget is shared per authenticated user across all tools, bundles
// and transports. Pending calls reserve their worst-case output before dispatch.
type IntegrationBudget struct {
	CallsPerHour       int64 `json:"callsPerHour"`
	OutputBytesPerHour int64 `json:"outputBytesPerHour"`
	RecordsPerHour     int64 `json:"recordsPerHour"`
	ConcurrentCalls    int64 `json:"concurrentCalls"`
}

func DefaultIntegrationBudget() IntegrationBudget {
	return IntegrationBudget{CallsPerHour: 60, OutputBytesPerHour: 1 << 20, RecordsPerHour: 1000, ConcurrentCalls: 2}
}

type ToolCallAudit struct {
	ID             string    `json:"id"`
	UserID         string    `json:"userId"`
	Integration    string    `json:"integration"`
	Tool           string    `json:"tool"`
	Version        string    `json:"version"`
	Bundle         string    `json:"bundle"`
	Transport      string    `json:"transport"`
	Resource       string    `json:"resource,omitempty"`
	InputHash      string    `json:"inputHash"`
	StartedAt      time.Time `json:"startedAt"`
	LeaseUntil     time.Time `json:"leaseUntil"`
	Status         string    `json:"status"`
	OutputBytes    int64     `json:"outputBytes"`
	Records        int64     `json:"records"`
	DurationMillis int64     `json:"durationMillis"`
}

type ToolCallReservation struct {
	Audit       ToolCallAudit
	Budget      IntegrationBudget
	OutputBytes int64
	Records     int64
}

type ToolAuditQuery struct {
	UserID string
	Tool   string
	Before time.Time
	Limit  int
}

// IntegrationStateStore must reserve atomically across instances. Failed audit
// writes block output delivery; integrations never fail open on missing storage.
type IntegrationStateStore interface {
	ReserveToolCall(context.Context, ToolCallReservation) error
	FinishToolCall(context.Context, ToolCallAudit) error
	RecordToolAudit(context.Context, ToolCallAudit) error
	ListToolAudit(context.Context, ToolAuditQuery) ([]ToolCallAudit, error)
}

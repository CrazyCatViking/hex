package hex

import (
	"context"
	"time"
)

// AdminAccessStore stores temporary privileges and their audit history together.
// Grant and revocation writes must be atomic and reads must not use a local cache.
type AdminAccessStore interface {
	GrantAdminAccess(context.Context, AdminAccessGrant) error
	RevokeAdminAccess(context.Context, string, string, Person, time.Time) error
	// ListAdminAccess returns at most Limit grants, ordered by ID descending,
	// honoring every supplied filter. Before is an exclusive ID cursor.
	ListAdminAccess(context.Context, AdminAccessQuery) ([]AdminAccessGrant, error)
}

type AdminAccessGrant struct {
	ID        string    `json:"id"`
	Site      string    `json:"site"`
	Subject   string    `json:"subject"`
	Actor     Person    `json:"actor"`
	Scope     string    `json:"scope"`
	Reason    string    `json:"reason"`
	StartedAt time.Time `json:"startedAt"`
	ExpiresAt time.Time `json:"expiresAt"`
	RevokedAt time.Time `json:"revokedAt,omitzero"`
	RevokedBy *Person   `json:"revokedBy,omitempty"`
}

func (g AdminAccessGrant) Active(at time.Time) bool {
	return !at.Before(g.StartedAt) && at.Before(g.ExpiresAt) && g.RevokedAt.IsZero()
}

type AdminAccessQuery struct {
	ID       string
	Site     string
	Subject  string
	ActiveAt time.Time
	Before   string
	Limit    int
}

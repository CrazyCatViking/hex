package memory

import (
	"context"
	"errors"
	"slices"
	"strings"
	"time"

	hex "github.com/crazycatviking/hex/server"
)

func (d *Database) GrantAdminAccess(ctx context.Context, grant hex.AdminAccessGrant) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.adminAccess == nil {
		d.adminAccess = make(map[string]hex.AdminAccessGrant)
	}
	if _, exists := d.adminAccess[grant.ID]; exists {
		return errors.New("admin access grant already exists")
	}
	for id, old := range d.adminAccess {
		if old.Site == grant.Site && old.Subject == grant.Subject && old.Scope == grant.Scope && old.RevokedAt.IsZero() && old.ExpiresAt.After(grant.StartedAt) {
			actor := grant.Actor
			old.RevokedAt, old.RevokedBy = grant.StartedAt, &actor
			if old.RevokedAt.Before(old.StartedAt) {
				old.RevokedAt = old.StartedAt
			}
			d.adminAccess[id] = old
		}
	}
	d.adminAccess[grant.ID] = grant
	return nil
}

func (d *Database) RevokeAdminAccess(ctx context.Context, site, id string, by hex.Person, at time.Time) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	grant, exists := d.adminAccess[id]
	if !exists || grant.Site != site {
		return hex.ErrNotFound
	}
	if grant.RevokedAt.IsZero() {
		grant.RevokedAt, grant.RevokedBy = at, &by
		d.adminAccess[id] = grant
	}
	return nil
}

func (d *Database) ListAdminAccess(ctx context.Context, query hex.AdminAccessQuery) ([]hex.AdminAccessGrant, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	d.mu.RLock()
	defer d.mu.RUnlock()
	rows := []hex.AdminAccessGrant{}
	for _, grant := range d.adminAccess {
		if query.ID != "" && grant.ID != query.ID || query.Site != "" && grant.Site != query.Site || query.Subject != "" && grant.Subject != query.Subject || !query.ActiveAt.IsZero() && !grant.Active(query.ActiveAt) || query.Before != "" && grant.ID >= query.Before {
			continue
		}
		if grant.RevokedBy != nil {
			by := *grant.RevokedBy
			grant.RevokedBy = &by
		}
		rows = append(rows, grant)
	}
	slices.SortFunc(rows, func(a, b hex.AdminAccessGrant) int { return strings.Compare(b.ID, a.ID) })
	return rows[:min(len(rows), max(1, min(query.Limit, 1000)))], nil
}

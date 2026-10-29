package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	hex "github.com/crazycatviking/hex/server"
)

func (d *Database) migrateAdminAccess(ctx context.Context) error {
	_, err := d.pool.Exec(ctx, `CREATE TABLE IF NOT EXISTS hex_admin_access (
		id text COLLATE "C" PRIMARY KEY, site text NOT NULL, subject text NOT NULL,
		scope text NOT NULL, actor jsonb NOT NULL, reason text NOT NULL,
		started_at timestamptz NOT NULL, expires_at timestamptz NOT NULL,
		revoked_at timestamptz, revoked_by jsonb
	);
	CREATE INDEX IF NOT EXISTS hex_admin_access_subject ON hex_admin_access(subject, site, expires_at) WHERE revoked_at IS NULL;
	CREATE INDEX IF NOT EXISTS hex_admin_access_site ON hex_admin_access(site, id DESC);`)
	if err != nil {
		return fmt.Errorf("migrate temporary admin access: %w", err)
	}
	return nil
}

func (d *Database) GrantAdminAccess(ctx context.Context, grant hex.AdminAccessGrant) error {
	actor, err := json.Marshal(grant.Actor)
	if err != nil {
		return err
	}
	tx, err := d.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer rollbackAnalytics(tx)
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, "hex-admin-access:"+grant.Site+":"+grant.Subject); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE hex_admin_access SET revoked_at=GREATEST(started_at,$4),revoked_by=$5 WHERE site=$1 AND subject=$2 AND scope=$3 AND revoked_at IS NULL AND expires_at>$4`, grant.Site, grant.Subject, grant.Scope, grant.StartedAt, actor); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO hex_admin_access(id,site,subject,scope,actor,reason,started_at,expires_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8)`, grant.ID, grant.Site, grant.Subject, grant.Scope, actor, grant.Reason, grant.StartedAt, grant.ExpiresAt); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (d *Database) RevokeAdminAccess(ctx context.Context, site, id string, by hex.Person, at time.Time) error {
	actor, err := json.Marshal(by)
	if err != nil {
		return err
	}
	result, err := d.pool.Exec(ctx, `UPDATE hex_admin_access SET revoked_at=COALESCE(revoked_at,$3),revoked_by=COALESCE(revoked_by,$4) WHERE site=$1 AND id=$2`, site, id, at, actor)
	if err != nil {
		return err
	}
	if result.RowsAffected() == 0 {
		return hex.ErrNotFound
	}
	return nil
}

func (d *Database) ListAdminAccess(ctx context.Context, query hex.AdminAccessQuery) ([]hex.AdminAccessGrant, error) {
	var activeAt *time.Time
	if !query.ActiveAt.IsZero() {
		activeAt = &query.ActiveAt
	}
	rows, err := d.pool.Query(ctx, `SELECT id,site,subject,scope,actor,reason,started_at,expires_at,revoked_at,revoked_by FROM hex_admin_access
		WHERE ($1='' OR site=$1) AND ($2='' OR subject=$2) AND ($3::timestamptz IS NULL OR (started_at<=$3 AND expires_at>$3 AND revoked_at IS NULL))
		AND ($4='' OR id<$4) AND ($6='' OR id=$6) ORDER BY id DESC LIMIT $5`, query.Site, query.Subject, activeAt, query.Before, max(1, min(query.Limit, 1000)), query.ID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	grants := []hex.AdminAccessGrant{}
	for rows.Next() {
		var grant hex.AdminAccessGrant
		var actor, revokedBy []byte
		var revokedAt *time.Time
		if err := rows.Scan(&grant.ID, &grant.Site, &grant.Subject, &grant.Scope, &actor, &grant.Reason, &grant.StartedAt, &grant.ExpiresAt, &revokedAt, &revokedBy); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(actor, &grant.Actor); err != nil {
			return nil, err
		}
		if revokedAt != nil {
			grant.RevokedAt = *revokedAt
		}
		if len(revokedBy) > 0 {
			if err := json.Unmarshal(revokedBy, &grant.RevokedBy); err != nil {
				return nil, err
			}
		}
		grants = append(grants, grant)
	}
	return grants, rows.Err()
}

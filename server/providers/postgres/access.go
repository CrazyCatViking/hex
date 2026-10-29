package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"

	hex "github.com/crazycatviking/hex/server"
	"github.com/jackc/pgx/v5"
)

func (d *Database) GetSiteAccess(ctx context.Context, site string) (hex.SiteAccess, error) {
	const query = `
		SELECT policy FROM hex_site_policies
		WHERE site = $1`

	var data []byte
	err := d.pool.QueryRow(ctx, query, site).Scan(&data)
	if errors.Is(err, pgx.ErrNoRows) {
		return hex.SiteAccess{}, hex.ErrNotFound
	}
	if err != nil {
		return hex.SiteAccess{}, fmt.Errorf("read site policy for %s: %w", site, err)
	}

	var access hex.SiteAccess
	if err := decodeSitePolicy(data, &access); err != nil {
		return hex.SiteAccess{}, fmt.Errorf("decode site policy for %s: %w", site, err)
	}
	return hex.NormalizeSiteAccess(access), nil
}

func (d *Database) GetSiteAccessBatch(ctx context.Context, sites []string) (map[string]hex.SiteAccess, error) {
	rows, err := d.pool.Query(ctx, `SELECT site, policy FROM hex_site_policies WHERE site = ANY($1)`, sites)
	if err != nil {
		return nil, fmt.Errorf("read site policies: %w", err)
	}
	defer rows.Close()
	policies := make(map[string]hex.SiteAccess, len(sites))
	for rows.Next() {
		var site string
		var data []byte
		if err := rows.Scan(&site, &data); err != nil {
			return nil, fmt.Errorf("scan site policy: %w", err)
		}
		var policy hex.SiteAccess
		if err := decodeSitePolicy(data, &policy); err != nil {
			return nil, fmt.Errorf("decode site policy for %s: %w", site, err)
		}
		policies[site] = hex.NormalizeSiteAccess(policy)
	}
	return policies, rows.Err()
}

func (d *Database) PutSiteAccess(ctx context.Context, site string, access hex.SiteAccess) error {
	return d.UpdateSiteAccess(ctx, site, func(hex.SiteAccess, bool) (*hex.SiteAccess, error) {
		return &access, nil
	})
}

func (d *Database) DeleteSiteAccess(ctx context.Context, site string) error {
	return d.UpdateSiteAccess(ctx, site, func(_ hex.SiteAccess, exists bool) (*hex.SiteAccess, error) {
		if !exists {
			return nil, hex.ErrNotFound
		}
		return nil, nil
	})
}

func (d *Database) UpdateSiteAccess(ctx context.Context, site string, update hex.AccessUpdate) error {
	transaction, err := d.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return fmt.Errorf("begin site policy update: %w", err)
	}
	defer func() {
		if err := transaction.Rollback(ctx); err != nil && !errors.Is(err, pgx.ErrTxClosed) {
			slog.Error("roll back site policy update", "site", site, "error", err)
		}
	}()

	// A row lock alone cannot serialize claims for a name with no row yet.
	// The transaction lock covers both creation and subsequent mutations.
	if _, err := transaction.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('hex-policy:' || $1, 0))`, site); err != nil {
		return fmt.Errorf("lock site policy for %s: %w", site, err)
	}
	var data []byte
	err = transaction.QueryRow(ctx, `SELECT policy FROM hex_site_policies WHERE site = $1 FOR UPDATE`, site).Scan(&data)
	exists := err == nil
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("read site policy for update: %w", err)
	}
	var current hex.SiteAccess
	if exists {
		if err := decodeSitePolicy(data, &current); err != nil {
			return fmt.Errorf("decode site policy for update: %w", err)
		}
	}
	next, err := update(hex.NormalizeSiteAccess(current), exists)
	if err != nil {
		return err
	}
	if next == nil {
		_, err = transaction.Exec(ctx, `DELETE FROM hex_site_policies WHERE site = $1`, site)
	} else {
		data, err = encodeSitePolicy(hex.NormalizeSiteAccess(*next))
		if err != nil {
			return fmt.Errorf("encode site policy for %s: %w", site, err)
		}
		_, err = transaction.Exec(ctx, `
			INSERT INTO hex_site_policies (site, policy) VALUES ($1, $2)
			ON CONFLICT (site) DO UPDATE SET policy = EXCLUDED.policy`, site, data)
	}
	if err != nil {
		return fmt.Errorf("update site policy for %s: %w", site, err)
	}
	if err := transaction.Commit(ctx); err != nil {
		return fmt.Errorf("commit site policy update: %w", err)
	}
	return nil
}

// Old binaries must fail closed during a rolling upgrade, not interpret a v2
// policy's absent legacy lists as permission for everybody. Their SiteAccess
// decoder rejects this envelope's boolean owners field. New binaries unwrap it.
func encodeSitePolicy(policy hex.SiteAccess) ([]byte, error) {
	if policy.Version != 2 {
		return json.Marshal(policy)
	}
	return json.Marshal(struct {
		Version int            `json:"version"`
		Policy  hex.SiteAccess `json:"policy"`
		Owners  bool           `json:"owners"`
	}{Version: 2, Policy: policy})
}

func decodeSitePolicy(data []byte, policy *hex.SiteAccess) error {
	var envelope struct {
		Version int             `json:"version"`
		Policy  json.RawMessage `json:"policy"`
	}
	if err := json.Unmarshal(data, &envelope); err != nil {
		return err
	}
	if envelope.Version == 2 && len(envelope.Policy) > 0 {
		data = envelope.Policy
	}
	return json.Unmarshal(data, policy)
}

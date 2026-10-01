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
	if err := json.Unmarshal(data, &access); err != nil {
		return hex.SiteAccess{}, fmt.Errorf("decode site policy for %s: %w", site, err)
	}
	return hex.NormalizeSiteAccess(access), nil
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
		if err := json.Unmarshal(data, &current); err != nil {
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
		data, err = json.Marshal(hex.NormalizeSiteAccess(*next))
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

package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

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
	const query = `
		INSERT INTO hex_site_policies (site, policy)
		VALUES ($1, $2)
		ON CONFLICT (site)
		DO UPDATE SET policy = EXCLUDED.policy`

	data, err := json.Marshal(hex.NormalizeSiteAccess(access))
	if err != nil {
		return fmt.Errorf("encode site policy for %s: %w", site, err)
	}
	if _, err := d.pool.Exec(ctx, query, site, data); err != nil {
		return fmt.Errorf("save site policy for %s: %w", site, err)
	}

	return nil
}

func (d *Database) DeleteSiteAccess(ctx context.Context, site string) error {
	const query = `
		DELETE FROM hex_site_policies
		WHERE site = $1`

	result, err := d.pool.Exec(ctx, query, site)
	if err != nil {
		return fmt.Errorf("delete site policy for %s: %w", site, err)
	}
	if result.RowsAffected() == 0 {
		return hex.ErrNotFound
	}

	return nil
}

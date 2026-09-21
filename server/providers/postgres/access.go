package postgres

import (
	"context"
	"errors"
	"fmt"

	hex "github.com/crazycatviking/hex/server"
	"github.com/jackc/pgx/v5"
)

func (d *Database) GetSiteAccess(ctx context.Context, site string) (hex.SiteAccess, error) {
	const query = `
		SELECT owners, groups FROM hex_site_access
		WHERE site = $1`

	var access hex.SiteAccess
	err := d.pool.QueryRow(ctx, query, site).Scan(&access.Owners, &access.Groups)
	if errors.Is(err, pgx.ErrNoRows) {
		return hex.SiteAccess{}, hex.ErrNotFound
	}
	if err != nil {
		return hex.SiteAccess{}, fmt.Errorf("read site access for %s: %w", site, err)
	}

	return access, nil
}

func (d *Database) PutSiteAccess(ctx context.Context, site string, access hex.SiteAccess) error {
	const query = `
		INSERT INTO hex_site_access (site, owners, groups)
		VALUES ($1, $2, $3)
		ON CONFLICT (site)
		DO UPDATE SET owners = EXCLUDED.owners, groups = EXCLUDED.groups`

	if _, err := d.pool.Exec(ctx, query, site, access.Owners, access.Groups); err != nil {
		return fmt.Errorf("save site access for %s: %w", site, err)
	}

	return nil
}

func (d *Database) DeleteSiteAccess(ctx context.Context, site string) error {
	const query = `
		DELETE FROM hex_site_access
		WHERE site = $1`

	result, err := d.pool.Exec(ctx, query, site)
	if err != nil {
		return fmt.Errorf("delete site access for %s: %w", site, err)
	}
	if result.RowsAffected() == 0 {
		return hex.ErrNotFound
	}

	return nil
}

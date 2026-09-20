package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	hex "github.com/hex-platform/hex/server"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Database struct {
	pool *pgxpool.Pool
}

func New(ctx context.Context, connectionString string) (*Database, error) {
	pool, err := pgxpool.New(ctx, connectionString)
	if err != nil {
		return nil, fmt.Errorf("create PostgreSQL pool: %w", err)
	}

	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("connect to PostgreSQL: %w", err)
	}

	return &Database{pool: pool}, nil
}

func (d *Database) Close() {
	d.pool.Close()
}

func (d *Database) Migrate(ctx context.Context) error {
	const query = `
		CREATE TABLE IF NOT EXISTS hex_documents (
			site text NOT NULL,
			collection text NOT NULL,
			id text COLLATE "C" NOT NULL,
			data jsonb NOT NULL,
			PRIMARY KEY (site, collection, id)
		)`

	if _, err := d.pool.Exec(ctx, query); err != nil {
		return fmt.Errorf("create documents table: %w", err)
	}

	return nil
}

func (d *Database) Put(ctx context.Context, site, collection, id string, data json.RawMessage) error {
	const query = `
		INSERT INTO hex_documents (site, collection, id, data)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (site, collection, id)
		DO UPDATE SET data = EXCLUDED.data`

	if _, err := d.pool.Exec(ctx, query, site, collection, id, data); err != nil {
		return fmt.Errorf("save document %s/%s/%s: %w", site, collection, id, err)
	}

	return nil
}

func (d *Database) Get(ctx context.Context, site, collection, id string) (hex.Document, error) {
	const query = `
		SELECT data FROM hex_documents
		WHERE site = $1 AND collection = $2 AND id = $3`

	document := hex.Document{ID: id}
	err := d.pool.QueryRow(ctx, query, site, collection, id).Scan(&document.Data)
	if errors.Is(err, pgx.ErrNoRows) {
		return hex.Document{}, hex.ErrNotFound
	}
	if err != nil {
		return hex.Document{}, fmt.Errorf("read document %s/%s/%s: %w", site, collection, id, err)
	}

	return document, nil
}

func (d *Database) List(ctx context.Context, site, collection, after string, limit int) ([]hex.Document, error) {
	const query = `
		SELECT id, data FROM hex_documents
		WHERE site = $1 AND collection = $2 AND id > $3
		ORDER BY id
		LIMIT $4`

	rows, err := d.pool.Query(ctx, query, site, collection, after, limit)
	if err != nil {
		return nil, fmt.Errorf("list documents in %s/%s: %w", site, collection, err)
	}
	defer rows.Close()

	documents := []hex.Document{}
	for rows.Next() {
		var document hex.Document
		if err := rows.Scan(&document.ID, &document.Data); err != nil {
			return nil, fmt.Errorf("decode document row: %w", err)
		}
		documents = append(documents, document)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read document rows: %w", err)
	}

	return documents, nil
}

func (d *Database) Delete(ctx context.Context, site, collection, id string) error {
	const query = `
		DELETE FROM hex_documents
		WHERE site = $1 AND collection = $2 AND id = $3`

	result, err := d.pool.Exec(ctx, query, site, collection, id)
	if err != nil {
		return fmt.Errorf("delete document %s/%s/%s: %w", site, collection, id, err)
	}
	if result.RowsAffected() == 0 {
		return hex.ErrNotFound
	}

	return nil
}

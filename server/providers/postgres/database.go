package postgres

import (
	"context"
	"encoding/json"
	"errors"

	hex "github.com/hex-platform/hex/server"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Database struct{ pool *pgxpool.Pool }

func New(ctx context.Context, connectionString string) (*Database, error) {
	pool, err := pgxpool.New(ctx, connectionString)
	if err != nil {
		return nil, err
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, err
	}
	return &Database{pool: pool}, nil
}

func (d *Database) Close() { d.pool.Close() }

func (d *Database) Migrate(ctx context.Context) error {
	_, err := d.pool.Exec(ctx, `CREATE TABLE IF NOT EXISTS hex_documents (
        site text NOT NULL,
        collection text NOT NULL,
        id text COLLATE "C" NOT NULL,
        data jsonb NOT NULL,
        PRIMARY KEY (site, collection, id)
    )`)
	return err
}

func (d *Database) Put(ctx context.Context, site, collection, id string, data json.RawMessage) error {
	_, err := d.pool.Exec(ctx, `INSERT INTO hex_documents (site, collection, id, data) VALUES ($1,$2,$3,$4)
        ON CONFLICT (site, collection, id) DO UPDATE SET data = EXCLUDED.data`, site, collection, id, data)
	return err
}

func (d *Database) Get(ctx context.Context, site, collection, id string) (hex.Document, error) {
	doc := hex.Document{ID: id}
	err := d.pool.QueryRow(ctx, `SELECT data FROM hex_documents WHERE site=$1 AND collection=$2 AND id=$3`, site, collection, id).Scan(&doc.Data)
	if errors.Is(err, pgx.ErrNoRows) {
		return hex.Document{}, hex.ErrNotFound
	}
	return doc, err
}

func (d *Database) List(ctx context.Context, site, collection, after string, limit int) ([]hex.Document, error) {
	rows, err := d.pool.Query(ctx, `SELECT id,data FROM hex_documents WHERE site=$1 AND collection=$2 AND id>$3 ORDER BY id LIMIT $4`, site, collection, after, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	docs := []hex.Document{}
	for rows.Next() {
		var doc hex.Document
		if err := rows.Scan(&doc.ID, &doc.Data); err != nil {
			return nil, err
		}
		docs = append(docs, doc)
	}
	return docs, rows.Err()
}

func (d *Database) Delete(ctx context.Context, site, collection, id string) error {
	result, err := d.pool.Exec(ctx, `DELETE FROM hex_documents WHERE site=$1 AND collection=$2 AND id=$3`, site, collection, id)
	if err != nil {
		return err
	}
	if result.RowsAffected() == 0 {
		return hex.ErrNotFound
	}
	return nil
}

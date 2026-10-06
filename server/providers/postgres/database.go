package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	hex "github.com/crazycatviking/hex/server"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Database struct {
	pool           *pgxpool.Pool
	trafficCacheMu sync.Mutex
	trafficCache   map[string]cachedSiteTraffic
}

type cachedSiteTraffic struct {
	row     hex.AnalyticsRow
	expires time.Time
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
	return d.migrate(ctx, d.migrateDocuments, d.migratePeople, d.migrateAnalytics,
		d.migrateSitePolicies, d.migrateIntegrations, d.migrateAutomations,
		d.migrateAIUsage, d.migrateIntegrationAudit)
}

// MigratePlatform prepares the durable platform stores, independently of app documents.
func (d *Database) MigratePlatform(ctx context.Context) error {
	return d.migrate(ctx, d.migratePeople, d.migrateSitePolicies, d.migrateIntegrations,
		d.migrateAutomations, d.migrateAIUsage, d.migrateIntegrationAudit)
}

// All public migration entry points share this database-wide lock. PostgreSQL's
// IF NOT EXISTS does not serialize concurrent first-time CREATE TABLE/INDEX.
// Compositions call internal migrations so they acquire the global lock once.
func (d *Database) migrate(ctx context.Context, migrations ...func(context.Context) error) error {
	// Keep the lock outside the runtime pool: the internal migrations may open
	// transactions, and must work even when the pool has just one connection.
	connection, err := pgx.ConnectConfig(ctx, d.pool.Config().ConnConfig.Copy())
	if err != nil {
		return fmt.Errorf("connect for schema migration lock: %w", err)
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		// Closing this dedicated session releases its advisory lock, even after
		// cancellation or a failed migration. Never return a locked pooled session.
		if err := connection.Close(cleanup); err != nil {
			slog.Error("close schema migration lock connection", "error", err)
		}
	}()
	if _, err := connection.Exec(ctx, `SELECT pg_advisory_lock(hashtextextended('hex_schema_migration', 0))`); err != nil {
		return fmt.Errorf("lock schema migrations: %w", err)
	}
	for _, migrate := range migrations {
		if err := migrate(ctx); err != nil {
			return err
		}
	}
	return nil
}

func (d *Database) MigrateDocuments(ctx context.Context) error {
	return d.migrate(ctx, d.migrateDocuments)
}

func (d *Database) migrateDocuments(ctx context.Context) error {
	const documents = `
		CREATE TABLE IF NOT EXISTS hex_documents (
			site text NOT NULL,
			collection text NOT NULL,
			id text COLLATE "C" NOT NULL,
			data jsonb NOT NULL,
			PRIMARY KEY (site, collection, id)
		)`

	if _, err := d.pool.Exec(ctx, documents); err != nil {
		return fmt.Errorf("create documents table: %w", err)
	}

	const creators = `
		ALTER TABLE hex_documents
		ADD COLUMN IF NOT EXISTS created_by text NOT NULL DEFAULT ''`

	if _, err := d.pool.Exec(ctx, creators); err != nil {
		return fmt.Errorf("add document creators: %w", err)
	}

	const creatorIndex = `
		CREATE INDEX IF NOT EXISTS hex_documents_creator
		ON hex_documents (site, collection, created_by, id)`

	if _, err := d.pool.Exec(ctx, creatorIndex); err != nil {
		return fmt.Errorf("index document creators: %w", err)
	}
	return nil
}

func (d *Database) MigratePeople(ctx context.Context) error {
	return d.migrate(ctx, d.migratePeople)
}

func (d *Database) migratePeople(ctx context.Context) error {
	const people = `
		CREATE TABLE IF NOT EXISTS hex_people (
			id text PRIMARY KEY,
			name text NOT NULL,
			email text NOT NULL,
			last_seen timestamptz NOT NULL
		)`

	if _, err := d.pool.Exec(ctx, people); err != nil {
		return fmt.Errorf("create people table: %w", err)
	}

	return nil
}

// MigrateAnalytics creates only analytics tables and imports an existing people
// directory when present, without requiring that store on this connection.
func (d *Database) MigrateAnalytics(ctx context.Context) error {
	return d.migrate(ctx, d.migrateAnalytics)
}
func (d *Database) MigrateAccess(ctx context.Context) error {
	return d.migrate(ctx, d.migrateSitePolicies)
}
func (d *Database) MigrateIntegrations(ctx context.Context) error {
	return d.migrate(ctx, d.migrateIntegrations)
}
func (d *Database) MigrateAutomations(ctx context.Context) error {
	return d.migrate(ctx, d.migrateAutomations)
}
func (d *Database) MigrateAIUsage(ctx context.Context) error {
	return d.migrate(ctx, d.migrateAIUsage)
}
func (d *Database) MigrateIntegrationAudit(ctx context.Context) error {
	return d.migrate(ctx, d.migrateIntegrationAudit)
}

// migrateSitePolicies creates the policy table and converts entries from the
// former owners/groups table, whose groups become the policy's viewers.
func (d *Database) migrateSitePolicies(ctx context.Context) error {
	const policies = `
		CREATE TABLE IF NOT EXISTS hex_site_policies (
			site text PRIMARY KEY,
			policy jsonb NOT NULL
		)`

	if _, err := d.pool.Exec(ctx, policies); err != nil {
		return fmt.Errorf("create site policy table: %w", err)
	}

	var legacyTable *string
	if err := d.pool.QueryRow(ctx, `SELECT to_regclass('hex_site_access')::text`).Scan(&legacyTable); err != nil {
		return fmt.Errorf("look up legacy site access table: %w", err)
	}
	if legacyTable == nil {
		return nil
	}

	const convert = `
		INSERT INTO hex_site_policies (site, policy)
		SELECT site, jsonb_build_object('owners', owners, 'editors', '[]'::jsonb, 'viewers', groups)
		FROM hex_site_access
		ON CONFLICT (site) DO NOTHING`

	transaction, err := d.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin site access migration: %w", err)
	}
	defer func() {
		if err := transaction.Rollback(ctx); err != nil && !errors.Is(err, pgx.ErrTxClosed) {
			slog.Error("roll back site access migration", "error", err)
		}
	}()

	if _, err := transaction.Exec(ctx, convert); err != nil {
		return fmt.Errorf("convert legacy site access entries: %w", err)
	}
	if _, err := transaction.Exec(ctx, `DROP TABLE hex_site_access`); err != nil {
		return fmt.Errorf("drop legacy site access table: %w", err)
	}
	if err := transaction.Commit(ctx); err != nil {
		return fmt.Errorf("commit site access migration: %w", err)
	}
	return nil
}

func (d *Database) Put(ctx context.Context, site, collection, id string, data json.RawMessage, options hex.WriteOptions) (hex.Document, error) {
	// The conditional update leaves other creators' documents untouched and
	// then returns no row, which is how creator-only writes are refused.
	const query = `
		INSERT INTO hex_documents (site, collection, id, data, created_by)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (site, collection, id)
		DO UPDATE SET data = EXCLUDED.data
		WHERE NOT $6 OR hex_documents.created_by = $5
		RETURNING created_by`

	document := hex.Document{ID: id, Data: data}
	err := d.pool.QueryRow(ctx, query, site, collection, id, data, options.Creator, options.CreatorOnly).Scan(&document.CreatedBy)
	if errors.Is(err, pgx.ErrNoRows) {
		return hex.Document{}, hex.ErrForbidden
	}
	if err != nil {
		return hex.Document{}, fmt.Errorf("save document %s/%s/%s: %w", site, collection, id, err)
	}

	return document, nil
}

func (d *Database) Get(ctx context.Context, site, collection, id string) (hex.Document, error) {
	const query = `
		SELECT data, created_by FROM hex_documents
		WHERE site = $1 AND collection = $2 AND id = $3`

	document := hex.Document{ID: id}
	err := d.pool.QueryRow(ctx, query, site, collection, id).Scan(&document.Data, &document.CreatedBy)
	if errors.Is(err, pgx.ErrNoRows) {
		return hex.Document{}, hex.ErrNotFound
	}
	if err != nil {
		return hex.Document{}, fmt.Errorf("read document %s/%s/%s: %w", site, collection, id, err)
	}

	return document, nil
}

func (d *Database) List(ctx context.Context, site, collection string, options hex.ListOptions) ([]hex.Document, error) {
	const query = `
		SELECT id,
		  CASE WHEN $6 = 0 OR octet_length(data::text) <= $6 THEN data ELSE NULL END,
		  created_by FROM hex_documents
		WHERE site = $1 AND collection = $2 AND id > $3
		AND ($5 = '' OR created_by = $5)
		ORDER BY id
		LIMIT $4`

	rows, err := d.pool.Query(ctx, query, site, collection, options.After, options.Limit, options.CreatedBy, options.MaxDocumentBytes)
	if err != nil {
		return nil, fmt.Errorf("list documents in %s/%s: %w", site, collection, err)
	}
	defer rows.Close()

	documents := []hex.Document{}
	for rows.Next() {
		var document hex.Document
		if err := rows.Scan(&document.ID, &document.Data, &document.CreatedBy); err != nil {
			return nil, fmt.Errorf("decode document row: %w", err)
		}
		if document.Data == nil {
			return nil, hex.ErrDocumentReadLimit
		}
		documents = append(documents, document)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read document rows: %w", err)
	}

	return documents, nil
}

func (d *Database) Delete(ctx context.Context, site, collection, id string, options hex.WriteOptions) error {
	const query = `
		DELETE FROM hex_documents
		WHERE site = $1 AND collection = $2 AND id = $3
		AND (NOT $4 OR created_by = $5)`

	result, err := d.pool.Exec(ctx, query, site, collection, id, options.CreatorOnly, options.Creator)
	if err != nil {
		return fmt.Errorf("delete document %s/%s/%s: %w", site, collection, id, err)
	}
	if result.RowsAffected() > 0 {
		return nil
	}

	if _, err := d.Get(ctx, site, collection, id); err != nil {
		return err
	}
	return hex.ErrForbidden
}

func (d *Database) ListCollections(ctx context.Context, site string) ([]hex.Collection, error) {
	const query = `
		SELECT collection, count(*) FROM hex_documents
		WHERE site = $1
		GROUP BY collection
		ORDER BY collection`

	rows, err := d.pool.Query(ctx, query, site)
	if err != nil {
		return nil, fmt.Errorf("list collections of %s: %w", site, err)
	}
	defer rows.Close()

	collections := []hex.Collection{}
	for rows.Next() {
		var collection hex.Collection
		if err := rows.Scan(&collection.Name, &collection.Documents); err != nil {
			return nil, fmt.Errorf("decode collection row: %w", err)
		}
		collections = append(collections, collection)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read collection rows: %w", err)
	}
	return collections, nil
}

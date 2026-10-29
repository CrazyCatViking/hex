package hex_test

import (
	"context"
	"crypto/rand"
	"net/url"
	"os"
	"strings"
	"testing"

	hex "github.com/crazycatviking/hex/server"
	"github.com/crazycatviking/hex/server/providers/memory"
	"github.com/crazycatviking/hex/server/providers/postgres"
	"github.com/jackc/pgx/v5"
)

type durableTestDatabase interface {
	hex.Database
	hex.GuardedDatabase
}

// The same end-to-end scenarios run against an isolated PostgreSQL schema when
// requested, including the real policy and automation stores.
func durableTestStores(t *testing.T) (durableTestDatabase, hex.AccessStore, hex.AutomationStore) {
	t.Helper()
	connection := os.Getenv("HEX_TEST_DURABLE_POSTGRES_URL")
	if connection == "" {
		return memory.NewDatabase(), memory.NewAccessStore(), memory.NewAutomationStore()
	}
	ctx := context.Background()
	admin, err := pgx.Connect(ctx, connection)
	if err != nil {
		t.Fatal(err)
	}
	schema := "durable_" + strings.ToLower(rand.Text())
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+pgx.Identifier{schema}.Sanitize()); err != nil {
		t.Fatal(err)
	}
	parsed, err := url.Parse(connection)
	if err != nil {
		t.Fatal(err)
	}
	query := parsed.Query()
	query.Set("search_path", schema)
	parsed.RawQuery = query.Encode()
	database, err := postgres.New(ctx, parsed.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		database.Close()
		if _, err := admin.Exec(ctx, "DROP SCHEMA "+pgx.Identifier{schema}.Sanitize()+" CASCADE"); err != nil {
			t.Error(err)
		}
		if err := admin.Close(ctx); err != nil {
			t.Error(err)
		}
	})
	if err := database.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	return database, database, database
}

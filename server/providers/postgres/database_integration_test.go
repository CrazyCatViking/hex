package postgres

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"os"
	"testing"
	"time"

	hex "github.com/crazycatviking/hex/server"
)

func TestPostgresDatabase(t *testing.T) {
	connection := os.Getenv("HEX_TEST_POSTGRES_URL")
	if connection == "" {
		t.Skip("set HEX_TEST_POSTGRES_URL to run against PostgreSQL")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	database, err := New(ctx, connection)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if err := database.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	site := "test-" + rand.Text()
	defer func() {
		cleanup, stop := context.WithTimeout(context.Background(), 10*time.Second)
		defer stop()
		if _, err := database.pool.Exec(cleanup, "DELETE FROM hex_documents WHERE site=$1", site); err != nil {
			t.Error(err)
		}
	}()

	for _, id := range []string{"a", "b", "c"} {
		if err := database.Put(ctx, site, "notes", id, json.RawMessage(`{"value":"initial"}`)); err != nil {
			t.Fatal(err)
		}
	}
	if err := database.Put(ctx, site, "notes", "b", json.RawMessage(`{"updated":true}`)); err != nil {
		t.Fatal(err)
	}
	reopened, err := New(ctx, connection)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	document, err := reopened.Get(ctx, site, "notes", "b")
	if err != nil {
		t.Fatal(err)
	}
	var data map[string]any
	if err := json.Unmarshal(document.Data, &data); err != nil {
		t.Fatal(err)
	}
	if len(data) != 1 || data["updated"] != true {
		t.Fatal("document replacement did not persist", data)
	}
	documents, err := database.List(ctx, site, "notes", "a", 1)
	if err != nil || len(documents) != 1 || documents[0].ID != "b" {
		t.Fatalf("unexpected page: %v %v", documents, err)
	}
	documents, err = database.List(ctx, site, "other", "", 100)
	if err != nil || len(documents) != 0 {
		t.Fatalf("collection isolation failed: %v %v", documents, err)
	}
	if err := database.Delete(ctx, site, "notes", "b"); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Get(ctx, site, "notes", "b"); !errors.Is(err, hex.ErrNotFound) {
		t.Fatalf("expected not found, got %v", err)
	}
}

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
		if _, err := database.Put(ctx, site, "notes", id, json.RawMessage(`{"value":"initial"}`), hex.WriteOptions{Creator: "alice"}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := database.Put(ctx, site, "notes", "b", json.RawMessage(`{"blocked":true}`), hex.WriteOptions{Creator: "bob", CreatorOnly: true}); !errors.Is(err, hex.ErrForbidden) {
		t.Fatalf("creator-only write by another user: got %v", err)
	}
	stored, err := database.Put(ctx, site, "notes", "b", json.RawMessage(`{"updated":true}`), hex.WriteOptions{Creator: "bob"})
	if err != nil {
		t.Fatal(err)
	}
	if stored.CreatedBy != "alice" {
		t.Fatalf("replacement changed the creator: %+v", stored)
	}
	if _, err := database.Put(ctx, site, "notes", "d", json.RawMessage(`{"owner":"bob"}`), hex.WriteOptions{Creator: "bob", CreatorOnly: true}); err != nil {
		t.Fatal(err)
	}
	own, err := database.List(ctx, site, "notes", hex.ListOptions{Limit: 100, CreatedBy: "bob"})
	if err != nil || len(own) != 1 || own[0].ID != "d" {
		t.Fatalf("creator filter failed: %v %v", own, err)
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
	documents, err := database.List(ctx, site, "notes", hex.ListOptions{After: "a", Limit: 1})
	if err != nil || len(documents) != 1 || documents[0].ID != "b" {
		t.Fatalf("unexpected page: %v %v", documents, err)
	}
	documents, err = database.List(ctx, site, "other", hex.ListOptions{Limit: 100})
	if err != nil || len(documents) != 0 {
		t.Fatalf("collection isolation failed: %v %v", documents, err)
	}
	if err := database.Delete(ctx, site, "notes", "b", hex.WriteOptions{Creator: "bob", CreatorOnly: true}); !errors.Is(err, hex.ErrForbidden) {
		t.Fatalf("creator-only delete by another user: got %v", err)
	}
	if err := database.Delete(ctx, site, "notes", "b", hex.WriteOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Get(ctx, site, "notes", "b"); !errors.Is(err, hex.ErrNotFound) {
		t.Fatalf("expected not found, got %v", err)
	}
}

func TestPostgresSiteAccess(t *testing.T) {
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
		if _, err := database.pool.Exec(cleanup, "DELETE FROM hex_site_policies WHERE site=$1", site); err != nil {
			t.Error(err)
		}
	}()

	if _, err := database.GetSiteAccess(ctx, site); !errors.Is(err, hex.ErrNotFound) {
		t.Fatalf("expected not found, got %v", err)
	}
	entry := hex.SiteAccess{
		Owners:      []string{"user:owner-id"},
		Viewers:     []string{"group:sales", "group:ops"},
		Paths:       []hex.PathRule{{Prefix: "/admin/", Viewers: hex.Audience{Level: hex.LevelOwners}}},
		Collections: map[string]hex.DataRule{"drafts": {Read: hex.Audience{Level: hex.LevelCreator}}},
	}
	if err := database.PutSiteAccess(ctx, site, entry); err != nil {
		t.Fatal(err)
	}
	replacement := hex.SiteAccess{
		Owners:      []string{"user:owner-id", "user:backup-id"},
		Collections: map[string]hex.DataRule{"drafts": {Read: hex.Audience{Level: hex.LevelCreator}}},
	}
	if err := database.PutSiteAccess(ctx, site, replacement); err != nil {
		t.Fatal(err)
	}

	reopened, err := New(ctx, connection)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	access, err := reopened.GetSiteAccess(ctx, site)
	if err != nil {
		t.Fatal(err)
	}
	if len(access.Owners) != 2 || access.Owners[1] != "user:backup-id" || len(access.Viewers) != 0 || access.Collections["drafts"].Read.Level != hex.LevelCreator {
		t.Fatalf("unexpected access entry: %+v", access)
	}

	if err := database.DeleteSiteAccess(ctx, site); err != nil {
		t.Fatal(err)
	}
	if err := database.DeleteSiteAccess(ctx, site); !errors.Is(err, hex.ErrNotFound) {
		t.Fatalf("expected not found, got %v", err)
	}
}

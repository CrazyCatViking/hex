package local

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	hex "github.com/crazycatviking/hex/server"
)

func TestSiteDiscoveryIgnoresSymlinksAndDoesNotCreateMetadata(t *testing.T) {
	root := t.TempDir()
	store, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	defer closeResource(t, store)

	sites := filepath.Join(root, "public", "sites")
	for _, name := range []string{"real", "linked-index", "incomplete"} {
		if err := os.MkdirAll(filepath.Join(sites, name), 0750); err != nil {
			t.Fatal(err)
		}
	}
	index := filepath.Join(sites, "real", "index.html")
	if err := os.WriteFile(index, []byte("hello"), 0640); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(sites, "real"), filepath.Join(sites, "linked-site")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(index, filepath.Join(sites, "linked-index", "index.html")); err != nil {
		t.Fatal(err)
	}

	names, err := store.ListSites(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(names) != 1 || names[0] != "real" {
		t.Fatalf("unexpected sites: %v", names)
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "public" {
		t.Fatal("discovery wrote files outside the public site directory")
	}
}

func TestSiteMetadata(t *testing.T) {
	root := t.TempDir()
	store, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	defer closeResource(t, store)
	ctx := context.Background()
	if metadata, err := store.ReadSiteMetadata(ctx, "legacy"); err != nil || metadata != nil {
		t.Fatalf("sites without metadata must remain valid: %+v %v", metadata, err)
	}
	directory := filepath.Join(root, "public", "sites", "demo")
	if err := os.MkdirAll(directory, 0755); err != nil {
		t.Fatal(err)
	}
	expected := hex.SiteMetadata{Title: "Dashboard", Author: "Alex", PublishedAt: time.Now().UTC()}
	data, err := json.Marshal(expected)
	if err != nil {
		t.Fatal(err)
	}
	filename := filepath.Join(directory, ".hex-site.json")
	if err := os.WriteFile(filename, data, 0644); err != nil {
		t.Fatal(err)
	}
	metadata, err := store.ReadSiteMetadata(ctx, "demo")
	if err != nil || metadata == nil || *metadata != expected {
		t.Fatalf("unexpected metadata: %+v %v", metadata, err)
	}
	for _, invalid := range [][]byte{[]byte("invalid JSON"), make([]byte, 64*1024+1)} {
		if err := os.WriteFile(filename, invalid, 0644); err != nil {
			t.Fatal(err)
		}
		if _, err := store.ReadSiteMetadata(ctx, "demo"); err == nil {
			t.Fatal("invalid metadata accepted")
		}
	}
	if err := os.Remove(filename); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "private.json"), filename); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ReadSiteMetadata(ctx, "demo"); err == nil {
		t.Fatal("symlink metadata accepted")
	}
	if _, err := store.ReadSiteMetadata(ctx, "../outside"); err == nil {
		t.Fatal("invalid site name accepted")
	}
}

package local

import (
	"context"
	"os"
	"path/filepath"
	"testing"
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

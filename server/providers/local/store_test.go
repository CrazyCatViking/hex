package local

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func closeResource(t *testing.T, resource io.Closer) {
	t.Helper()
	if err := resource.Close(); err != nil {
		t.Errorf("close test resource: %v", err)
	}
}

func TestTraversalAndSymlinkEscape(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()

	if err := os.WriteFile(filepath.Join(outside, "secret"), []byte("secret"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	s, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	defer closeResource(t, s)

	for _, key := range []string{"../secret", "/secret", "link/secret"} {
		if f, err := s.Open(context.Background(), key); err == nil {
			closeResource(t, f)
			t.Fatalf("opened %s", key)
		}
		if err := s.Put(context.Background(), key, strings.NewReader("overwrite")); err == nil {
			t.Fatalf("wrote %s", key)
		}
	}
}

type brokenReader struct{}

func (brokenReader) Read([]byte) (int, error) {
	return 0, errors.New("broken")
}

func TestFailedWriteKeepsOldObject(t *testing.T) {
	s, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer closeResource(t, s)

	ctx := context.Background()
	if err := s.Put(ctx, "site/key", strings.NewReader("original")); err != nil {
		t.Fatal(err)
	}
	if err := s.Put(ctx, "site/key", brokenReader{}); err == nil {
		t.Fatal("expected failure")
	}
	f, err := s.Open(ctx, "site/key")
	if err != nil {
		t.Fatal(err)
	}
	defer closeResource(t, f)

	data, err := io.ReadAll(f)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "original" {
		t.Fatalf("got %q, want original content", data)
	}

	objects, err := s.List(ctx, "site/")
	if err != nil {
		t.Fatal(err)
	}
	if len(objects) != 1 {
		t.Fatalf("got %d objects, want 1", len(objects))
	}
}

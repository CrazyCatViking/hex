package local

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/iotest"

	hex "github.com/crazycatviking/hex/server"
)

func TestWriteSiteFileReplacement(t *testing.T) {
	readError := errors.New("source failed")
	for _, test := range []struct {
		name      string
		size      int64
		source    io.Reader
		want      string
		wantError error
	}{
		{name: "short", size: 4, source: strings.NewReader("new"), want: "original"},
		{name: "overlong", size: 2, source: strings.NewReader("new"), want: "original"},
		{name: "partial copy failure", size: 4, source: io.MultiReader(strings.NewReader("new"), iotest.ErrReader(readError)), want: "original", wantError: readError},
		{name: "failure after expected bytes", size: 3, source: io.MultiReader(strings.NewReader("new"), iotest.ErrReader(readError)), want: "original", wantError: readError},
		{name: "correct", size: 3, source: strings.NewReader("new"), want: "new"},
		{name: "empty", size: 0, source: strings.NewReader(""), want: ""},
		{name: "overlong empty", size: 0, source: strings.NewReader("new"), want: "original"},
		{name: "negative size", size: -1, source: strings.NewReader("new"), want: "original", wantError: fs.ErrInvalid},
		{name: "maximum size", size: math.MaxInt64, source: strings.NewReader("new"), want: "original"},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			store, err := New(root)
			if err != nil {
				t.Fatal(err)
			}
			defer closeResource(t, store)

			ctx := context.Background()
			if err := store.WriteSiteFile(ctx, "demo", "index.html", 8, strings.NewReader("original")); err != nil {
				t.Fatal(err)
			}
			err = store.WriteSiteFile(ctx, "demo", "index.html", test.size, test.source)
			if test.want == "original" {
				if err == nil {
					t.Fatal("invalid replacement succeeded")
				}
			} else if err != nil {
				t.Fatalf("valid replacement failed: %v", err)
			}
			if test.wantError != nil && !errors.Is(err, test.wantError) {
				t.Fatalf("got error %v, want %v", err, test.wantError)
			}

			file, err := store.ReadSiteFile(ctx, "demo", "index.html")
			if err != nil {
				t.Fatal(err)
			}
			data, err := io.ReadAll(file)
			closeResource(t, file)
			if err != nil {
				t.Fatal(err)
			}
			if string(data) != test.want {
				t.Fatalf("got content %q, want %q", data, test.want)
			}

			entries, err := os.ReadDir(filepath.Join(root, "public", "sites", "demo"))
			if err != nil {
				t.Fatal(err)
			}
			if len(entries) != 1 || entries[0].Name() != "index.html" {
				t.Fatalf("unexpected files after replacement: %v", entries)
			}
		})
	}
}

func TestReadPublishedFileRejectsSymlinks(t *testing.T) {
	root := t.TempDir()
	store, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	defer closeResource(t, store)
	ctx := context.Background()
	if err := store.WriteSiteFile(ctx, "private", "icons/logo.svg", 6, strings.NewReader("secret")); err != nil {
		t.Fatal(err)
	}
	if err := store.WriteSiteFile(ctx, "public", "index.html", 3, strings.NewReader("app")); err != nil {
		t.Fatal(err)
	}
	private := filepath.Join(root, "public", "sites", "private", "icons")
	public := filepath.Join(root, "public", "sites", "public")
	for link, target := range map[string]string{"favicon.svg": filepath.Join(private, "logo.svg"), "assets": private} {
		if err := os.Symlink(target, filepath.Join(public, link)); err != nil {
			t.Fatal(err)
		}
	}
	for _, file := range []string{"favicon.svg", "assets/logo.svg"} {
		reader, err := store.ReadSiteFile(ctx, "public", file)
		if reader != nil {
			closeResource(t, reader)
		}
		if !errors.Is(err, hex.ErrNotFound) {
			t.Fatalf("read through published symlink %s: %v", file, err)
		}
	}
}

func TestWriteSiteFileLimitsOverlongInput(t *testing.T) {
	store, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer closeResource(t, store)

	source := strings.NewReader(strings.Repeat("x", 1024*1024))
	initialSize := source.Len()
	if err := store.WriteSiteFile(context.Background(), "demo", "index.html", 3, source); err == nil {
		t.Fatal("overlong input accepted")
	}
	if consumed := initialSize - source.Len(); consumed > 4 {
		t.Fatalf("consumed %d bytes for a 3-byte file", consumed)
	}
}

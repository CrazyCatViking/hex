package cli

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func writePublishFixture(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for name, content := range files {
		path := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
	}
}

func TestPublishDirectorySelection(t *testing.T) {
	for _, test := range []struct {
		name       string
		files      map[string]string
		configured string
		want       string
		errorText  string
	}{
		{name: "plain HTML", files: map[string]string{"index.html": "plain"}, want: "."},
		{name: "public directory", files: map[string]string{"public/index.html": "public", "index.html": "root"}, want: "public"},
		{name: "build output wins", files: map[string]string{"dist/index.html": "built", "public/index.html": "public", "index.html": "root"}, want: "dist"},
		{name: "empty dist falls back", files: map[string]string{"dist/assets/app.js": "asset", "index.html": "root"}, want: "."},
		{name: "build required", files: map[string]string{"index.html": "source", "public/index.html": "unbuilt", "package.json": `{"scripts":{"build":"vite build"}}`}, errorText: "build command first"},
		{name: "dependencies without build", files: map[string]string{"index.html": "plain", "package.json": `{"dependencies":{}}`}, want: "."},
		{name: "explicit root", files: map[string]string{"index.html": "root", "dist/index.html": "built", "package.json": `{"scripts":{"build":"custom"}}`}, configured: ".", want: "."},
		{name: "explicit output", files: map[string]string{"output/index.html": "output", "package.json": `{"scripts":{"build":"custom"}}`}, configured: "output", want: "output"},
		{name: "explicit missing directory", files: map[string]string{"index.html": "root"}, configured: "dist", errorText: "open publish directory"},
		{name: "no index", files: map[string]string{"app.js": "app"}, errorText: "no site found"},
		{name: "invalid package", files: map[string]string{"index.html": "root", "package.json": "invalid"}, errorText: "parse package.json"},
	} {
		t.Run(test.name, func(t *testing.T) {
			project := t.TempDir()
			writePublishFixture(t, project, test.files)
			source, err := readSource(project, test.configured)
			if test.errorText != "" {
				if err == nil || !strings.Contains(err.Error(), test.errorText) {
					t.Fatalf("expected %q, got %v", test.errorText, err)
				}
				return
			}
			expected, resolveError := filepath.EvalSymlinks(filepath.Join(project, test.want))
			if err != nil || resolveError != nil || source.Directory != expected {
				t.Fatalf("unexpected source: %s, errors: %v %v", source.Directory, err, resolveError)
			}
		})
	}
}

func TestPublishPlainSiteExcludesProjectFiles(t *testing.T) {
	directory := t.TempDir()
	t.Setenv("HEX_CONFIG_DIR", filepath.Join(directory, "profiles"))
	project := filepath.Join(directory, "demo")
	destination := filepath.Join(directory, "published")
	run(t, directory, "init", project, "--publish-root", destination)
	assets := map[string]string{
		"index.html":      "<script src='./app.js'></script>",
		"app.js":          "console.log('plain site')",
		"styles/main.css": "body { color: green; }",
		"images/icon.bin": "\x00\xff\x01",
	}
	writePublishFixture(t, project, assets)
	excluded := map[string]string{
		"hex.dev.json":                  "local settings",
		"AGENTS.md":                     "private instructions",
		"package.json":                  `{"scripts":{"test":"custom"}}`,
		"package-lock.json":             "lockfile",
		".env":                          "secret",
		".git/config":                   "repository",
		"node_modules/library/index.js": "dependency",
		"nested/.secret":                "hidden",
	}
	writePublishFixture(t, project, excluded)
	run(t, project, "publish")
	for name, want := range assets {
		data, err := os.ReadFile(filepath.Join(destination, "demo", filepath.FromSlash(name)))
		if err != nil || string(data) != want {
			t.Fatalf("asset %s was not preserved: %v", name, err)
		}
	}
	excluded["hex.json"] = ""
	excluded[".agents/skills/hex/SKILL.md"] = ""
	for name := range excluded {
		if _, err := os.Stat(filepath.Join(destination, "demo", filepath.FromSlash(name))); !os.IsNotExist(err) {
			t.Fatalf("project file %s was published: %v", name, err)
		}
	}
	if err := os.Remove(filepath.Join(project, "app.js")); err != nil {
		t.Fatal(err)
	}
	run(t, project, "publish")
	if _, err := os.Stat(filepath.Join(destination, "demo", "app.js")); !os.IsNotExist(err) {
		t.Fatalf("stale asset survived republishing: %v", err)
	}
}

func TestRootPublishingRetainsPathBoundaries(t *testing.T) {
	directory := t.TempDir()
	project := filepath.Join(directory, "project")
	writePublishFixture(t, project, map[string]string{"index.html": "site"})
	writePublishFixture(t, directory, map[string]string{"outside/index.html": "outside"})
	if _, err := readSource(project, "../outside"); err == nil {
		t.Fatal("publishing outside the project was allowed")
	}
	source, err := readSource(project, ".")
	if err != nil {
		t.Fatal(err)
	}
	if err := syncFilesystem(context.Background(), source, filepath.Join(project, "published")); err == nil {
		t.Fatal("overlapping root publication was allowed")
	}
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation requires Windows privileges")
	}
	if err := os.Symlink(filepath.Join(directory, "outside"), filepath.Join(project, "dist")); err != nil {
		t.Fatal(err)
	}
	if _, err := readSource(project, ""); err == nil {
		t.Fatal("auto-detection followed an outside build directory")
	}
	if err := os.Remove(filepath.Join(project, "dist")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(directory, "outside", "index.html"), filepath.Join(project, "leak.html")); err != nil {
		t.Fatal(err)
	}
	if _, err := readSource(project, "."); err == nil {
		t.Fatal("root publishing accepted a symlink asset")
	}
}

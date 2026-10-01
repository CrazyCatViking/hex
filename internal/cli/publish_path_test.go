package cli

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	hex "github.com/crazycatviking/hex/server"
	"github.com/crazycatviking/hex/server/providers/memory"
)

func TestPublishRejectsExplicitFileSymlinks(t *testing.T) {
	directory := t.TempDir()
	requests := 0
	platform := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		t.Errorf("symlink publication reached the API: %s %s", r.Method, r.URL)
	}))
	defer platform.Close()
	saveReadProfile(t, directory, platform.URL)
	writePublishFixture(t, directory, map[string]string{
		"notes.txt": "ordinary text", ".env": "SECRET=private", "private.key": "private key",
	})
	for i, target := range []string{"notes.txt", ".env", "private.key"} {
		t.Run(target, func(t *testing.T) {
			link := fmt.Sprintf("share-%d.txt", i)
			path := filepath.Join(directory, link)
			if err := os.Symlink(filepath.Join(directory, target), path); err != nil {
				t.Fatal(err)
			}
			temporary := t.TempDir()
			if _, err := shareFiles(path, "Title", temporary); err == nil || !strings.Contains(err.Error(), "symlinks") {
				t.Fatalf("symlink preview accepted: %v", err)
			}
			entries, err := os.ReadDir(temporary)
			if err != nil || len(entries) != 0 {
				t.Fatalf("symlink created a preview: %v %v", entries, err)
			}
			app, output := testApp(t, directory)
			if err := app.Execute(context.Background(), []string{"publish", link, "--yes"}, "test"); err == nil || !strings.Contains(err.Error(), "symlinks") {
				t.Fatalf("symlink publication accepted: %v", err)
			}
			if output.Len() != 0 {
				t.Fatalf("symlink generated publication output: %s", output)
			}
		})
	}
	if requests != 0 {
		t.Fatal("symlink created a network artifact")
	}
}

var artifactURL = regexp.MustCompile(`http://([a-z][a-z0-9]{9})\.localhost:8080/`)

func sharePlatform(t *testing.T) testPlatform {
	t.Helper()
	t.Setenv("HEX_CONFIG_DIR", filepath.Join(t.TempDir(), "profiles"))
	platform := startPlatform(t, func(config *hex.Config) {
		config.Identity = hex.StaticIdentity{Identity: hex.Identity{ID: "alex", Name: "Alex"}}
		config.Access = memory.NewAccessStore()
	})
	run(t, t.TempDir(), "setup", platform.URL, "--name", "company", "--json")
	return platform
}

// shared publishes a file or folder without hex.json and returns its site
// name.
func shared(t *testing.T, directory string, args ...string) string {
	t.Helper()
	output := run(t, directory, append([]string{"publish"}, args...)...)
	match := artifactURL.FindStringSubmatch(output)
	if match == nil {
		t.Fatalf("no artifact URL in %q", output)
	}
	return match[1]
}

func readShared(t *testing.T, platform testPlatform, name, file string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(platform.Sites, name, filepath.FromSlash(file)))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestPublishFilesWithGeneratedPages(t *testing.T) {
	platform := sharePlatform(t)
	directory := t.TempDir()
	writePublishFixture(t, directory, map[string]string{
		"report.pdf":         "%PDF-1.7",
		"notes.txt":          "<script>alert(1)</script> & more",
		"page.html":          "<h1>Hand-written</h1>",
		"bundle/data.csv":    "a,b",
		"bundle/img/x.png":   "png",
		"bundle/my file.txt": "spaces",
	})

	pdf := shared(t, directory, "report.pdf", "--name", "Q3 report")
	index := readShared(t, platform, pdf, "index.html")
	if readShared(t, platform, pdf, "report.pdf") != "%PDF-1.7" || !strings.Contains(index, "<title>Q3 report</title>") || !strings.Contains(index, `<iframe src="./report.pdf"`) {
		t.Fatalf("PDF artifact page: %s", index)
	}

	text := readShared(t, platform, shared(t, directory, "notes.txt"), "index.html")
	if strings.Contains(text, "<script>") || !strings.Contains(text, "&lt;script&gt;alert(1)&lt;/script&gt; &amp; more") {
		t.Fatalf("text preview was not escaped: %s", text)
	}

	if page := readShared(t, platform, shared(t, directory, "page.html"), "index.html"); page != "<h1>Hand-written</h1>" {
		t.Fatalf("HTML files are published as the page: %s", page)
	}

	bundle := shared(t, directory, "bundle")
	listing := readShared(t, platform, bundle, "index.html")
	for _, link := range []string{`href="./data.csv"`, `href="./img/x.png"`, `href="./my%20file.txt"`, "3 files"} {
		if !strings.Contains(listing, link) {
			t.Fatalf("listing lacks %s: %s", link, listing)
		}
	}
	if readShared(t, platform, bundle, "img/x.png") != "png" {
		t.Fatal("folder contents were not published")
	}

	artifacts := run(t, directory, "sites", "--mine")
	for _, expected := range []string{pdf, "Q3 report", "File", "Only owners", bundle} {
		if !strings.Contains(artifacts, expected) {
			t.Fatalf("artifact list lacks %q: %s", expected, artifacts)
		}
	}
}

func TestPublishPathsWithOthersAndUpdate(t *testing.T) {
	platform := sharePlatform(t)
	directory := t.TempDir()
	writePublishFixture(t, directory, map[string]string{"report.pdf": "v1"})

	name := shared(t, directory, "report.pdf", "-n", "Q3 report", "--with", "group:sales")
	policy := run(t, directory, "access", "show", name)
	if !strings.Contains(policy, `"user:alex"`) || !strings.Contains(policy, `"group:sales"`) {
		t.Fatalf("sharing did not keep the creator and add the group: %s", policy)
	}

	writePublishFixture(t, directory, map[string]string{"report.pdf": "v2"})
	if updated := shared(t, directory, "report.pdf", "--update", name); updated != name {
		t.Fatalf("update moved the artifact to %s", updated)
	}
	if readShared(t, platform, name, "report.pdf") != "v2" || !strings.Contains(readShared(t, platform, name, "index.html"), "<title>Q3 report</title>") {
		t.Fatal("update did not replace the content or lost the title")
	}
	if policy := run(t, directory, "access", "show", name); !strings.Contains(policy, `"group:sales"`) {
		t.Fatalf("an update without --with changed who can see it: %s", policy)
	}

	app, _ := testApp(t, directory)
	if err := app.Execute(context.Background(), []string{"publish", "report.pdf", "--with", "sales"}, "test"); err == nil || !strings.Contains(err.Error(), "prefix") {
		t.Fatalf("an untyped --with value was accepted: %v", err)
	}

	// Artifacts can be deleted by name without a project file.
	run(t, directory, "delete", name, "--yes")
	if _, err := os.Stat(filepath.Join(platform.Sites, name, "index.html")); !os.IsNotExist(err) {
		t.Fatal("artifact was not deleted")
	}
}

func TestPublishChoosesProjectOrPath(t *testing.T) {
	platform := sharePlatform(t)
	directory := t.TempDir()
	project := filepath.Join(directory, "dashboard")
	run(t, directory, "init", project)
	createBuild(t, project)

	// A folder with hex.json is published as its project, by path or from
	// inside it; project names and access come from hex.json.
	if output := run(t, directory, "publish", "dashboard"); !strings.Contains(output, "http://dashboard.localhost:8080/") {
		t.Fatalf("project not published by path: %s", output)
	}
	if readShared(t, platform, "dashboard", "index.html") != "test website" {
		t.Fatal("project content missing")
	}
	for _, args := range [][]string{
		{"publish", "dashboard", "--name", "Other"},
		{"publish", "dashboard", "--with", "group:sales"},
		{"publish", "dashboard", "--update", "abcdefghij"},
	} {
		app, _ := testApp(t, directory)
		if err := app.Execute(context.Background(), args, "test"); err == nil || !strings.Contains(err.Error(), "hex.json") {
			t.Fatalf("%v: expected a hex.json error, got %v", args, err)
		}
	}

	// Without hex.json and without a path there is nothing safe to publish.
	app, _ := testApp(t, t.TempDir())
	if err := app.Execute(context.Background(), []string{"publish"}, "test"); err == nil || !strings.Contains(err.Error(), "hex init") {
		t.Fatalf("publishing a bare folder by default: %v", err)
	}
}

func TestPublishRefusesPrivateContent(t *testing.T) {
	sharePlatform(t)
	directory := t.TempDir()
	refuse := func(args ...string) {
		t.Helper()
		app, _ := testApp(t, directory)
		err := app.Execute(context.Background(), args, "test")
		if err == nil || !strings.Contains(err.Error(), "refusing") {
			t.Fatalf("%v was not refused: %v", args, err)
		}
	}

	writePublishFixture(t, directory, map[string]string{
		"web/index.html":                "site",
		"web/node_modules/lib/index.js": "dependency",
		"repo/index.html":               "site",
		"repo/.git/config":              "repository",
		"env/index.html":                "site",
		"env/config/.env.production":    "SECRET=1",
		"deploy.pem":                    "-----BEGIN PRIVATE KEY-----",
	})
	refuse("publish", "web")
	refuse("publish", "repo")
	refuse("publish", "env")
	refuse("publish", "deploy.pem")

	// Projects leave out dot-files and node_modules, but refuse secret-looking
	// files in their build output.
	project := filepath.Join(directory, "app")
	run(t, directory, "init", project)
	createBuild(t, project)
	writePublishFixture(t, project, map[string]string{"dist/server.key": "key", ".env": "SECRET=1"})
	app, _ := testApp(t, project)
	if err := app.Execute(context.Background(), []string{"publish"}, "test"); err == nil || !strings.Contains(err.Error(), "server.key") {
		t.Fatalf("a key in the build output was published: %v", err)
	}
	if err := os.Remove(filepath.Join(project, "dist", "server.key")); err != nil {
		t.Fatal(err)
	}
	run(t, project, "publish")
}

func TestLargePublicationsNeedConfirmation(t *testing.T) {
	sharePlatform(t)
	directory := t.TempDir()
	files := map[string]string{"index.html": "site"}
	for i := range 120 {
		files[fmt.Sprintf("assets/file-%03d.txt", i)] = "x"
	}
	writePublishFixture(t, filepath.Join(directory, "many"), files)

	app, output := testApp(t, directory)
	err := app.Execute(context.Background(), []string{"publish", "many"}, "test")
	if err == nil || !strings.Contains(err.Error(), "--yes") || !strings.Contains(output.String(), "121 files") {
		t.Fatalf("a large publication went ahead without confirmation: %v\n%s", err, output)
	}

	app, _ = testApp(t, directory)
	app.Interactive = true
	app.input.Reset(strings.NewReader("n\n"))
	if err := app.Execute(context.Background(), []string{"publish", "many"}, "test"); err == nil || !strings.Contains(err.Error(), "cancelled") {
		t.Fatalf("answering no did not cancel: %v", err)
	}

	shared(t, directory, "many", "--yes")
}

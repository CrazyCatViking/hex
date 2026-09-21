package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
)

func testApp(t *testing.T, directory string) (*App, *bytes.Buffer) {
	t.Helper()
	output := new(bytes.Buffer)
	app, err := New(strings.NewReader(""), output, output)
	if err != nil {
		t.Fatal(err)
	}
	app.Dir = directory
	return app, output
}

func run(t *testing.T, directory string, args ...string) string {
	t.Helper()
	app, output := testApp(t, directory)
	if err := app.Execute(context.Background(), args, "test"); err != nil {
		t.Fatalf("%v: %v\n%s", args, err, output)
	}
	return output.String()
}

func localConnection(server, root string) Connection {
	return Connection{
		Version: 1, Name: "Company Hex", Server: server, SiteBaseURL: "http://localhost:8080",
		Publishing:   &Publishing{Provider: "filesystem", Root: root},
		Capabilities: &Capabilities{Version: 1, Sites: true, Files: true, MaxUploadBytes: 4096},
	}
}

func TestSetupProfilesAndOfflinePublishing(t *testing.T) {
	directory := t.TempDir()
	t.Setenv("HEX_CONFIG_DIR", filepath.Join(directory, "profiles"))
	t.Setenv("HEX_TOKEN", "must-not-send-during-setup")
	var requests atomic.Int32
	var blocked atomic.Bool
	connection := localConnection("", filepath.Join(directory, "published"))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.URL.Path != connectionPath {
			t.Errorf("unexpected request %s", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "" {
			t.Error("setup sent a token")
		}
		if blocked.Load() {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(connection); err != nil {
			t.Error(err)
		}
	}))
	defer server.Close()
	connection.Server = server.URL

	output := run(t, directory, "setup", server.URL, "--name", "company", "--json")
	if !strings.Contains(output, `"status": "ready"`) {
		t.Fatal(output)
	}
	project := filepath.Join(directory, "demo")
	run(t, directory, "init", project)
	data, err := os.ReadFile(filepath.Join(project, "hex.json"))
	if err != nil {
		t.Fatal(err)
	}
	var config Project
	if err := json.Unmarshal(data, &config); err != nil {
		t.Fatal(err)
	}
	if config.Platform != "" || config.Directory != "" || config.Server != "" || config.Name != "demo" {
		t.Fatalf("unexpected project: %+v", config)
	}
	blocked.Store(true)
	createBuild(t, project)
	if output := run(t, project, "publish"); strings.TrimSpace(output) != "http://demo.localhost:8080/" {
		t.Fatal(output)
	}
	if output := run(t, project, "capabilities"); !strings.Contains(output, `"files": true`) {
		t.Fatal(output)
	}
	run(t, project, "delete", "--yes")
	if requests.Load() != 1 {
		t.Fatal("publishing contacted the API")
	}
}

func TestProtectedSetupAndDroppedFile(t *testing.T) {
	directory := t.TempDir()
	t.Setenv("HEX_CONFIG_DIR", filepath.Join(directory, "profiles"))
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.Header().Set("Location", "/login")
		w.WriteHeader(http.StatusFound)
	}))
	defer server.Close()
	app, output := testApp(t, directory)
	err := app.Execute(context.Background(), []string{"setup", server.URL, "--json"}, "test")
	var exit *ExitError
	if !errors.As(err, &exit) || exit.Code != 2 {
		t.Fatalf("expected action-required exit: %v", err)
	}
	if !strings.Contains(output.String(), `"status": "download_required"`) || requests.Load() != 1 {
		t.Fatal(output.String())
	}

	file := filepath.Join(directory, "connection with spaces.json")
	if err := writeJSONFile(file, localConnection(server.URL, filepath.Join(directory, "sites"))); err != nil {
		t.Fatal(err)
	}
	app, output = testApp(t, directory)
	app.Interactive = true
	app.input.Reset(strings.NewReader("'" + file + "'\n"))
	var opened string
	app.OpenBrowser = func(url string) error { opened = url; return nil }
	if err := app.Execute(context.Background(), []string{"setup", server.URL, "--name", "company"}, "test"); err != nil {
		t.Fatal(err)
	}
	if opened != server.URL+connectionPath {
		t.Fatal(opened)
	}
	connection, _, err := loadProfile("company", false)
	if err != nil || connection.Name != "Company Hex" {
		t.Fatalf("%+v %v", connection, err)
	}
}

func TestConnectionValidation(t *testing.T) {
	connection := localConnection("http://localhost:8080", t.TempDir())
	connection.Resource = "api://00000000-0000-0000-0000-000000000001"
	data, err := json.Marshal(connection)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := parseConnection(data, "http://127.0.0.1:8080")
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Resource != connection.Resource {
		t.Fatalf("resource not preserved: %+v", parsed)
	}
	connection.Resource = "api://bad resource"
	data, err = json.Marshal(connection)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := parseConnection(data, ""); err == nil {
		t.Fatal("invalid API resource accepted")
	}
	connection.Resource = ""
	if _, err := parseConnection(data, "https://other.example"); err == nil {
		t.Fatal("wrong-platform file accepted")
	}
	var object map[string]any
	if err := json.Unmarshal(data, &object); err != nil {
		t.Fatal(err)
	}
	object["clientSecret"] = "never-store-this"
	data, err = json.Marshal(object)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := parseConnection(data, ""); err == nil || strings.Contains(err.Error(), "never-store-this") {
		t.Fatal("credential document accepted or leaked")
	}
	connection.Server = "https://company.example"
	data, err = json.Marshal(connection)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := parseConnection(data, ""); err == nil {
		t.Fatal("remote filesystem configuration accepted")
	}
	if _, err := parseConnection(bytes.Repeat([]byte(" "), maxConfigBytes+1), ""); err == nil {
		t.Fatal("oversized configuration accepted")
	}
}

func TestSetupDoesNotOverwriteAnInvalidProfileStore(t *testing.T) {
	directory := t.TempDir()
	t.Setenv("HEX_CONFIG_DIR", directory)
	path := filepath.Join(directory, "profiles.json")
	if err := os.WriteFile(path, []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := saveProfile(localConnection("http://localhost:8080", t.TempDir()), "company"); err == nil {
		t.Fatal("invalid profile store was accepted")
	}
	content, err := os.ReadFile(path)
	if err != nil || string(content) != "{}" {
		t.Fatalf("profile store was changed: %s %v", content, err)
	}
}

func TestDroppedPathsAndSiteURLs(t *testing.T) {
	cases := map[string]string{
		"'/tmp/a b.json'":                   "/tmp/a b.json",
		`"/tmp/a b.json"`:                   "/tmp/a b.json",
		`/tmp/a\ b.json `:                   "/tmp/a b.json",
		`'/tmp/user'\''s file.json'`:        "/tmp/user's file.json",
		`& 'C:\Users\Alex\hex config.json'`: `C:\Users\Alex\hex config.json`,
		`$(touch /tmp/never-execute).json`:  `$(touch /tmp/never-execute).json`,
	}
	for input, expected := range cases {
		actual, err := droppedFilePath(input)
		if err != nil || actual != expected {
			t.Errorf("%q: %q %v", input, actual, err)
		}
	}
	if _, err := droppedFilePath(""); err == nil {
		t.Fatal("empty path accepted")
	}
	actual, err := siteURL("http://localhost:8080", "demo")
	if err != nil || actual != "http://demo.localhost:8080/" {
		t.Fatalf("%s %v", actual, err)
	}
	for _, name := range []string{"../outside", "upperCase", "under_score", "-first", "last-", strings.Repeat("a", 64)} {
		if _, err := siteURL("https://hex.example.com", name); err == nil {
			t.Errorf("accepted %q", name)
		}
	}
}

func TestPublishingMirrorsOnlyOneSite(t *testing.T) {
	directory := t.TempDir()
	t.Setenv("HEX_CONFIG_DIR", filepath.Join(directory, "profiles"))
	project := filepath.Join(directory, "project")
	destination := filepath.Join(directory, "sites")
	run(t, directory, "init", project, "--name", "demo", "--publish-root", destination)
	createBuild(t, project)
	write := func(path, value string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(value), 0644); err != nil {
			t.Fatal(err)
		}
	}
	source := filepath.Join(project, "dist")
	write(filepath.Join(source, ".env"), "secret")
	write(filepath.Join(source, "node_modules", "package.js"), "dependency")
	write(filepath.Join(source, "asset"), "file")
	write(filepath.Join(destination, "other", "index.html"), "other site")
	run(t, project, "publish")
	if _, err := os.Stat(filepath.Join(destination, "demo", ".env")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("hidden file published")
	}
	if err := os.Remove(filepath.Join(source, "asset")); err != nil {
		t.Fatal(err)
	}
	write(filepath.Join(source, "asset", "nested.js"), "nested")
	run(t, project, "publish")
	if err := os.RemoveAll(filepath.Join(source, "asset")); err != nil {
		t.Fatal(err)
	}
	write(filepath.Join(source, "asset"), "file again")
	run(t, project, "publish")
	if content, err := os.ReadFile(filepath.Join(destination, "demo", "asset")); err != nil || string(content) != "file again" {
		t.Fatalf("%s %v", content, err)
	}
	run(t, project, "delete", "--yes")
	if content, err := os.ReadFile(filepath.Join(destination, "other", "index.html")); err != nil || string(content) != "other site" {
		t.Fatalf("%s %v", content, err)
	}
}

func TestPublishingRejectsSymlinksAndOverlap(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation needs Windows privileges")
	}
	directory := t.TempDir()
	t.Setenv("HEX_CONFIG_DIR", filepath.Join(directory, "profiles"))
	project := filepath.Join(directory, "project")
	destination := filepath.Join(directory, "sites")
	run(t, directory, "init", project, "--name", "demo", "--publish-root", destination)
	createBuild(t, project)
	source := filepath.Join(project, "dist")
	if err := os.Symlink(filepath.Join(source, "index.html"), filepath.Join(source, "leak")); err != nil {
		t.Fatal(err)
	}
	app, _ := testApp(t, project)
	if err := app.Execute(context.Background(), []string{"publish"}, "test"); err == nil {
		t.Fatal("source symlink accepted")
	}
	if err := os.Remove(filepath.Join(source, "leak")); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(destination, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(source, filepath.Join(destination, "demo")); err != nil {
		t.Fatal(err)
	}
	if err := app.Execute(context.Background(), []string{"publish"}, "test"); err == nil {
		t.Fatal("destination symlink accepted")
	}
	config := Project{Name: "demo", Server: "http://localhost:8080", Directory: "dist", Publishing: &Publishing{Provider: "filesystem", Root: source}}
	if err := writeJSONFile(filepath.Join(project, "hex.json"), config); err != nil {
		t.Fatal(err)
	}
	if err := app.Execute(context.Background(), []string{"publish"}, "test"); err == nil || !strings.Contains(err.Error(), "overlap") {
		t.Fatal(err)
	}
}

func TestDevSettingsAndPorts(t *testing.T) {
	directory := t.TempDir()
	config := devSettings{Package: "./cmd/platform", Services: []string{"postgres", "azurite"}, EnvFile: ".env.local", Port: 8088, APIPort: 8081, PostgresPort: 54320, BlobPort: 10000}
	if err := writeJSONFile(filepath.Join(directory, "hex.dev.json"), config); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, ".env.local"), []byte("CUSTOM_VALUE=\"hello world\"\nLITERAL=$NO_EXPANSION\n"), 0600); err != nil {
		t.Fatal(err)
	}
	settings, err := readDevSettings(directory, "", devSettings{Services: []string{"none"}, Port: 8090}, func(name string) bool { return name == "services" || name == "port" }, false)
	if err != nil {
		t.Fatal(err)
	}
	if settings.Package != "./cmd/platform" || settings.Port != 8090 || len(settings.Services) != 0 || settings.Environment["CUSTOM_VALUE"] != "hello world" || settings.Environment["LITERAL"] != "$NO_EXPANSION" {
		t.Fatalf("%+v", settings)
	}
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer closeLogged(io.Discard, listener)
	if err := checkPortsAvailable(listener.Addr().(*net.TCPAddr).Port); err == nil {
		t.Fatal("occupied port accepted")
	}
	settings.Services = []string{"postgres"}
	args := composeArguments(settings, "compose.yaml", "up")
	if strings.Join(args[len(args)-6:], " ") != "up --detach --wait --wait-timeout 120 postgres" {
		t.Fatal(args)
	}
	if strings.Contains(strings.Join(composeArguments(settings, "compose.yaml", "down"), " "), "--volumes") {
		t.Fatal("shutdown deletes volumes")
	}
}

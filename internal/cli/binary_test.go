package cli

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestStandaloneBinaryNeedsNeitherNodeNorGoAtRuntime(t *testing.T) {
	if testing.Short() {
		t.Skip("standalone binary build")
	}
	directory := t.TempDir()
	binary := filepath.Join(directory, "hex")
	if runtime.GOOS == "windows" {
		binary += ".exe"
	}
	build := exec.Command("go", "build", "-o", binary, "./cmd/hex")
	build.Dir = filepath.Join("..", "..")
	build.Env = append(os.Environ(), "CGO_ENABLED=0")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build CLI: %v\n%s", err, output)
	}
	emptyPath := filepath.Join(directory, "empty-path")
	if err := os.Mkdir(emptyPath, 0755); err != nil {
		t.Fatal(err)
	}
	call := func(args ...string) string {
		t.Helper()
		command := exec.Command(binary, args...)
		command.Dir = directory
		command.Env = []string{"PATH=" + emptyPath, "HOME=" + directory, "USERPROFILE=" + directory, "HEX_CONFIG_DIR=" + filepath.Join(directory, "profiles")}
		output, err := command.CombinedOutput()
		if err != nil {
			t.Fatalf("%v: %v\n%s", args, err, output)
		}
		return string(output)
	}
	call("init", "demo", "--publish-root", filepath.Join(directory, "sites"))
	entries, err := os.ReadDir(filepath.Join(directory, "demo"))
	if err != nil || len(entries) != 2 || entries[0].Name() != ".agents" || entries[1].Name() != "hex.json" {
		t.Fatalf("init must create only configuration and skills: %v %v", entries, err)
	}
	if data, err := os.ReadFile(filepath.Join(directory, "demo", ".agents", "skills", "hex", "SKILL.md")); err != nil || !strings.Contains(string(data), "hex publish") {
		t.Fatalf("missing embedded skill: %v", err)
	}
	data, err := json.Marshal(localConnection("http://localhost:8080", filepath.Join(directory, "sites")))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "connection.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	if result := call("setup", "--file", "connection.json", "--json"); !strings.Contains(result, `"status": "ready"`) {
		t.Fatal(result)
	}
	call("--version")
}

func TestAzureToolsAreDelegatedWithoutHexRequests(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test helper uses a POSIX shell")
	}
	directory := t.TempDir()
	t.Setenv("HEX_CONFIG_DIR", filepath.Join(directory, "profiles"))
	tools := filepath.Join(directory, "tools")
	t.Setenv("HEX_AZCOPY_PATH", filepath.Join(tools, "azcopy"))
	if err := os.Mkdir(tools, 0755); err != nil {
		t.Fatal(err)
	}
	logFile := filepath.Join(directory, "tool-args")
	metadataLog := filepath.Join(directory, "metadata.json")
	t.Setenv("HEX_METADATA_LOG", metadataLog)
	t.Setenv("HEX_TOOL_LOG", logFile)
	t.Setenv("PATH", tools+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("DISPLAY", ":fixture")
	t.Setenv("CODESPACES", "")
	t.Setenv("AZCOPY_TENANT_ID", "")
	if err := os.WriteFile(filepath.Join(tools, "az"), []byte("#!/bin/sh\nprintf '%s\\n' \"$@\" > \"$HEX_TOOL_LOG\"\n"), 0755); err != nil {
		t.Fatal(err)
	}
	script := `#!/bin/sh
set -eu
printf '%s\n' "$@" > "$HEX_TOOL_LOG"
if [ "$1" = sync ]; then
    test -f "$2/index.html"
    test -f "$2/styles/main.css"
    test ! -e "$2/hex.json"
    test ! -e "$2/.agents"
    cp "$2/.hex-site.json" "$HEX_METADATA_LOG"
fi
`
	if err := os.WriteFile(filepath.Join(tools, "azcopy"), []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	project := filepath.Join(directory, "demo")
	run(t, directory, "init", project, "--publish-url", "https://account.file.core.windows.net/sites/public/sites")
	writePublishFixture(t, project, map[string]string{"index.html": "plain site", "styles/main.css": "body {}"})
	run(t, project, "publish")
	args, err := os.ReadFile(logFile)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(args)), "\n")
	if len(lines) != 5 || lines[0] != "sync" || lines[2] != "https://account.file.core.windows.net/sites/public/sites/demo" || lines[4] != "--delete-destination=true" {
		t.Fatal(lines)
	}
	if _, err := os.Stat(lines[1]); !os.IsNotExist(err) {
		t.Fatal("publishing snapshot was not removed")
	}
	metadata, err := os.ReadFile(metadataLog)
	if err != nil || !strings.Contains(string(metadata), `"publishedAt"`) {
		t.Fatalf("Azure snapshot is missing metadata: %s %v", metadata, err)
	}
	run(t, project, "login")
	args, err = os.ReadFile(logFile)
	if err != nil || !strings.HasPrefix(string(args), "login\n--allow-no-subscriptions\n") {
		t.Fatalf("%s %v", args, err)
	}
	run(t, project, "delete", "--yes")
	args, err = os.ReadFile(logFile)
	if err != nil || !strings.HasPrefix(string(args), "remove\n") {
		t.Fatalf("%s %v", args, err)
	}
}

func TestSetupDistinguishesMissingAndProtectedEndpoints(t *testing.T) {
	for _, testCase := range []struct {
		status      int
		contentType string
		body        string
		browser     bool
	}{
		{401, "text/plain", "unauthorized", true},
		{200, "text/html", "sign in", true},
		{404, "text/plain", "not found", false},
		{200, "application/json", "invalid JSON", false},
	} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", testCase.contentType)
			w.WriteHeader(testCase.status)
			if _, err := io.WriteString(w, testCase.body); err != nil {
				t.Error(err)
			}
		}))
		app, _ := testApp(t, t.TempDir())
		_, browser, err := app.downloadConnection(context.Background(), server.URL)
		server.Close()
		if browser != testCase.browser || (err == nil && !browser) {
			t.Fatalf("status %d: browser=%v err=%v", testCase.status, browser, err)
		}
	}
}

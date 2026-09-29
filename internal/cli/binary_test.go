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
	call("init", "demo")
	entries, err := os.ReadDir(filepath.Join(directory, "demo"))
	if err != nil || len(entries) != 2 || entries[0].Name() != ".agents" || entries[1].Name() != "hex.json" {
		t.Fatalf("init must create only configuration and skills: %v %v", entries, err)
	}
	if data, err := os.ReadFile(filepath.Join(directory, "demo", ".agents", "skills", "hex", "SKILL.md")); err != nil || !strings.Contains(string(data), "hex publish") {
		t.Fatalf("missing embedded skill: %v", err)
	}
	data, err := json.Marshal(localConnection("http://localhost:8080"))
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

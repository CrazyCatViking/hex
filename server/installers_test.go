package hex_test

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	hex "github.com/crazycatviking/hex/server"
)

func installerPlatform(releaseURL string) *hex.Server {
	return hex.New(hex.Config{
		SiteBaseURL:   "https://hex.smartdok.dev",
		CLIReleaseURL: releaseURL,
		Connection: &hex.ConnectionConfig{
			Name: "SmartDok Hex", Server: "https://hex.smartdok.dev",
			Publishing: &hex.PublishingConfig{Provider: "azure-files", URL: "https://example.file.core.windows.net/sites/public/sites"},
		},
	})
}

func TestInstallerDownloadsAndAuthenticationNavigation(t *testing.T) {
	server := installerPlatform("")
	for _, osName := range []string{"macos", "linux", "windows"} {
		response := request(t, server, http.MethodGet, "/api/hex/install/"+osName, nil, http.StatusOK)
		if !strings.Contains(response.Header().Get("Content-Disposition"), "install-hex-"+osName) || response.Header().Get("Cache-Control") != "no-store" {
			t.Fatal("installer must be an uncached browser download")
		}
		if !strings.Contains(response.Body.String(), "setup --file") || !strings.Contains(response.Body.String(), "SHA256SUMS") || strings.Contains(response.Body.String(), "{{.") {
			t.Fatal("installer is missing configuration import or release verification")
		}
		if osName != "windows" && runtime.GOOS != "windows" {
			command := exec.Command("bash", "-n")
			command.Stdin = strings.NewReader(response.Body.String())
			if output, err := command.CombinedOutput(); err != nil {
				t.Fatalf("invalid Bash installer: %v\n%s", err, output)
			}
		}
	}
	request(t, server, http.MethodGet, "/api/hex/install/other", nil, http.StatusNotFound)
	request(t, installerPlatform("http://remote.example/releases"), http.MethodGet, "/api/hex/install/linux", nil, http.StatusInternalServerError)
	r := httptest.NewRequest(http.MethodGet, "/api/hex/install/linux", nil)
	r.Header.Set("Sec-Fetch-Site", "cross-site")
	w := httptest.NewRecorder()
	server.ServeHTTP(w, r)
	if w.Code != http.StatusForbidden {
		t.Fatal("cross-site fetch was accepted")
	}
	r.Header.Set("Sec-Fetch-Mode", "navigate")
	r.Header.Set("Sec-Fetch-Dest", "document")
	w = httptest.NewRecorder()
	server.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatal("navigation after hosting SSO could not download the installer")
	}
}

func TestLinuxInstallerBootstrapsWithoutPlatformRequests(t *testing.T) {
	if testing.Short() || runtime.GOOS != "linux" || runtime.GOARCH != "amd64" {
		t.Skip("Linux x86-64 installer integration")
	}
	if _, err := exec.LookPath("curl"); err != nil {
		t.Skip("curl is required")
	}
	directory := t.TempDir()
	binary := filepath.Join(directory, "release-hex")
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	build := exec.CommandContext(ctx, "go", "build", "-o", binary, "./cmd/hex")
	build.Dir = ".."
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build installer fixture: %v\n%s", err, output)
	}
	data, err := os.ReadFile(binary)
	if err != nil {
		t.Fatal(err)
	}
	checksum := fmt.Sprintf("%x", sha256.Sum256(data))
	var corrupted atomic.Bool
	releases := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/SHA256SUMS":
			if _, err := fmt.Fprintf(w, "%s  hex-linux-amd64\n", checksum); err != nil {
				t.Error(err)
			}
		case "/hex-linux-amd64":
			content := data
			if corrupted.Load() {
				content = []byte("corrupted download")
			}
			if _, err := w.Write(content); err != nil {
				t.Error(err)
			}
		default:
			t.Errorf("unexpected installer request: %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer releases.Close()
	script := request(t, installerPlatform(releases.URL), http.MethodGet, "/api/hex/install/linux", nil, http.StatusOK).Body.String()
	home := filepath.Join(directory, "employee home")
	if err := os.MkdirAll(home, 0700); err != nil {
		t.Fatal(err)
	}
	profiles := filepath.Join(home, "profiles")
	runInstaller := func() ([]byte, error) {
		command := exec.CommandContext(ctx, "bash")
		command.Stdin = strings.NewReader(script)
		command.Env = append(os.Environ(), "HOME="+home, "HEX_CONFIG_DIR="+profiles, "ZDOTDIR="+home, "SHELL=/bin/bash", "TMPDIR="+directory)
		return command.CombinedOutput()
	}
	for range 2 {
		if output, err := runInstaller(); err != nil {
			t.Fatalf("installer failed: %v\n%s", err, output)
		}
	}
	configuration, err := os.ReadFile(filepath.Join(profiles, "profiles.json"))
	if err != nil {
		t.Fatal(err)
	}
	var store struct {
		DefaultProfile string `json:"defaultProfile"`
		Profiles       map[string]struct {
			Server string `json:"server"`
		} `json:"profiles"`
	}
	if err := json.Unmarshal(configuration, &store); err != nil {
		t.Fatal(err)
	}
	if store.DefaultProfile != "hex.smartdok.dev" || store.Profiles[store.DefaultProfile].Server != "https://hex.smartdok.dev" {
		t.Fatalf("installer did not configure the company profile: %s", configuration)
	}
	for _, name := range []string{".profile", ".bashrc", ".zshrc"} {
		content, err := os.ReadFile(filepath.Join(home, name))
		if err != nil || strings.Count(string(content), "# Hex CLI") != 1 {
			t.Fatalf("PATH setup is not idempotent: %s %v", content, err)
		}
	}
	corrupted.Store(true)
	if output, err := runInstaller(); err == nil || !strings.Contains(string(output), "Checksum mismatch") {
		t.Fatalf("corrupted binary accepted: %v\n%s", err, output)
	}
	installed, err := os.ReadFile(filepath.Join(home, ".local", "bin", "hex"))
	if err != nil || fmt.Sprintf("%x", sha256.Sum256(installed)) != checksum {
		t.Fatalf("failed update damaged installed CLI: %v", err)
	}
}

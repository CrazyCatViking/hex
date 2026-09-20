package cli

import (
	"bytes"
	"context"
	"crypto/sha256"
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
)

func TestReleaseChecksumsAndTargets(t *testing.T) {
	hash := strings.Repeat("a", 64)
	for _, platform := range [][2]string{{"linux", "amd64"}, {"darwin", "amd64"}, {"darwin", "arm64"}, {"windows", "amd64"}} {
		artifact, err := cliArtifact(platform[0], platform[1])
		if err != nil {
			t.Fatal(err)
		}
		checksum, err := releaseChecksum([]byte(hash+"  "+artifact+"\n"), artifact)
		if err != nil || checksum != hash {
			t.Fatalf("invalid target %s: %s %v", artifact, checksum, err)
		}
		for _, invalid := range []string{hash + "  other-binary\n", "bad  " + artifact + "\n", strings.Repeat(hash+"  "+artifact+"\n", 2)} {
			if _, err := releaseChecksum([]byte(invalid), artifact); err == nil {
				t.Fatal("invalid release manifest accepted")
			}
		}
	}
	if _, err := cliArtifact("linux", "386"); err == nil {
		t.Fatal("unsupported platform accepted")
	}
	for _, address := range []string{"http://remote.example/releases", "https://user:password@example.com", "https://example.com/?token=secret"} {
		if err := validateReleaseDirectory(address); err == nil {
			t.Fatalf("invalid release location accepted: %s", address)
		}
	}
}

func TestCLIUpdateReplacesExecutableAndPreservesConfiguration(t *testing.T) {
	if testing.Short() {
		t.Skip("build release fixture")
	}
	directory := t.TempDir()
	artifact, err := cliArtifact(runtime.GOOS, runtime.GOARCH)
	if err != nil {
		t.Skip(err)
	}
	release := filepath.Join(directory, artifact)
	build := exec.Command("go", "build", "-ldflags", "-X main.version=9.9.9", "-o", release, "./cmd/hex")
	build.Dir = filepath.Join("..", "..")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build update fixture: %v\n%s", err, output)
	}
	binary, err := os.ReadFile(release)
	if err != nil {
		t.Fatal(err)
	}
	checksum := fmt.Sprintf("%x", sha256.Sum256(binary))
	var binaryRequests atomic.Int32
	var corrupt atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/SHA256SUMS" {
			if _, err := fmt.Fprintf(w, "%s  %s\n", checksum, artifact); err != nil {
				t.Error(err)
			}
			return
		}
		if r.URL.Path != "/"+artifact {
			http.NotFound(w, r)
			return
		}
		binaryRequests.Add(1)
		data := binary
		if corrupt.Load() {
			data = []byte("corrupt")
		}
		if _, err := w.Write(data); err != nil {
			t.Error(err)
		}
	}))
	defer server.Close()
	installed := filepath.Join(directory, "installed-hex")
	if runtime.GOOS == "windows" {
		installed += ".exe"
	}
	if err := os.WriteFile(installed, []byte("old executable"), 0755); err != nil {
		t.Fatal(err)
	}
	configuration := filepath.Join(directory, "profiles.json")
	if err := os.WriteFile(configuration, []byte("existing configuration"), 0600); err != nil {
		t.Fatal(err)
	}
	app, output := testApp(t, directory)
	if err := app.updateExecutable(context.Background(), installed, server.URL); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "Updated Hex to 9.9.9") {
		t.Fatal(output.String())
	}
	if err := app.updateExecutable(context.Background(), installed, server.URL); err != nil {
		t.Fatal(err)
	}
	if binaryRequests.Load() != 1 || !strings.Contains(output.String(), "already up to date") {
		t.Fatal("unchanged CLI was downloaded again")
	}
	data, err := os.ReadFile(configuration)
	if err != nil || string(data) != "existing configuration" {
		t.Fatal("configuration was modified")
	}
	corrupt.Store(true)
	failedUpdate := filepath.Join(directory, "failed-update-target")
	if err := os.WriteFile(failedUpdate, []byte("old executable"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := app.updateExecutable(context.Background(), failedUpdate, server.URL); err == nil || !strings.Contains(err.Error(), "SHA-256 mismatch") {
		t.Fatalf("corrupt update accepted: %v", err)
	}
	data, err = os.ReadFile(failedUpdate)
	if err != nil || string(data) != "old executable" {
		t.Fatal("failed update changed installed executable")
	}
	corrupt.Store(false)
	oldBuild := exec.Command("go", "build", "-ldflags", "-X main.version=9.9.8", "-o", installed, "./cmd/hex")
	oldBuild.Dir = filepath.Join("..", "..")
	if output, err := oldBuild.CombinedOutput(); err != nil {
		t.Fatalf("build running update fixture: %v\n%s", err, output)
	}
	t.Setenv("HEX_CONFIG_DIR", filepath.Join(directory, "profiles"))
	t.Setenv("HEX_CLI_RELEASE_URL", "")
	connection := localConnection("http://localhost:8080", filepath.Join(directory, "sites"))
	connection.CLIReleaseURL = server.URL
	if _, err := saveProfile(connection, "company"); err != nil {
		t.Fatal(err)
	}
	profilePath := filepath.Join(directory, "profiles", "profiles.json")
	profileBefore, err := os.ReadFile(profilePath)
	if err != nil {
		t.Fatal(err)
	}
	command := exec.Command(installed, "update")
	result, err := command.CombinedOutput()
	if err != nil || !bytes.Contains(result, []byte("Updated Hex to 9.9.9")) {
		t.Fatalf("update command failed: %v\n%s", err, result)
	}
	result, err = exec.Command(installed, "--version").CombinedOutput()
	if err != nil || strings.TrimSpace(string(result)) != "hex version 9.9.9" {
		t.Fatalf("replacement is not executable: %v\n%s", err, result)
	}
	profileAfter, err := os.ReadFile(profilePath)
	if err != nil || !bytes.Equal(profileBefore, profileAfter) {
		t.Fatal("self-update changed saved platform profiles")
	}
}

func TestWindowsUpdateRollback(t *testing.T) {
	directory := t.TempDir()
	executable := filepath.Join(directory, "hex.exe")
	if err := os.WriteFile(executable, []byte("original"), 0755); err != nil {
		t.Fatal(err)
	}
	app, _ := testApp(t, directory)
	if err := app.replaceWindowsExecutable(filepath.Join(directory, "missing.exe"), executable); err == nil {
		t.Fatal("replacement failure was hidden")
	}
	data, err := os.ReadFile(executable)
	if err != nil || string(data) != "original" {
		t.Fatal("failed Windows update did not restore original executable")
	}
}

func TestConnectionKeepsReleaseMirror(t *testing.T) {
	directory := t.TempDir()
	t.Setenv("HEX_CONFIG_DIR", directory)
	connection := localConnection("http://localhost:8080", filepath.Join(directory, "sites"))
	connection.CLIReleaseURL = "https://releases.example.com/hex/latest"
	if _, err := saveProfile(connection, "company"); err != nil {
		t.Fatal(err)
	}
	loaded, _, err := loadProfile("company", false)
	if err != nil || loaded.CLIReleaseURL != connection.CLIReleaseURL {
		t.Fatalf("release mirror was lost: %+v %v", loaded, err)
	}
}

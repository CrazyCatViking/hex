package cli

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
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

func azCopyArchive(t *testing.T, format, name string) []byte {
	t.Helper()
	var buffer bytes.Buffer
	content := []byte("verified tool bytes")
	if format == "zip" {
		archive := zip.NewWriter(&buffer)
		entry, err := archive.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := entry.Write(content); err != nil {
			t.Fatal(err)
		}
		if err := archive.Close(); err != nil {
			t.Fatal(err)
		}
	} else {
		compressed := gzip.NewWriter(&buffer)
		archive := tar.NewWriter(compressed)
		if err := archive.WriteHeader(&tar.Header{Name: name, Mode: 0755, Size: int64(len(content)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		if _, err := archive.Write(content); err != nil {
			t.Fatal(err)
		}
		if err := archive.Close(); err != nil {
			t.Fatal(err)
		}
		if err := compressed.Close(); err != nil {
			t.Fatal(err)
		}
	}
	return buffer.Bytes()
}

func TestManagedAzCopyDownloadAndCache(t *testing.T) {
	for _, format := range []string{"tar.gz", "zip"} {
		t.Run(format, func(t *testing.T) {
			directory := t.TempDir()
			archive := azCopyArchive(t, format, "azcopy_fixture/azcopy")
			var requests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				if _, err := w.Write(archive); err != nil {
					t.Error(err)
				}
			}))
			defer server.Close()
			release := azCopyRelease{Name: "azcopy_fixture." + format, URL: server.URL, SHA256: fmt.Sprintf("%x", sha256.Sum256(archive))}
			app, _ := testApp(t, directory)
			for range 2 {
				binary, err := app.installAzCopy(context.Background(), filepath.Join(directory, "cache"), "azcopy", release)
				if err != nil {
					t.Fatal(err)
				}
				data, err := os.ReadFile(binary)
				if err != nil || string(data) != "verified tool bytes" {
					t.Fatalf("unexpected cached binary: %s %v", data, err)
				}
			}
			if requests.Load() != 1 {
				t.Fatal("cached tool was downloaded again")
			}
			release.SHA256 = strings.Repeat("0", 64)
			if _, err := app.installAzCopy(context.Background(), filepath.Join(directory, "corrupt"), "azcopy", release); err == nil || !strings.Contains(err.Error(), "SHA-256 mismatch") {
				t.Fatalf("corrupted download accepted: %v", err)
			}
			if _, err := os.Stat(filepath.Join(directory, "corrupt", "azcopy")); !os.IsNotExist(err) {
				t.Fatal("invalid tool was installed")
			}
			unsafe := filepath.Join(directory, "unsafe."+format)
			if err := os.WriteFile(unsafe, azCopyArchive(t, format, "../../azcopy"), 0600); err != nil {
				t.Fatal(err)
			}
			if err := extractAzCopy(unsafe, filepath.Join(directory, "unexpected"), "azcopy"); err == nil {
				t.Fatal("archive path traversal accepted")
			}
		})
	}
}

func TestAzurePublishingManagesLogin(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("provider fixture uses a POSIX shell")
	}
	for _, mode := range []string{"interactive", "cached", "sas", "automation", "azcli", "noninteractive", "login-failure", "publish-failure", "device", "headless"} {
		t.Run(mode, func(t *testing.T) {
			directory := t.TempDir()
			binary := filepath.Join(directory, "azcopy")
			log := filepath.Join(directory, "calls")
			session := filepath.Join(directory, "session")
			t.Setenv("HEX_AZCOPY_PATH", binary)
			t.Setenv("HEX_TEST_CALLS", log)
			t.Setenv("HEX_TEST_SESSION", session)
			t.Setenv("HEX_TEST_MODE", mode)
			t.Setenv("HEX_PUBLISH_SAS", "")
			t.Setenv("AZCOPY_AUTO_LOGIN_TYPE", "")
			t.Setenv("AZCOPY_TENANT_ID", "")
			t.Setenv("DISPLAY", ":fixture")
			t.Setenv("CODESPACES", "")
			t.Setenv("PATH", directory+string(os.PathListSeparator)+os.Getenv("PATH"))
			script := `#!/bin/sh
printf '%s\n' "$*" >> "$HEX_TEST_CALLS"
if [ "$1" = account ]; then
    test -f "$HEX_TEST_SESSION"
elif [ "$1" = login ]; then
    test "$AZURE_CORE_ENABLE_BROKER_ON_WINDOWS" = false || exit 9
    if [ "$HEX_TEST_MODE" = login-failure ]; then exit 1; fi
    : > "$HEX_TEST_SESSION"
elif [ "$1" = sync ]; then
    if [ "$HEX_TEST_MODE" != sas ] && [ "$HEX_TEST_MODE" != automation ]; then
        test "$AZCOPY_AUTO_LOGIN_TYPE" = AZCLI || exit 9
    fi
    printf '%s\n' 'provider progress'
    if [ "$HEX_TEST_MODE" = publish-failure ]; then exit 1; fi
fi
`
			if err := os.WriteFile(binary, []byte(script), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(directory, "az"), []byte(script), 0700); err != nil {
				t.Fatal(err)
			}
			if mode == "cached" || mode == "azcli" {
				if err := os.WriteFile(session, nil, 0600); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "sas" {
				t.Setenv("HEX_PUBLISH_SAS", "fixture")
			}
			if mode == "automation" {
				t.Setenv("AZCOPY_AUTO_LOGIN_TYPE", "SPN")
			}
			if mode == "azcli" {
				t.Setenv("AZCOPY_AUTO_LOGIN_TYPE", "AZCLI")
			}
			if mode == "device" {
				t.Setenv("AZCOPY_AUTO_LOGIN_TYPE", "DEVICE")
			}
			if mode == "headless" {
				t.Setenv("CODESPACES", "true")
			}
			app, output := testApp(t, directory)
			app.Interactive = mode != "noninteractive"
			app.OpenBrowser = func(location string) error {
				t.Fatal("Hex must not open the device-code page")
				return nil
			}
			err := app.storageCommand(context.Background(), "sync", "source", "destination")
			calls, readError := os.ReadFile(log)
			if readError != nil && !(mode == "device" && os.IsNotExist(readError)) {
				t.Fatal(readError)
			}
			failure := mode == "noninteractive" || mode == "login-failure" || mode == "publish-failure" || mode == "device" || mode == "headless"
			if (err != nil) != failure {
				t.Fatalf("mode %s: %v", mode, err)
			}
			if failure && mode != "publish-failure" {
				if strings.Contains(string(calls), "sync") {
					t.Fatal("transfer started before successful authentication")
				}
			} else if !strings.Contains(output.String(), "provider progress") {
				t.Fatal("provider progress is hidden")
			}
			if mode == "interactive" && string(calls) != "account get-access-token --resource https://storage.azure.com/ --output none\nlogin --allow-no-subscriptions --scope https://storage.azure.com/.default --output none\nsync source destination\n" {
				t.Fatalf("unexpected login flow: %s", calls)
			}
			if (mode == "sas" || mode == "automation") && strings.Contains(string(calls), "login") {
				t.Fatal("explicit automation credentials triggered login")
			}
			if mode == "cached" && strings.Contains(string(calls), "login") {
				t.Fatal("cached session triggered browser login")
			}
		})
	}
}

func TestOfficialManagedAzCopy(t *testing.T) {
	if os.Getenv("HEX_TEST_DOWNLOAD_AZCOPY") != "1" {
		t.Skip("opt-in official tool download")
	}
	release, err := azCopyArtifact(runtime.GOOS, runtime.GOARCH)
	if err != nil {
		t.Fatal(err)
	}
	directory := t.TempDir()
	app, _ := testApp(t, directory)
	name := "azcopy"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	binary, err := app.installAzCopy(context.Background(), directory, name, release)
	if err != nil {
		t.Fatal(err)
	}
	output, err := exec.Command(binary, "--version").CombinedOutput()
	if err != nil || !strings.Contains(string(output), azCopyVersion) {
		t.Fatalf("official tool: %s %v", output, err)
	}
}

package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"

	hex "github.com/crazycatviking/hex/server"
)

// fakeFileShare records Azure Files Create File and Put Range requests.
type fakeFileShare struct {
	mu      sync.Mutex
	files   map[string][]byte
	secrets []string
}

func (s *fakeFileShare) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if r.Header.Get("Authorization") != "" {
		s.secrets = append(s.secrets, r.Header.Get("Authorization"))
	}
	if r.URL.Query().Get("sig") != "fixture-signature" {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	// Azure Files rejects user delegation SAS requests without it.
	if r.Header.Get("x-ms-file-request-intent") != "backup" {
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	name := strings.TrimPrefix(r.URL.Path, "/sites/public/sites/demo/")
	switch {
	case r.Method == http.MethodPut && r.URL.Query().Get("comp") == "range":
		data, err := io.ReadAll(r.Body)
		var start, end int
		_, scanError := fmt.Sscanf(r.Header.Get("x-ms-range"), "bytes=%d-%d", &start, &end)
		if err != nil || scanError != nil || end >= len(s.files[name]) || end-start+1 != len(data) {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		copy(s.files[name][start:], data)
	case r.Method == http.MethodPut && r.Header.Get("x-ms-type") == "file":
		size, err := strconv.Atoi(r.Header.Get("x-ms-content-length"))
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		s.files[name] = make([]byte, size)
	default:
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	w.WriteHeader(http.StatusCreated)
}

func TestAzureFilesUploadsUsePresignedURLs(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test helper uses a POSIX shell")
	}
	directory := t.TempDir()
	t.Setenv("HEX_CONFIG_DIR", filepath.Join(directory, "profiles"))
	t.Setenv("HEX_TOKEN", "")
	t.Setenv("HEX_TENANT_ID", "")
	tools := filepath.Join(directory, "tools")
	if err := os.Mkdir(tools, 0755); err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\n[ \"$1 $2 $3 $4\" = \"account get-access-token --resource api://hex\" ] && echo fixture-token\n"
	if err := os.WriteFile(filepath.Join(tools, "az"), []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", tools+string(os.PathListSeparator)+os.Getenv("PATH"))

	share := &fakeFileShare{files: map[string][]byte{}}
	storage := httptest.NewServer(share)
	defer storage.Close()

	var completed []hex.SiteFile
	platform := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer fixture-token" || r.Header.Get("X-Hex-Request") != "1" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		var request publishRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/hex/sites/demo/publish":
			var uploads []hex.UploadTarget
			for _, file := range request.Files {
				uploads = append(uploads, hex.UploadTarget{
					Path:     file.Path,
					Protocol: "azure-files",
					URL:      storage.URL + "/sites/public/sites/demo/" + file.Path + "?sv=2026-06-06&sig=fixture-signature",
				})
			}
			json.NewEncoder(w).Encode(struct {
				Uploads []hex.UploadTarget `json:"uploads"`
			}{Uploads: uploads})
		case "/api/hex/sites/demo/publish/complete":
			completed = request.Files
			json.NewEncoder(w).Encode(publishResult{URL: "https://demo.hex.example.com/"})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer platform.Close()

	project := filepath.Join(directory, "demo")
	run(t, directory, "init", project, "--server", platform.URL, "--resource", "api://hex")
	large := strings.Repeat("x", 5<<20)
	writePublishFixture(t, project, map[string]string{"index.html": "plain site", "styles/main.css": "body {}", "large.bin": large})
	if output := run(t, project, "publish"); !strings.Contains(output, "https://demo.hex.example.com/") {
		t.Fatal(output)
	}

	if string(share.files["index.html"]) != "plain site" || string(share.files["styles/main.css"]) != "body {}" || string(share.files["large.bin"]) != large {
		t.Fatalf("files were not uploaded intact: %d files", len(share.files))
	}
	if len(share.secrets) > 0 {
		t.Fatalf("credentials were sent to storage: %v", share.secrets)
	}
	if len(completed) != 3 {
		t.Fatalf("publication was not completed with the manifest: %+v", completed)
	}
}

func TestPlatformUploadsStayOnThePlatform(t *testing.T) {
	app, _ := testApp(t, t.TempDir())
	project := Project{Server: "http://127.0.0.1:1"}
	for _, target := range []string{"https://attacker.example/api/hex/sites/demo/publish/files/a", "//attacker.example/x", "/api/other"} {
		if err := app.uploadThroughPlatform(t.Context(), project, target, "unused"); err == nil || !strings.Contains(err.Error(), "refusing") {
			t.Fatalf("%s: %v", target, err)
		}
	}
}

func TestAzureCLIRejectsShellCharactersInResources(t *testing.T) {
	app, _ := testApp(t, t.TempDir())
	for _, resource := range []string{"api://hex&calc", "api://hex|x", `api://"hex"`, "api://hex x"} {
		if _, err := app.azureAccessToken(t.Context(), resource); err == nil || !strings.Contains(err.Error(), "invalid API resource") {
			t.Fatalf("%q: %v", resource, err)
		}
		if err := app.azureLogin(t.Context(), resource); err == nil || !strings.Contains(err.Error(), "invalid API resource") {
			t.Fatalf("%q: %v", resource, err)
		}
	}
}

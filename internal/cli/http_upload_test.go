package cli

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	hex "github.com/crazycatviking/hex/server"
)

func TestHTTPUploadsUseSignedURLsWithoutPlatformCredentials(t *testing.T) {
	t.Setenv("HEX_TOKEN", "platform-secret")
	directory := t.TempDir()
	app, _ := testApp(t, directory)
	contents := map[string]string{"index.html": "site", "asset.bin": "\x00\x80\xff", "empty.txt": ""}
	var sources []sourceFile
	for name, body := range contents {
		path := filepath.Join(directory, name)
		if err := os.WriteFile(path, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
		sources = append(sources, sourceFile{Key: name, Path: path})
	}

	var mu sync.Mutex
	var uploaded []string
	storage := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut || r.URL.Query().Get("signature") != "signed" {
			t.Errorf("unexpected signed upload: %s %s", r.Method, r.URL)
		}
		for _, name := range []string{"Authorization", "X-Hex-Request", "Cookie"} {
			if value := r.Header.Get(name); value != "" {
				t.Errorf("forwarded %s to storage: %q", name, value)
			}
		}
		if r.Header.Get("Content-Type") != "application/x-signed" || r.Header.Get("X-Storage-Signed") != "required" {
			t.Errorf("provider-supplied signed headers missing: %v", r.Header)
		}
		name := strings.TrimPrefix(r.URL.Path, "/")
		body, err := io.ReadAll(r.Body)
		if err != nil || string(body) != contents[name] || r.ContentLength != int64(len(contents[name])) {
			t.Errorf("file %s not uploaded intact: body %q, length %d, error %v", name, body, r.ContentLength, err)
		}
		mu.Lock()
		uploaded = append(uploaded, name)
		mu.Unlock()
		w.WriteHeader(http.StatusCreated)
	}))
	defer storage.Close()

	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	storageURL, err := url.Parse(storage.URL)
	if err != nil {
		t.Fatal(err)
	}
	jar.SetCookies(storageURL, []*http.Cookie{{Name: "platform-session", Value: "secret"}})
	app.HTTP.Jar = jar

	var completed atomic.Bool
	platform := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer platform-secret" || r.Header.Get("X-Hex-Request") != "1" {
			t.Error("platform publication request did not retain its credentials")
		}
		var request publishRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if !reflect.DeepEqual(request.SupportedUploadProtocols, []string{"hex", "http", "azure-files"}) {
			t.Errorf("unexpected supported upload protocols: %v", request.SupportedUploadProtocols)
		}
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/complete") {
			completed.Store(true)
			json.NewEncoder(w).Encode(publishResult{URL: "https://site.example/"})
			return
		}
		var uploads []uploadTarget
		for _, file := range request.Files {
			uploads = append(uploads, uploadTarget{
				UploadTarget: hex.UploadTarget{Path: file.Path, Protocol: "http", URL: storage.URL + "/" + file.Path + "?signature=signed"},
				Headers:      map[string]string{"Content-Type": "application/x-signed", "X-Storage-Signed": "required"},
			})
		}
		json.NewEncoder(w).Encode(publishPlan{Uploads: uploads})
	}))
	defer platform.Close()

	result, err := app.publishFiles(t.Context(), Project{Server: platform.URL}, "demo", sources, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if !completed.Load() || result.URL != "https://site.example/" || len(uploaded) != len(contents) || uploaded[len(uploaded)-1] != "index.html" {
		t.Fatalf("publication did not finish with index.html last: result %+v, completed %v, uploads %v", result, completed.Load(), uploaded)
	}
	if app.HTTP.Jar != jar {
		t.Fatal("storage upload changed the platform HTTP client")
	}
}

func TestHTTPUploadRejectsInvalidURLs(t *testing.T) {
	app, _ := testApp(t, t.TempDir())
	for _, target := range []string{
		"/upload", "//storage.example/upload", "http://storage.example/upload", "ftp://storage.example/upload",
		"https:///upload", "https://:443/upload", "https://user:password@storage.example/upload",
		"https://storage.example/upload#fragment", "https:opaque", ":invalid",
	} {
		t.Run(target, func(t *testing.T) {
			err := app.uploadToHTTP(t.Context(), target, nil, "unused")
			if err == nil || !strings.Contains(err.Error(), "invalid storage upload URL") {
				t.Fatalf("URL was not rejected before opening a file: %v", err)
			}
		})
	}
}

func TestHTTPUploadRejectsRedirectsAndStorageFailures(t *testing.T) {
	app, _ := testApp(t, t.TempDir())
	path := filepath.Join(app.Dir, "file")
	if err := os.WriteFile(path, []byte("data"), 0600); err != nil {
		t.Fatal(err)
	}
	var forwarded atomic.Int32
	destination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		forwarded.Add(1)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer destination.Close()
	// Prove upload redirects are refused even if the injected client follows them.
	app.HTTP.CheckRedirect = nil
	for _, status := range []int{http.StatusTemporaryRedirect, http.StatusForbidden, http.StatusInternalServerError} {
		storage := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Location", destination.URL)
			w.WriteHeader(status)
		}))
		err := app.uploadToHTTP(t.Context(), storage.URL, map[string]string{"X-Storage-Signed": "secret"}, path)
		storage.Close()
		if err == nil || !strings.Contains(err.Error(), "storage upload failed (HTTP") {
			t.Fatalf("storage failure %d was accepted: %v", status, err)
		}
	}
	if forwarded.Load() != 0 {
		t.Fatal("a signed upload was forwarded to a redirect destination")
	}
}

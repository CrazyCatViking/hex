package cli

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	hex "github.com/crazycatviking/hex/server"
	"github.com/crazycatviking/hex/server/providers/memory"
)

func TestRawGetStreamingTimeoutAndCancellation(t *testing.T) {
	t.Setenv("HEX_TOKEN", "")
	platform := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		if r.URL.Path == "/cancel" {
			<-r.Context().Done()
			return
		}
		timer := time.NewTimer(10 * time.Millisecond)
		defer timer.Stop()
		select {
		case <-timer.C:
			_, _ = io.WriteString(w, "slow body")
		case <-r.Context().Done():
		}
	}))
	defer platform.Close()
	app, _ := testApp(t, t.TempDir())
	app.HTTP = platform.Client()
	app.HTTP.Timeout = time.Millisecond
	original := app.HTTP
	transport := app.HTTP.Transport
	project := Project{Server: platform.URL}
	response, err := app.rawGet(context.Background(), project, platform.URL+"/slow")
	if err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(response.Body)
	response.Body.Close()
	if err != nil || string(data) != "slow body" {
		t.Fatalf("streaming body retained short API timeout: %q %v", data, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	response, err = app.rawGet(ctx, project, platform.URL+"/cancel")
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	_, err = io.ReadAll(response.Body)
	response.Body.Close()
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("download ignored cancellation: %v", err)
	}
	if app.HTTP != original || app.HTTP.Timeout != time.Millisecond || app.HTTP.Transport != transport || app.HTTP.CheckRedirect != nil {
		t.Fatal("download mutated the shared HTTP client")
	}
}

func saveReadProfile(t *testing.T, directory, server string) {
	t.Helper()
	t.Setenv("HEX_CONFIG_DIR", filepath.Join(directory, "profiles"))
	if _, err := saveProfile(localConnection(server), "company"); err != nil {
		t.Fatal(err)
	}
}

func TestFetchAuthenticationAndRedirectBoundary(t *testing.T) {
	directory := t.TempDir()
	t.Setenv("HEX_TOKEN", "test-token")
	foreignCalls := 0
	foreign := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { foreignCalls++ }))
	defer foreign.Close()
	platform := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-token" {
			t.Error("missing authentication")
		}
		switch r.URL.Path {
		case "/redirect":
			http.Redirect(w, r, "/page", http.StatusFound)
		case "/external":
			http.Redirect(w, r, foreign.URL, http.StatusFound)
		case "/forbidden":
			w.WriteHeader(http.StatusForbidden)
		default:
			w.Header().Set("Content-Type", "text/html")
			_, _ = io.WriteString(w, "<h1>Protected page</h1>")
		}
	}))
	defer platform.Close()
	saveReadProfile(t, directory, platform.URL)
	if got := run(t, directory, "fetch", platform.URL+"/redirect"); got != "<h1>Protected page</h1>" {
		t.Fatal(got)
	}
	for _, address := range []string{foreign.URL, platform.URL + "/external", platform.URL + "/forbidden"} {
		app, _ := testApp(t, directory)
		if err := app.Execute(context.Background(), []string{"fetch", address}, "test"); err == nil {
			t.Fatalf("accepted %s", address)
		}
	}
	if foreignCalls != 0 {
		t.Fatal("sent a request outside the platform")
	}
}

func TestFetchURLValidation(t *testing.T) {
	project := Project{Server: "https://api.example.com", SiteBaseURL: "https://hex.example.com"}
	for _, address := range []string{"https://api.example.com/api/test", "https://hex.example.com/", "https://demo.hex.example.com/path?q=value", "https://demo.hex.example.com:443/path"} {
		if err := validateFetchURL(project, address); err != nil {
			t.Fatalf("%s: %v", address, err)
		}
	}
	for _, address := range []string{"https://hex.example.com.evil.test/", "https://nested.demo.hex.example.com/", "http://demo.hex.example.com/", "https://demo.hex.example.com:8443/", "https://user:secret@hex.example.com/", "file:///etc/passwd", "/relative", "https://hex.example.com/#fragment"} {
		if err := validateFetchURL(project, address); err == nil {
			t.Fatalf("accepted %s", address)
		}
	}
}

func TestFetchSitePathAndBinaryOutput(t *testing.T) {
	directory := t.TempDir()
	content := []byte{0, 1, 255, 10}
	var siteHost string
	platform := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Host != siteHost || r.URL.RequestURI() != "/asset?q=yes" {
			t.Errorf("unexpected request: %s %s", r.Host, r.URL)
		}
		_, _ = w.Write(content)
	}))
	defer platform.Close()
	saveReadProfile(t, directory, platform.URL)
	target, err := url.Parse(platform.URL)
	if err != nil {
		t.Fatal(err)
	}
	siteHost = "demo.localhost:" + target.Port()
	connection := localConnection(platform.URL)
	connection.SiteBaseURL = "http://localhost:" + target.Port()
	if _, err := saveProfile(connection, "company"); err != nil {
		t.Fatal(err)
	}
	app, _ := testApp(t, directory)
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = func(*http.Request) (*url.URL, error) { return nil, errors.New("loopback must bypass the proxy") }
	app.HTTP.Transport = transport
	defer transport.CloseIdleConnections()
	if err := app.Execute(context.Background(), []string{"fetch", "--site", "demo", "--path", "/asset?q=yes", "--output", "asset.bin"}, "test"); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(directory, "asset.bin"))
	if err != nil || !bytes.Equal(got, content) {
		t.Fatalf("%v %v", got, err)
	}
}

type failingDownload struct{}

func (failingDownload) Read([]byte) (int, error) { return 0, errors.New("interrupted") }

func TestDownloadPreservesOutputOnFailure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "existing")
	if err := os.WriteFile(path, []byte("original"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := saveResponse(path, failingDownload{}); err == nil {
		t.Fatal("ignored failed download")
	}
	got, err := os.ReadFile(path)
	if err != nil || string(got) != "original" {
		t.Fatalf("%s %v", got, err)
	}
}

func TestReadCommandsAgainstPlatform(t *testing.T) {
	directory := t.TempDir()
	database := memory.NewDatabase()
	files := memory.NewStore()
	ctx := context.Background()
	for _, id := range []string{"a", "b"} {
		if _, err := database.Put(ctx, "demo", "tasks", id, []byte(`{"done":false}`), hex.WriteOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	for _, key := range []string{"exports/a.csv", "other.txt", "exports/a b?#.csv"} {
		if err := files.Put(ctx, "demo/"+key, strings.NewReader(key)); err != nil {
			t.Fatal(err)
		}
	}
	platform := httptest.NewServer(hex.New(hex.Config{Database: database, Files: files}))
	defer platform.Close()
	saveReadProfile(t, directory, platform.URL)
	page := run(t, directory, "data", "list", "--site", "demo", "--collection", "tasks", "--limit", "1", "--after", "a")
	if !strings.Contains(page, `"id": "b"`) || strings.Contains(page, `"id": "a"`) {
		t.Fatal(page)
	}
	if got := run(t, directory, "data", "get", "--site", "demo", "--collection", "tasks", "--id", "a"); !strings.Contains(got, `"done": false`) {
		t.Fatal(got)
	}
	listing := run(t, directory, "files", "list", "--site", "demo", "--prefix", "exports/")
	if !strings.Contains(listing, "exports/a.csv") || strings.Contains(listing, "other.txt") {
		t.Fatal(listing)
	}
	key := "exports/a b?#.csv"
	if got := run(t, directory, "files", "get", "--site", "demo", "--key", key); got != key {
		t.Fatal(got)
	}
	for _, args := range [][]string{
		{"data", "get", "--site", "demo", "--collection", "../bad", "--id", "a"},
		{"data", "list", "--site", "demo", "--collection", "tasks", "--limit", "101"},
		{"files", "get", "--site", "demo", "--key", "../secret"},
		{"files", "list"},
	} {
		app, _ := testApp(t, directory)
		if err := app.Execute(ctx, args, "test"); err == nil {
			t.Fatalf("accepted %v", args)
		}
	}
}

func TestReadCommandsRespectDataPermissions(t *testing.T) {
	directory := t.TempDir()
	database := memory.NewDatabase()
	files := memory.NewStore()
	access := memory.NewAccessStore()
	ctx := context.Background()
	if err := access.PutSiteAccess(ctx, "demo", hex.SiteAccess{
		Owners: []string{"user:owner"}, Editors: []string{"user:owner"}, Viewers: []string{"user:alice"},
		Collections: map[string]hex.DataRule{"drafts": {Read: hex.Audience{Level: hex.LevelCreator}}},
		Files:       map[string]hex.DataRule{"private/": {Read: hex.Audience{Level: hex.LevelOwners}}},
	}); err != nil {
		t.Fatal(err)
	}
	for _, user := range []string{"alice", "bob"} {
		if _, err := database.Put(ctx, "demo", "drafts", user, []byte(`{"title":"draft"}`), hex.WriteOptions{Creator: user}); err != nil {
			t.Fatal(err)
		}
	}
	for _, key := range []string{"public.txt", "private/secret.txt"} {
		if err := files.Put(ctx, "demo/"+key, strings.NewReader(key)); err != nil {
			t.Fatal(err)
		}
	}
	platform := httptest.NewServer(hex.New(hex.Config{Database: database, Files: files, Access: access, Identity: hex.StaticIdentity{Identity: hex.Identity{ID: "alice"}}}))
	defer platform.Close()
	saveReadProfile(t, directory, platform.URL)
	page := run(t, directory, "data", "list", "--site", "demo", "--collection", "drafts", "--platform", "company")
	if !strings.Contains(page, `"id": "alice"`) || strings.Contains(page, `"id": "bob"`) {
		t.Fatal(page)
	}
	list := run(t, directory, "files", "list", "--site", "demo")
	if !strings.Contains(list, "public.txt") || strings.Contains(list, "secret.txt") {
		t.Fatal(list)
	}
	for _, args := range [][]string{
		{"data", "get", "--site", "demo", "--collection", "drafts", "--id", "bob"},
		{"files", "get", "--site", "demo", "--key", "private/secret.txt"},
	} {
		app, output := testApp(t, directory)
		if err := app.Execute(ctx, args, "test"); err == nil {
			t.Fatalf("read restricted data: %v", args)
		}
		if output.Len() != 0 {
			t.Fatalf("restricted read produced output: %s", output)
		}
	}
}

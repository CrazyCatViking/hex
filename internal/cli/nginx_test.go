package cli

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	hex "github.com/crazycatviking/hex/server"
	"github.com/crazycatviking/hex/server/providers/local"
	"github.com/crazycatviking/hex/server/providers/memory"
)

func TestNginxLocalRuntimePaths(t *testing.T) {
	binary := os.Getenv("NGINX_BIN")
	if binary == "" {
		binary = "nginx"
	}
	if _, err := exec.LookPath(binary); err != nil {
		t.Skipf("NGINX unavailable: %v", err)
	}
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, err := io.Copy(w, r.Body); err != nil {
			t.Errorf("echo request body: %v", err)
		}
	}))
	defer backend.Close()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	directory := filepath.Join(t.TempDir(), "nginx workspace")
	if err := os.Mkdir(directory, 0700); err != nil {
		t.Fatal(err)
	}
	app, err := New(strings.NewReader(""), io.Discard, os.Stderr)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	settings := devSettings{
		Port: port, APIPort: backend.Listener.Addr().(*net.TCPAddr).Port,
		DataDirectory: directory,
	}
	nginx, err := app.startNginx(ctx, settings, directory)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := nginx.stop(); err != nil {
			t.Error(err)
		}
	}()
	baseURL := fmt.Sprintf("http://127.0.0.1:%d", port)
	if err := waitForHTTP(ctx, baseURL+"/healthz", nginx); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"client-body", "proxy", "fastcgi", "scgi", "uwsgi"} {
		info, err := os.Stat(filepath.Join(directory, name))
		if err != nil {
			t.Fatal(err)
		}
		if !info.IsDir() {
			t.Fatalf("%s is not a runtime directory", name)
		}
	}
	body := strings.Repeat("upload data", 100_000)
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+"/api/upload", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	data, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK || string(data) != body {
		t.Fatalf("buffered upload failed: status %d, received %d bytes", response.StatusCode, len(data))
	}
}

func TestNginxCompletedRequestAnalytics(t *testing.T) {
	binary := os.Getenv("NGINX_BIN")
	if binary == "" {
		binary = "nginx"
	}
	if _, err := exec.LookPath(binary); err != nil {
		t.Skip("NGINX unavailable")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	directory := t.TempDir()
	sites, err := local.New(filepath.Join(directory, "sites"))
	if err != nil {
		t.Fatal(err)
	}
	defer sites.Close()
	for path, content := range map[string]string{"index.html": "<title>Analytics</title>", "app.js": "console.log('asset')", "folder/index.html": "<title>Folder</title>"} {
		if err := sites.WriteSiteFile(ctx, "demo", path, int64(len(content)), strings.NewReader(content)); err != nil {
			t.Fatal(err)
		}
	}
	analytics := memory.NewAnalytics()
	collector, err := hex.StartTrafficCollector(ctx, fmt.Sprintf("127.0.0.1:%d", port), analytics)
	if err != nil {
		t.Fatal(err)
	}
	defer collector.Close()
	hexHandler := hex.New(hex.Config{
		Sites: sites, Identity: hex.StaticIdentity{Identity: hex.Identity{ID: "verified-user"}},
		Analytics: analytics, Files: memory.NewStore(),
	})
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "test-api") {
			w.Header().Set("X-Hex-Analytics-User", "dmVyaWZpZWQtdXNlcg")
			w.Header().Set("Content-Type", "text/html")
			io.WriteString(w, "<title>API response</title>")
			return
		}
		hexHandler.ServeHTTP(w, r)
	}))
	defer backend.Close()
	app, err := New(strings.NewReader(""), io.Discard, os.Stderr)
	if err != nil {
		t.Fatal(err)
	}
	nginx, err := app.startNginx(ctx, devSettings{Port: port, APIPort: backend.Listener.Addr().(*net.TCPAddr).Port, DataDirectory: directory}, directory)
	if err != nil {
		t.Fatal(err)
	}
	base := fmt.Sprintf("http://127.0.0.1:%d", port)
	if err := waitForHTTP(ctx, base+"/healthz", nginx); err != nil {
		nginx.stop()
		t.Fatal(err)
	}
	if err := nginx.stop(); err != nil {
		t.Fatal(err)
	}
	// Force early evaluation, then change request context. Cached map results
	// would misclassify rewritten APIs or lose the static auth-subrequest user.
	configPath := filepath.Join(directory, "nginx.conf")
	config, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	modified := strings.Replace(string(config), "location = /.hex/authz {", `location = /.hex/authz {
            set $early_subrequest_analytics "$hex_api_request:$hex_analytics_user";`, 1)
	modified = strings.Replace(modified, "location /api/ {", `location = /rewrite-api {
            set $early_rewrite_analytics "$hex_api_request:$hex_analytics_user";
            rewrite ^ /api/test-api last;
        }
        location = /internal-api {
            set $early_internal_analytics "$hex_api_request:$hex_analytics_user";
            try_files /missing /api/test-api;
        }
        location /api/ {`, 1)
	if err := os.WriteFile(configPath, []byte(modified), 0600); err != nil {
		t.Fatal(err)
	}
	nginx, err = app.startProcess(binary, app.Dir, nil, "-p", directory+string(filepath.Separator), "-c", configPath)
	if err != nil {
		t.Fatal(err)
	}
	defer nginx.stop()
	if err := waitForHTTP(ctx, base+"/healthz", nginx); err != nil {
		t.Fatal(err)
	}
	paths := []string{"/", "/folder/", "/app.js", "/missing.html", "/api/sites/demo/files", "/%61pi/test-api", "/static/../api/test-api", "//api//test-api", "/api%2ftest-api", "/rewrite-api", "/internal-api"}
	for _, path := range paths {
		request, err := http.NewRequestWithContext(ctx, "GET", base+path, nil)
		if err != nil {
			t.Fatal(err)
		}
		request.Host = "demo.localhost"
		request.Header.Set("X-Hex-Analytics-User", "Zm9yZ2VkLXVzZXI")
		if path != "/app.js" {
			request.Header.Set("Sec-Fetch-Dest", "document")
		}
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		if response.Header.Get("X-Hex-Analytics-User") != "" {
			t.Fatal("internal analytics header exposed by nginx")
		}
		if err := response.Body.Close(); err != nil {
			t.Fatal(err)
		}
	}
	query := hex.AnalyticsQuery{From: time.Now().UTC().Truncate(24 * time.Hour), Until: time.Now().UTC().Truncate(24*time.Hour).AddDate(0, 0, 1), Site: "demo"}
	var report hex.AnalyticsReport
	deadline := time.Now().Add(5 * time.Second)
	for {
		report, err = analytics.QueryAnalytics(ctx, query)
		if err != nil {
			t.Fatal(err)
		}
		if report.Traffic.Requests >= int64(len(paths)) || time.Now().After(deadline) {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if report.Traffic.Requests != int64(len(paths)) || report.Traffic.PageViews != 2 || report.Traffic.Errors != 1 || report.Traffic.Visitors != 1 || len(report.Users) != 1 || report.Users[0].Key != "verified-user" {
		t.Fatalf("completed logs are wrong: %+v; collector %+v", report, collector.Status())
	}
}

func TestNginxStaticAssetAuthorization(t *testing.T) {
	binary := os.Getenv("NGINX_BIN")
	if binary == "" {
		binary = "nginx"
	}
	if _, err := exec.LookPath(binary); err != nil {
		t.Skipf("NGINX unavailable: %v", err)
	}
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/hex/authz" {
			t.Errorf("unexpected backend path: %s", r.URL.Path)
			http.Error(w, "unexpected path", http.StatusInternalServerError)
			return
		}
		if r.Header.Get("X-Hex-Site") == "secret" {
			http.Error(w, "restricted", http.StatusForbidden)
			return
		}
		if strings.HasPrefix(r.Header.Get("X-Hex-Path"), "/admin/") {
			http.Error(w, "restricted page", http.StatusForbidden)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer backend.Close()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	directory := t.TempDir()
	for _, site := range []string{"open", "secret"} {
		siteDirectory := filepath.Join(directory, "sites", "public", "sites", site)
		if err := os.MkdirAll(filepath.Join(siteDirectory, "admin"), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(siteDirectory, "admin", "index.html"), []byte("admin"), 0644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(siteDirectory, "index.html"), []byte(site), 0644); err != nil {
			t.Fatal(err)
		}
	}
	app, err := New(strings.NewReader(""), io.Discard, os.Stderr)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	settings := devSettings{
		Port: port, APIPort: backend.Listener.Addr().(*net.TCPAddr).Port,
		DataDirectory: directory,
	}
	nginx, err := app.startNginx(ctx, settings, directory)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := nginx.stop(); err != nil {
			t.Error(err)
		}
	}()
	baseURL := fmt.Sprintf("http://127.0.0.1:%d", port)
	if err := waitForHTTP(ctx, baseURL+"/healthz", nginx); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		host string
		path string
		want int
	}{
		{"open.localhost", "/", http.StatusOK},
		{"secret.localhost", "/", http.StatusForbidden},
		{"secret.localhost", "/index.html", http.StatusForbidden},
		{"open.localhost", "/.hex/authz", http.StatusNotFound},
		{"open.localhost", "/admin/", http.StatusForbidden},
		{"open.localhost", "/admin/index.html", http.StatusForbidden},
		{"open.localhost", "/%61dmin/index.html", http.StatusForbidden},
		{"open.localhost", "/x/../admin/index.html", http.StatusForbidden},
	}
	for _, testCase := range cases {
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+testCase.path, nil)
		if err != nil {
			t.Fatal(err)
		}
		request.Host = testCase.host
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		if err := response.Body.Close(); err != nil {
			t.Fatal(err)
		}
		if response.StatusCode != testCase.want {
			t.Fatalf("%s%s: got %d, want %d", testCase.host, testCase.path, response.StatusCode, testCase.want)
		}
	}
}

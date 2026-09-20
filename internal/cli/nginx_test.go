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

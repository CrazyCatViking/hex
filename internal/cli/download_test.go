package cli

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestDownloadsRejectHTTPSDowngrades(t *testing.T) {
	var insecureRequests atomic.Int32
	insecure := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		insecureRequests.Add(1)
	}))
	defer insecure.Close()
	secure := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, insecure.URL, http.StatusFound)
	}))
	defer secure.Close()
	app, _ := testApp(t, t.TempDir())
	app.HTTP = secure.Client()
	if _, err := app.downloadBytes(context.Background(), secure.URL, 1024); err == nil || !strings.Contains(err.Error(), "HTTPS download redirect to HTTP") {
		t.Fatalf("unsafe redirect accepted: %v", err)
	}
	if insecureRequests.Load() != 0 {
		t.Fatal("download reached an insecure redirect destination")
	}
	if app.HTTP.CheckRedirect != nil {
		t.Fatal("download mutated the platform HTTP client's redirect behavior")
	}
}

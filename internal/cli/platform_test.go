package cli

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	hex "github.com/crazycatviking/hex/server"
	"github.com/crazycatviking/hex/server/providers/local"
	"github.com/crazycatviking/hex/server/providers/memory"
)

// testPlatform is a real Hex server publishing to a temporary directory, the
// way hex dev runs one locally.
type testPlatform struct {
	URL   string
	Sites string
}

func startPlatform(t *testing.T, configure func(*hex.Config)) testPlatform {
	t.Helper()
	root := t.TempDir()
	store, err := local.New(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Error(err)
		}
	})

	var handler http.Handler
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handler.ServeHTTP(w, r)
	}))
	t.Cleanup(server.Close)

	config := hex.Config{
		Sites:       store,
		Publisher:   store,
		Files:       memory.NewStore(),
		SiteBaseURL: "http://localhost:8080",
		Connection:  &hex.ConnectionConfig{Name: "Company Hex", Server: server.URL},
	}
	if configure != nil {
		configure(&config)
	}
	handler = hex.New(config)

	return testPlatform{URL: server.URL, Sites: filepath.Join(root, "public", "sites")}
}

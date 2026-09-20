package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	hex "github.com/hex-platform/hex/server"
)

func TestDisabledProvidersDoNotFallback(t *testing.T) {
	values := map[string]string{
		"HEX_SITES_PROVIDER":    "none",
		"HEX_FILES_PROVIDER":    "none",
		"HEX_DATABASE_PROVIDER": "none",
		"HEX_REALTIME_PROVIDER": "none",
		"DATABASE_URL":          "not-a-real-database",
		"AZURE_BLOB_ENDPOINT":   "not-a-real-endpoint",
	}
	getenv := func(key string) string {
		return values[key]
	}

	config, cleanup, err := configure(context.Background(), getenv)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()

	s := hex.New(config)
	w := httptest.NewRecorder()
	s.ServeHTTP(w, httptest.NewRequest("GET", "/api/hex/capabilities", nil))

	var capabilities map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &capabilities); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"sites", "files", "database", "realtime"} {
		if capabilities[key] != false {
			t.Fatalf("%s unexpectedly enabled", key)
		}
	}

	paths := []string{
		"/api/sites",
		"/api/sites/demo/files",
		"/api/sites/demo/db/tasks",
		"/api/sites/demo/realtime/updates",
	}
	for _, path := range paths {
		w := httptest.NewRecorder()
		s.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		if w.Code != http.StatusNotFound {
			t.Fatalf("%s: %d", path, w.Code)
		}
	}
}

func TestExplicitProviderConfigurationFailsEarly(t *testing.T) {
	cases := []struct {
		variable string
		provider string
	}{
		{"HEX_FILES_PROVIDER", "azureblob"},
		{"HEX_DATABASE_PROVIDER", "postgres"},
		{"HEX_REALTIME_PROVIDER", "redis"},
	}

	for _, testCase := range cases {
		t.Run(testCase.variable, func(t *testing.T) {
			_, _, err := configure(context.Background(), func(key string) string {
				if key == testCase.variable {
					return testCase.provider
				}

				return ""
			})
			if err == nil {
				t.Fatal("expected configuration error")
			}
		})
	}
}

func TestSiteOnlyConfiguration(t *testing.T) {
	values := map[string]string{
		"HEX_SITES_PROVIDER":    "filesystem",
		"HEX_SITES_DIR":         filepath.Join(t.TempDir(), "sites"),
		"HEX_FILES_PROVIDER":    "none",
		"HEX_DATABASE_PROVIDER": "none",
		"HEX_REALTIME_PROVIDER": "none",
	}
	getenv := func(key string) string {
		return values[key]
	}

	config, cleanup, err := configure(context.Background(), getenv)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()

	if config.Sites == nil || config.Files != nil || config.Database != nil || config.Realtime != nil {
		t.Fatal("unexpected provider configuration")
	}
}

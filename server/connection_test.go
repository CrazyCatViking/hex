package hex_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	hex "github.com/crazycatviking/hex/server"
	"github.com/crazycatviking/hex/server/providers/memory"
)

func TestConnectionDownloadDescribesConfiguredPlatform(t *testing.T) {
	server := hex.New(hex.Config{
		SiteBaseURL: "https://hex.example.com",
		Files:       memory.NewStore(),
		Connection: &hex.ConnectionConfig{
			Name:   "Company Hex",
			Server: "https://api.example.com",
			Publishing: &hex.PublishingConfig{
				Provider: "azure-files",
				URL:      "https://example.file.core.windows.net/sites/public/sites",
			},
		},
	})
	response := request(t, server, http.MethodGet, "/api/hex/config", nil, http.StatusOK)
	if response.Header().Get("Content-Disposition") != `attachment; filename="hex-platform.json"` {
		t.Fatal("connection settings must download as a JSON file")
	}
	if response.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("connection settings must not be cached by the gateway")
	}
	var document struct {
		Version      int                  `json:"version"`
		Server       string               `json:"server"`
		Publishing   hex.PublishingConfig `json:"publishing"`
		Capabilities map[string]any       `json:"capabilities"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &document); err != nil {
		t.Fatal(err)
	}
	if document.Version != 1 || document.Server != "https://api.example.com" || document.Publishing.Provider != "azure-files" {
		t.Fatalf("unexpected connection settings: %+v", document)
	}
	if document.Capabilities["files"] != true || document.Capabilities["database"] != false {
		t.Fatal("capabilities do not match configured providers")
	}
	if strings.Contains(response.Body.String(), "example.com/api/hex/config") {
		t.Fatal("request host should not determine platform configuration")
	}
}

func TestConnectionDownloadRejectsCredentialsAndRemoteFilesystem(t *testing.T) {
	for _, publishing := range []*hex.PublishingConfig{
		{Provider: "azure-files", URL: "https://example.file.core.windows.net/sites?sig=private-key"},
		{Provider: "filesystem", Root: "/sensitive/path"},
	} {
		server := hex.New(hex.Config{
			SiteBaseURL: "https://hex.example.com",
			Connection:  &hex.ConnectionConfig{Name: "Hex", Server: "https://hex.example.com", Publishing: publishing},
		})
		response := request(t, server, http.MethodGet, "/api/hex/config", nil, http.StatusInternalServerError)
		if strings.Contains(response.Body.String(), "private-key") || strings.Contains(response.Body.String(), "/sensitive/path") {
			t.Fatal("invalid configuration leaked into the response")
		}
	}
}

func TestConnectionEndpointIsOptionalAndRetainsOriginChecks(t *testing.T) {
	request(t, hex.New(hex.Config{}), http.MethodGet, "/api/hex/config", nil, http.StatusNotFound)
	server := hex.New(hex.Config{Connection: &hex.ConnectionConfig{Name: "Local", Server: "http://localhost:8080"}})
	r := httptest.NewRequest(http.MethodGet, "/api/hex/config", nil)
	r.Header.Set("Origin", "https://other.example")
	w := httptest.NewRecorder()
	server.ServeHTTP(w, r)
	if w.Code != http.StatusForbidden {
		t.Fatal("configuration endpoint bypasses normal API origin checks")
	}

	r = httptest.NewRequest(http.MethodGet, "/api/hex/config", nil)
	r.Header.Set("Sec-Fetch-Site", "cross-site")
	r.Header.Set("Sec-Fetch-Mode", "navigate")
	r.Header.Set("Sec-Fetch-Dest", "document")
	w = httptest.NewRecorder()
	server.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatal("browser navigation after hosting SSO must be able to download configuration")
	}
}

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
			Name:     "Company Hex",
			Server:   "https://api.example.com",
			Resource: "api://hex",
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
		Version      int            `json:"version"`
		Server       string         `json:"server"`
		Resource     string         `json:"resource"`
		Capabilities map[string]any `json:"capabilities"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &document); err != nil {
		t.Fatal(err)
	}
	if document.Version != 1 || document.Server != "https://api.example.com" || document.Resource != "api://hex" {
		t.Fatalf("unexpected connection settings: %+v", document)
	}
	if document.Capabilities["files"] != true || document.Capabilities["database"] != false {
		t.Fatal("capabilities do not match configured providers")
	}
	if strings.Contains(response.Body.String(), "example.com/api/hex/config") {
		t.Fatal("request host should not determine platform configuration")
	}
}

func TestConnectionAdvertisesCLISignIn(t *testing.T) {
	signIn := &hex.ConnectionConfig{
		Name:     "Hex",
		Server:   "https://hex.example.com",
		Resource: "api://11111111-2222-3333-4444-555555555555",
		ClientID: "66666666-7777-8888-9999-000000000000",
		TenantID: "contoso.onmicrosoft.com",
	}
	server := hex.New(hex.Config{SiteBaseURL: "https://hex.example.com", Connection: signIn})
	response := request(t, server, http.MethodGet, "/api/hex/config", nil, http.StatusOK).Body.String()
	if !strings.Contains(response, `"clientId":"66666666-7777-8888-9999-000000000000"`) || !strings.Contains(response, `"tenantId":"contoso.onmicrosoft.com"`) {
		t.Fatalf("sign-in settings missing: %s", response)
	}

	incomplete := *signIn
	incomplete.TenantID = ""
	server = hex.New(hex.Config{SiteBaseURL: "https://hex.example.com", Connection: &incomplete})
	request(t, server, http.MethodGet, "/api/hex/config", nil, http.StatusInternalServerError)
}

func TestConnectionDownloadRejectsCredentials(t *testing.T) {
	server := hex.New(hex.Config{
		SiteBaseURL: "https://hex.example.com",
		Connection:  &hex.ConnectionConfig{Name: "Hex", Server: "https://admin:private-key@hex.example.com"},
	})
	response := request(t, server, http.MethodGet, "/api/hex/config", nil, http.StatusInternalServerError)
	if strings.Contains(response.Body.String(), "private-key") {
		t.Fatal("invalid configuration leaked into the response")
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

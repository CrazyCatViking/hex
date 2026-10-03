package hex_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	hex "github.com/crazycatviking/hex/server"
	"github.com/crazycatviking/hex/server/providers/easyauth"
	"github.com/crazycatviking/hex/server/providers/memory"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type integrationBearerTransport struct{ token string }

func (t integrationBearerTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	copy := r.Clone(r.Context())
	copy.Header.Set("Authorization", "Bearer "+t.token)
	return http.DefaultTransport.RoundTrip(copy)
}

func TestRemoteMCPAuthenticatesEveryRequestAndValidatesAudienceScopeOrigin(t *testing.T) {
	registry := integrationRegistry(t, func(context.Context, hex.IntegrationContext, json.RawMessage) (json.RawMessage, error) {
		return json.RawMessage(`{"items":[{"id":"remote"}]}`), nil
	})
	state := memory.NewIntegrationState()
	runtime := integrationRuntime(t, registry, state, hex.IntegrationBudget{})
	platform := httptest.NewUnstartedServer(nil)
	base := "http://" + platform.Listener.Addr().String()
	hexServer := hex.New(hex.Config{Integrations: runtime, Identity: easyauth.Resolver{}, SiteBaseURL: "http://localhost:8080", Connection: &hex.ConnectionConfig{Server: base}, IntegrationMCP: &hex.IntegrationMCPConfig{ResourceURL: base + "/mcp", Audience: "hex-api", RequiredScopes: []string{"tools.read"}, AuthorizationServers: []string{"https://identity.example.com"}}})
	platform.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Simulate a trusted gateway: identity headers supplied by clients are
		// stripped and claims are injected only after authenticating the bearer.
		for _, name := range []string{"X-Ms-Client-Principal", "X-Ms-Client-Principal-Id", "X-Ms-Client-Principal-Name"} {
			r.Header.Del(name)
		}
		if r.URL.Path == "/.well-known/oauth-protected-resource" {
			hexServer.ServeHTTP(w, r)
			return
		}
		token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if token != "valid" && token != "wrong-audience" && token != "no-scope" {
			w.WriteHeader(401)
			return
		}
		audience, scope := "hex-api", "tools.read"
		if token == "wrong-audience" {
			audience = "other-service"
		}
		if token == "no-scope" {
			scope = "unrelated"
		}
		data, err := json.Marshal(map[string]any{"auth_typ": "aad", "claims": []map[string]string{{"typ": "groups", "val": "engineering"}, {"typ": "aud", "val": audience}, {"typ": "scp", "val": scope}}})
		if err != nil {
			t.Error(err)
			w.WriteHeader(500)
			return
		}
		r.Header.Set("X-Ms-Client-Principal", base64.StdEncoding.EncodeToString(data))
		r.Header.Set("X-Ms-Client-Principal-Id", "alice")
		hexServer.ServeHTTP(w, r)
	})
	platform.Start()
	defer platform.Close()
	metadata, err := http.Get(base + "/.well-known/oauth-protected-resource")
	if err != nil {
		t.Fatal(err)
	}
	if metadata.StatusCode != 200 {
		t.Fatal("public resource metadata unavailable")
	}
	metadata.Body.Close()
	for _, test := range []struct {
		token, origin string
		status        int
	}{{"wrong-audience", "", 401}, {"no-scope", "", 403}, {"valid", "https://evil.example", 403}} {
		r, err := http.NewRequest("POST", base+"/mcp/engineering", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{"name":"test","version":"1"}}}`))
		if err != nil {
			t.Fatal(err)
		}
		r.Header.Set("Authorization", "Bearer "+test.token)
		r.Header.Set("Origin", test.origin)
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Accept", "application/json, text/event-stream")
		response, err := http.DefaultClient.Do(r)
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		if response.StatusCode != test.status {
			t.Fatalf("%s/%s: got %d, want %d", test.token, test.origin, response.StatusCode, test.status)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	client := mcp.NewClient(&mcp.Implementation{Name: "remote-test", Version: "1"}, nil)
	httpClient := &http.Client{Transport: integrationBearerTransport{token: "valid"}}
	session, err := client.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: base + "/mcp/engineering", HTTPClient: httpClient}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	tools, err := session.ListTools(ctx, nil)
	if err != nil || len(tools.Tools) != 1 {
		t.Fatalf("remote discovery failed: %+v %v", tools, err)
	}
	result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "issues_summary", Arguments: map[string]any{"project": "web"}})
	if err != nil || result.IsError {
		t.Fatalf("remote MCP call failed: %+v %v", result, err)
	}
	result, err = session.CallTool(ctx, &mcp.CallToolParams{Name: "issues_summary", Arguments: map[string]any{"project": "private"}})
	if err != nil || !result.IsError {
		t.Fatal("remote MCP bypassed resource authorization")
	}
	audits, err := state.ListToolAudit(ctx, hex.ToolAuditQuery{})
	if err != nil || len(audits) != 2 || audits[0].Transport != "mcp-http" {
		t.Fatalf("remote calls missing from shared audit: %+v %v", audits, err)
	}
}

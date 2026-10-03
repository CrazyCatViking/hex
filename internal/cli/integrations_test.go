package cli

import (
	"context"
	"encoding/json"
	"io"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	hex "github.com/crazycatviking/hex/server"
	"github.com/crazycatviking/hex/server/providers/memory"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func cliIntegrationFixture(t *testing.T, directory string) (*httptest.Server, *hex.IntegrationRegistry) {
	t.Helper()
	registry := new(hex.IntegrationRegistry)
	definition := hex.IntegrationToolDefinition{ActionDefinition: hex.ActionDefinition{Name: "usage_summary", Description: "Read approved production statistics.", InputSchema: json.RawMessage(`{"type":"object","properties":{"days":{"type":"integer","minimum":1,"maximum":7}},"required":["days"],"additionalProperties":false}`), OutputSchema: json.RawMessage(`{"type":"object","properties":{"users":{"type":"integer"}},"required":["users"],"additionalProperties":false}`)}, Integration: "production", Version: "v1", ReadOnly: true}
	if err := registry.RegisterTool(hex.IntegrationTool{Definition: definition, Principals: []string{"user:alice"}, Handler: func(context.Context, hex.IntegrationContext, json.RawMessage) (json.RawMessage, error) {
		return json.RawMessage(`{"users":42}`), nil
	}}); err != nil {
		t.Fatal(err)
	}
	if err := registry.RegisterBundle(hex.ToolBundle{Name: "engineering", Description: "Engineering summaries", Principals: []string{"user:alice"}, Tools: []string{"usage_summary"}}); err != nil {
		t.Fatal(err)
	}
	runtime, err := hex.NewIntegrationRuntime(registry, memory.NewIntegrationState(), hex.IntegrationBudget{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	platform := httptest.NewUnstartedServer(nil)
	base := "http://" + platform.Listener.Addr().String()
	platform.Config.Handler = hex.New(hex.Config{Integrations: runtime, Identity: hex.StaticIdentity{Identity: hex.Identity{ID: "alice"}}, SiteBaseURL: "http://localhost:8080", Connection: &hex.ConnectionConfig{Server: base}})
	platform.Start()
	t.Cleanup(platform.Close)
	saveReadProfile(t, directory, platform.URL)
	return platform, registry
}

func TestIntegrationCLIAndScopedStdioMCP(t *testing.T) {
	directory := t.TempDir()
	_, registry := cliIntegrationFixture(t, directory)
	for _, args := range [][]string{{"integrations", "list"}, {"tools", "list", "--bundle", "engineering"}, {"tools", "describe", "usage_summary", "--bundle", "engineering"}, {"mcp", "config", "--bundle", "engineering"}} {
		output := run(t, directory, args...)
		if !json.Valid([]byte(output)) {
			t.Fatalf("CLI did not return JSON: %s", output)
		}
	}
	if err := os.WriteFile(filepath.Join(directory, "input.json"), []byte(`{"days":3}`), 0600); err != nil {
		t.Fatal(err)
	}
	if output := run(t, directory, "tools", "run", "usage_summary", "--bundle", "engineering", "--input", "@input.json"); !strings.Contains(output, "42") {
		t.Fatal(output)
	}
	if err := os.WriteFile(filepath.Join(directory, "bad.json"), []byte(`{"days":1000}`), 0600); err != nil {
		t.Fatal(err)
	}
	app, _ := testApp(t, directory)
	if err := app.Execute(context.Background(), []string{"tools", "run", "usage_summary", "--bundle", "engineering", "--input", "@bad.json"}, "test"); err == nil {
		t.Fatal("CLI accepted unbounded input")
	}
	// Exercise the actual CLI stdio entry point with the official SDK client.
	inputReader, inputWriter := io.Pipe()
	outputReader, outputWriter := io.Pipe()
	app, _, err := newMCPTestApp(directory, inputReader, outputWriter)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- app.Execute(ctx, []string{"mcp", "serve", "--bundle", "engineering"}, "test") }()
	client := mcp.NewClient(&mcp.Implementation{Name: "stdio-agent", Version: "1"}, nil)
	session, err := client.Connect(ctx, &mcp.IOTransport{Reader: outputReader, Writer: inputWriter}, nil)
	if err != nil {
		t.Fatal(err)
	}
	tools, err := session.ListTools(ctx, nil)
	if err != nil || len(tools.Tools) != 1 {
		t.Fatalf("stdio discovery failed: %+v %v", tools, err)
	}
	result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "usage_summary", Arguments: map[string]any{"days": 3}})
	if err != nil || result.IsError {
		t.Fatalf("stdio call failed: %+v %v", result, err)
	}
	if err := registry.SetToolEnabled("usage_summary", false); err != nil {
		t.Fatal(err)
	}
	tools, err = session.ListTools(ctx, nil)
	if err != nil || len(tools.Tools) != 0 {
		t.Fatal("stdio catalogue did not refresh revocation")
	}
	result, err = session.CallTool(ctx, &mcp.CallToolParams{Name: "usage_summary", Arguments: map[string]any{"days": 3}})
	if err != nil || !result.IsError {
		t.Fatal("stdio invocation bypassed current server policy")
	}
	if err := session.Close(); err != nil {
		t.Fatal(err)
	}
	inputWriter.Close()
	outputWriter.Close()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("stdio CLI did not stop on EOF")
	}
}

func newMCPTestApp(directory string, input io.Reader, output io.Writer) (*App, io.Writer, error) {
	app, err := New(input, output, io.Discard)
	if err != nil {
		return nil, nil, err
	}
	app.Dir = directory
	return app, io.Discard, nil
}

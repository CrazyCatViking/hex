package cli

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestMCPProjectLifecycle(t *testing.T) {
	directory := t.TempDir()
	t.Setenv("HEX_CONFIG_DIR", filepath.Join(directory, "profiles"))
	platform := startPlatform(t, nil)
	run(t, directory, "setup", platform.URL, "--name", "company", "--json")
	app, output := testApp(t, directory)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	server, err := app.newMCPServer(ctx, "test").Connect(ctx, serverTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	client, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil).Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	tools, err := client.ListTools(ctx, nil)
	if err != nil || len(tools.Tools) != 8 {
		t.Fatalf("tools: %+v %v", tools, err)
	}
	call := func(name string, input map[string]any, wantError bool) *mcp.CallToolResult {
		t.Helper()
		result, err := client.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: input})
		if err != nil {
			t.Fatal(err)
		}
		if result.IsError != wantError {
			t.Fatalf("%s: %+v", name, result)
		}
		return result
	}
	call("hex_get_docs", map[string]any{}, false)
	call("hex_get_context", map[string]any{}, false)
	call("hex_login_status", map[string]any{"loginID": "unknown"}, true)
	call("hex_init", map[string]any{"projectPath": "relative"}, true)
	project := filepath.Join(directory, "mcp-app")
	input := map[string]any{"projectPath": project}
	call("hex_init", input, false)
	call("hex_init", input, true)
	call("hex_publish", input, true) // Never silently builds or publishes missing output.
	createBuild(t, project)
	call("hex_capabilities", input, false)
	call("hex_publish", input, false)
	call("hex_sites", input, false)
	data, err := os.ReadFile(filepath.Join(platform.Sites, "mcp-app", "index.html"))
	if err != nil || string(data) != "test website" {
		t.Fatalf("published output: %s %v", data, err)
	}
	if output.Len() != 0 {
		t.Fatalf("tool output leaked into protocol stream: %s", output)
	}
	if _, err := os.Stat(filepath.Join(directory, "hex.json")); !os.IsNotExist(err) {
		t.Fatalf("used server working directory instead of explicit project path: %v", err)
	}
}

func TestMCPStdioProtocol(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	serverInput, clientOutput := io.Pipe()
	clientInput, serverOutput := io.Pipe()
	defer serverOutput.Close()
	defer clientOutput.Close()
	app, err := New(serverInput, serverOutput, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- app.Execute(ctx, []string{"mcp", "serve"}, "test") }()
	client, err := mcp.NewClient(&mcp.Implementation{Name: "stdio-test", Version: "1"}, nil).Connect(ctx, &mcp.IOTransport{Reader: clientInput, Writer: clientOutput}, nil)
	if err != nil {
		t.Fatal(err)
	}
	tools, err := client.ListTools(ctx, nil)
	if err != nil || len(tools.Tools) != 8 {
		t.Fatalf("stdio discovery: %+v %v", tools, err)
	}
	if err := client.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
}

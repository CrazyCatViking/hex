package cli

import (
	"context"
	"encoding/json"
	"io"
	"sync"

	hex "github.com/crazycatviking/hex/server"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/spf13/cobra"
)

func (a *App) mcpCommand() *cobra.Command {
	command := &cobra.Command{Use: "mcp", Short: "Expose an approved company tool bundle through MCP"}
	options := &integrationCommandOptions{}
	serve := &cobra.Command{Use: "serve", Short: "Serve a bundle over stdio using your saved Hex sign-in", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, args []string) error {
		project, err := options.config(a)
		if err != nil {
			return err
		}
		// Authentication prompts and diagnostic text must never enter MCP stdout.
		a.Interactive = false
		server, err := a.integrationMCPServer(cmd.Context(), project, options.bundle)
		if err != nil {
			return err
		}
		reader, ok := a.In.(io.ReadCloser)
		if !ok {
			reader = io.NopCloser(a.In)
		}
		return server.Run(cmd.Context(), &mcp.IOTransport{Reader: reader, Writer: mcpOutput{a.Out}, MaxLineLength: 2 * hex.MaxIntegrationInputBytes})
	}}
	options.register(serve)
	command.AddCommand(serve)
	configOptions := &integrationCommandOptions{}
	configuration := &cobra.Command{Use: "config", Short: "Print a non-secret MCP client configuration", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, args []string) error {
		if _, err := configOptions.config(a); err != nil {
			return err
		}
		arguments := []string{"mcp", "serve", "--bundle", configOptions.bundle}
		if configOptions.platform != "" {
			arguments = append(arguments, "--platform", configOptions.platform)
		}
		return a.printJSON(map[string]any{"mcpServers": map[string]any{"company-hex": map[string]any{"command": "hex", "args": arguments}}})
	}}
	configOptions.register(configuration)
	command.AddCommand(configuration)
	return command
}

type mcpOutput struct{ io.Writer }

func (mcpOutput) Close() error { return nil }

func (a *App) integrationMCPServer(ctx context.Context, project Project, bundle string) (*mcp.Server, error) {
	definitions, err := a.integrationTools(ctx, project, bundle)
	if err != nil {
		return nil, err
	}
	invoke := func(ctx context.Context, name string, input json.RawMessage) (json.RawMessage, error) {
		return a.runIntegrationTool(ctx, project, bundle, name, input)
	}
	server := hex.NewIntegrationMCPServer(definitions, invoke)
	var mu sync.Mutex
	// Refresh discovery on every tools/list. Revocations and updated contracts
	// are also enforced by the remote backend on every invocation.
	server.AddReceivingMiddleware(func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, request mcp.Request) (mcp.Result, error) {
			if method == "tools/list" {
				mu.Lock()
				defer mu.Unlock()
				updated, err := a.integrationTools(ctx, project, bundle)
				if err != nil {
					return nil, err
				}
				names := make([]string, 0, len(definitions))
				for _, definition := range definitions {
					names = append(names, definition.Name)
				}
				server.RemoveTools(names...)
				for _, definition := range updated {
					server.AddTool(&mcp.Tool{Name: definition.Name, Description: definition.Description, InputSchema: definition.InputSchema, OutputSchema: definition.OutputSchema, Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true}}, func(ctx context.Context, request *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
						output, err := invoke(ctx, request.Params.Name, request.Params.Arguments)
						if err != nil {
							return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: err.Error()}}}, nil
						}
						return &mcp.CallToolResult{StructuredContent: output, Content: []mcp.Content{&mcp.TextContent{Text: string(output)}}}, nil
					})
				}
				definitions = updated
			}
			return next(ctx, method, request)
		}
	})
	return server, nil
}

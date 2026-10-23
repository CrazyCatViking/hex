package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/spf13/cobra"
)

type mcpProjectInput struct {
	ProjectPath string `json:"projectPath" jsonschema:"Absolute project directory accessible to this local MCP process, not a path in an inaccessible sandbox"`
	Platform    string `json:"platform,omitempty" jsonschema:"Optional saved platform profile; otherwise use project/default selection"`
	Workspace   string `json:"workspace,omitempty" jsonschema:"Optional explicit directory workspace ID"`
	Directory   string `json:"directory,omitempty" jsonschema:"Optional saved workspace directory"`
	Account     string `json:"account,omitempty" jsonschema:"Optional saved directory account alias"`
}

func (input mcpProjectInput) flags() []string {
	var flags []string
	for _, pair := range [][2]string{{"platform", input.Platform}, {"workspace", input.Workspace}, {"directory", input.Directory}, {"account", input.Account}} {
		if pair[1] != "" {
			flags = append(flags, "--"+pair[0], pair[1])
		}
	}
	return flags
}

type mcpContextInput struct {
	ProjectPath string `json:"projectPath,omitempty" jsonschema:"Optional absolute project directory; omit to inspect the saved default platform without a project"`
	Platform    string `json:"platform,omitempty" jsonschema:"Optional saved platform profile"`
}

type mcpInitInput struct {
	mcpProjectInput
	Name string `json:"name,omitempty" jsonschema:"Optional site name, defaulting to the project directory name"`
}

type mcpCapabilitiesInput struct {
	mcpProjectInput
	Refresh bool `json:"refresh,omitempty" jsonschema:"Fetch live capabilities instead of using cached profile capabilities"`
}

type mcpSitesInput struct {
	mcpProjectInput
	Mine bool `json:"mine,omitempty" jsonschema:"List sites owned by the signed-in user, including private publications"`
}

type mcpPublishInput struct {
	mcpProjectInput
	ConfirmLarge bool `json:"confirmLarge,omitempty" jsonschema:"Explicitly confirm unusually large publications, equivalent to hex publish --yes"`
}

// Each call gets its own command state and output buffers. The existing CLI
// handlers remain responsible for configuration, authentication and validation.
func (a *App) mcpApp(ctx context.Context, directory string) (*App, error) {
	if !filepath.IsAbs(directory) {
		return nil, errors.New("projectPath must be an absolute directory visible to the local Hex process")
	}
	child, err := New(strings.NewReader(""), io.Discard, a.Err)
	if err != nil {
		return nil, err
	}
	child.Dir = filepath.Clean(directory)
	child.HTTP = a.HTTP
	child.OpenBrowser = a.OpenBrowser
	child.signInHost = a.signInHost
	child.signInHTTP = a.signInHTTP
	child.commandContext = ctx
	return child, nil
}

func (a *App) mcpInvoke(ctx context.Context, version string, input mcpProjectInput, args ...string) (*mcp.CallToolResult, error) {
	child, err := a.mcpApp(ctx, input.ProjectPath)
	if err != nil {
		return nil, err
	}
	var output, diagnostics bytes.Buffer
	child.Out = &output
	child.Err = &diagnostics
	args = append(args, input.flags()...)
	if err := child.Execute(ctx, args, version); err != nil {
		if strings.Contains(err.Error(), "hex login") {
			return nil, fmt.Errorf("%w; in this harness, call hex_login with the same projectPath and account/platform selectors, then hex_login_status", err)
		}
		return nil, err
	}
	text := output.String()
	if diagnostics.Len() > 0 {
		text += "\n" + diagnostics.String()
	}
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: text}}}, nil
}

func (a *App) newMCPServer(ctx context.Context, version string) *mcp.Server {
	server := mcp.NewServer(&mcp.Implementation{Name: "hex", Version: version}, &mcp.ServerOptions{
		Instructions: "Hex is the company web-app platform github.com/crazycatviking/hex. Start with hex_get_context and hex_get_docs. The harness owns file editing, dependency installation, builds and testing. Hex initializes configuration and publishes built assets. All project operations require an absolute path shared with this local process. If sign-in is required, call hex_login, show the browser link and let the user complete company sign-in/MFA, then check hex_login_status and retry. Never request passwords or tokens in chat.",
	})
	a.addMCPLoginTools(ctx, server, version)
	readOnly := &mcp.ToolAnnotations{ReadOnlyHint: true}
	mcp.AddTool(server, &mcp.Tool{Name: "hex_get_docs", Description: "Read authoritative, CLI-version-matched Hex app development and publishing guidance before creating an app. No web search needed.", Annotations: readOnly},
		func(ctx context.Context, request *mcp.CallToolRequest, input struct{}) (*mcp.CallToolResult, any, error) {
			data, err := assets.ReadFile("assets/SKILL.md")
			if err != nil {
				return nil, nil, err
			}
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: string(data)}}}, nil, nil
		})
	mcp.AddTool(server, &mcp.Tool{Name: "hex_get_context", Description: "Identify the company Hex platform and inspect configuration without signing in or requiring an existing project. Returns actionable setup errors.", Annotations: readOnly},
		func(ctx context.Context, request *mcp.CallToolRequest, input mcpContextInput) (*mcp.CallToolResult, any, error) {
			result := map[string]any{"version": version, "platformProduct": "github.com/crazycatviking/hex", "sdk": "@crazycatviking/hex", "workflow": "Read hex_get_docs; initialize configuration; use harness tools to edit, build and test; publish built assets."}
			var project Project
			var err error
			if input.ProjectPath != "" {
				var child *App
				child, err = a.mcpApp(ctx, input.ProjectPath)
				if err == nil {
					project, err = child.commandConfig(input.Platform, false)
				}
			} else {
				var connection *Connection
				connection, project.Platform, err = loadProfile(input.Platform, false)
				if err == nil {
					project.Server = connection.Server
					project.Capabilities = connection.Capabilities
				}
				if input.Platform == "" {
					store, _, directoryError := readDirectories()
					if directoryError != nil {
						err = directoryError
					} else if store.Active != nil {
						// Report the selection without resolving membership or signing in.
						project = Project{Workspace: &store.Active.workspacePin}
						result["account"] = store.Active.Account
						result["next"] = "Call hex_capabilities with an explicit projectPath to resolve current workspace membership and capabilities."
						err = nil
					}
				}
			}
			result["configured"] = err == nil
			if err != nil {
				result["setupError"] = err.Error()
			} else {
				result["server"] = project.Server
				result["platform"] = project.Platform
				result["workspace"] = project.Workspace
				result["capabilities"] = project.Capabilities
			}
			data, err := json.Marshal(result)
			if err != nil {
				return nil, nil, err
			}
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: string(data)}}, StructuredContent: result}, nil, nil
		})
	mcp.AddTool(server, &mcp.Tool{Name: "hex_init", Description: "Create hex.json and Hex agent guidance in a project. Does not scaffold source, install dependencies or build. Refuses to overwrite hex.json."},
		func(ctx context.Context, request *mcp.CallToolRequest, input mcpInitInput) (*mcp.CallToolResult, any, error) {
			args := []string{"init"}
			if input.Name != "" {
				args = append(args, "--name", input.Name)
			}
			result, err := a.mcpInvoke(ctx, version, input.mcpProjectInput, args...)
			return result, nil, err
		})
	mcp.AddTool(server, &mcp.Tool{Name: "hex_capabilities", Description: "Discover backend capabilities of the selected Hex platform for this project.", Annotations: readOnly},
		func(ctx context.Context, request *mcp.CallToolRequest, input mcpCapabilitiesInput) (*mcp.CallToolResult, any, error) {
			args := []string{"capabilities"}
			if input.Refresh {
				args = append(args, "--refresh")
			}
			result, err := a.mcpInvoke(ctx, version, input.mcpProjectInput, args...)
			return result, nil, err
		})
	mcp.AddTool(server, &mcp.Tool{Name: "hex_sites", Description: "List published apps on the project's selected Hex platform.", Annotations: readOnly},
		func(ctx context.Context, request *mcp.CallToolRequest, input mcpSitesInput) (*mcp.CallToolResult, any, error) {
			args := []string{"sites", "--json"}
			if input.Mine {
				args = append(args, "--mine")
			}
			result, err := a.mcpInvoke(ctx, version, input.mcpProjectInput, args...)
			return result, nil, err
		})
	mcp.AddTool(server, &mcp.Tool{Name: "hex_publish", Description: "Publish an existing Hex project's built assets and return its URL. The harness must build and test first. Preserves portal access-policy edits using normal CLI merge behavior. Does not run builds."},
		func(ctx context.Context, request *mcp.CallToolRequest, input mcpPublishInput) (*mcp.CallToolResult, any, error) {
			args := []string{"publish"}
			if input.ConfirmLarge {
				args = append(args, "--yes")
			}
			result, err := a.mcpInvoke(ctx, version, input.mcpProjectInput, args...)
			return result, nil, err
		})
	return server
}

type mcpWriter struct{ io.Writer }

func (mcpWriter) Close() error { return nil }

func (a *App) mcpCommand(version string) *cobra.Command {
	root := &cobra.Command{Use: "mcp", Short: "Local Model Context Protocol integration"}
	root.AddCommand(&cobra.Command{
		Use: "serve", Short: "Serve Hex tools over stdio; the harness owns editing and builds", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := context.WithCancel(cmd.Context())
			defer cancel()
			stopUpdater := a.startAutomaticUpdate(ctx, version)
			defer stopUpdater()
			reader, ok := a.In.(io.ReadCloser)
			if !ok {
				reader = io.NopCloser(a.In)
			}
			if err := a.newMCPServer(ctx, version).Run(ctx, &mcp.IOTransport{Reader: reader, Writer: mcpWriter{a.Out}}); err != nil {
				return fmt.Errorf("serve MCP: %w", err)
			}
			return nil
		},
	})
	return root
}

package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	hex "github.com/crazycatviking/hex/server"
	"github.com/spf13/cobra"
)

func (a *App) integrationsCommand() *cobra.Command {
	var platform string
	command := &cobra.Command{Use: "integrations", Short: "Discover approved company integrations and tool bundles"}
	command.PersistentFlags().StringVar(&platform, "platform", "", "Saved platform profile")
	command.AddCommand(&cobra.Command{Use: "list", Short: "List the bundles permitted for your identity", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, args []string) error {
		project, err := a.commandConfig(platform, false)
		if err != nil {
			return err
		}
		data, err := a.apiRequest(cmd.Context(), project, "/api/hex/integrations")
		if err != nil {
			return err
		}
		return a.printJSON(data)
	}})
	var user, tool, before string
	var limit int
	audit := &cobra.Command{Use: "audit", Short: "Read integration audit metadata (requires an explicit audit grant)", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, args []string) error {
		if limit < 1 || limit > 100 {
			return errors.New("--limit must be 1–100")
		}
		project, err := a.commandConfig(platform, false)
		if err != nil {
			return err
		}
		query := url.Values{"user": {user}, "tool": {tool}, "before": {before}, "limit": {fmt.Sprint(limit)}}
		data, err := a.apiRequest(cmd.Context(), project, "/api/hex/integrations/audit?"+query.Encode())
		if err != nil {
			return err
		}
		return a.printJSON(data)
	}}
	audit.Flags().StringVar(&user, "user", "", "Filter by verified identity ID")
	audit.Flags().StringVar(&tool, "tool", "", "Filter by tool name")
	audit.Flags().StringVar(&before, "before", "", "Page before an RFC3339 timestamp")
	audit.Flags().IntVar(&limit, "limit", 50, "Maximum audit records (1–100)")
	command.AddCommand(audit)
	return command
}

type integrationCommandOptions struct{ platform, bundle string }

func (o *integrationCommandOptions) register(command *cobra.Command) {
	command.PersistentFlags().StringVar(&o.platform, "platform", "", "Saved platform profile")
	command.PersistentFlags().StringVar(&o.bundle, "bundle", "", "Approved tool bundle (required)")
}

func (o *integrationCommandOptions) config(a *App) (Project, error) {
	if o.bundle == "" {
		return Project{}, errors.New("provide --bundle")
	}
	if err := validateIdentifier("bundle", o.bundle); err != nil {
		return Project{}, err
	}
	return a.commandConfig(o.platform, false)
}

func integrationToolsPath(bundle string) string {
	return "/api/hex/integrations/bundles/" + bundle + "/tools"
}

func (a *App) integrationTools(ctx context.Context, project Project, bundle string) ([]hex.IntegrationToolDefinition, error) {
	data, err := a.apiRequest(ctx, project, integrationToolsPath(bundle))
	if err != nil {
		return nil, err
	}
	var tools []hex.IntegrationToolDefinition
	if err := json.Unmarshal(data, &tools); err != nil {
		return nil, err
	}
	if len(tools) > hex.MaxBundleTools {
		return nil, errors.New("server bundle exceeds the tool limit")
	}
	for _, tool := range tools {
		if err := validateIntegrationContract(tool); err != nil {
			return nil, err
		}
	}
	return tools, nil
}

func validateIntegrationContract(tool hex.IntegrationToolDefinition) error {
	if err := validateIdentifier("tool", tool.Name); err != nil {
		return err
	}
	limits := tool.Limits
	if !tool.ReadOnly || limits.InputBytes < 1 || limits.InputBytes > hex.MaxIntegrationInputBytes || limits.OutputBytes < 1 || limits.OutputBytes > hex.MaxIntegrationOutputBytes || limits.TimeoutSeconds < 1 || limits.TimeoutSeconds > 60 {
		return errors.New("server returned an unsupported integration contract")
	}
	return tool.ValidateSchemas()
}

func (a *App) toolsCommand() *cobra.Command {
	options := &integrationCommandOptions{}
	command := &cobra.Command{Use: "tools", Short: "Discover and run curated company tools"}
	options.register(command)
	command.AddCommand(&cobra.Command{Use: "list", Short: "List the tools permitted in a bundle", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, args []string) error {
		project, err := options.config(a)
		if err != nil {
			return err
		}
		tools, err := a.integrationTools(cmd.Context(), project, options.bundle)
		if err != nil {
			return err
		}
		return a.printJSON(tools)
	}})
	command.AddCommand(a.integrationToolCommand(options, false), a.integrationToolCommand(options, true))
	return command
}

func (a *App) integrationToolCommand(options *integrationCommandOptions, execute bool) *cobra.Command {
	var input string
	name, short := "describe", "Show an approved tool contract and limits"
	if execute {
		name, short = "run", "Validate input and run an approved read-only tool"
	}
	command := &cobra.Command{Use: name + " <tool>", Short: short, Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		if err := validateIdentifier("tool", args[0]); err != nil {
			return err
		}
		project, err := options.config(a)
		if err != nil {
			return err
		}
		if !execute {
			data, err := a.apiRequest(cmd.Context(), project, integrationToolsPath(options.bundle)+"/"+args[0])
			if err != nil {
				return err
			}
			return a.printJSON(data)
		}
		if !strings.HasPrefix(input, "@") || len(input) == 1 {
			return errors.New("provide --input @<JSON-file>")
		}
		payload, err := readLimitedFile(resolvePath(a.Dir, strings.TrimPrefix(input, "@")), hex.MaxIntegrationInputBytes)
		if err != nil {
			return err
		}
		output, err := a.runIntegrationTool(cmd.Context(), project, options.bundle, args[0], payload)
		if err != nil {
			return err
		}
		return a.printJSON(output)
	}}
	if execute {
		command.Flags().StringVar(&input, "input", "", "JSON input file as @<path> (required)")
	}
	return command
}

func (a *App) runIntegrationTool(ctx context.Context, project Project, bundle, name string, input json.RawMessage) (json.RawMessage, error) {
	if err := validateIdentifier("tool", name); err != nil {
		return nil, err
	}
	path := integrationToolsPath(bundle) + "/" + name
	data, err := a.apiRequest(ctx, project, path)
	if err != nil {
		return nil, err
	}
	var definition hex.IntegrationToolDefinition
	if err := json.Unmarshal(data, &definition); err != nil {
		return nil, err
	}
	if definition.Name != name {
		return nil, errors.New("server returned a different tool contract")
	}
	if err := validateIntegrationContract(definition); err != nil {
		return nil, err
	}
	if len(input) > definition.Limits.InputBytes {
		return nil, errors.New("tool input exceeds its byte limit")
	}
	if err := definition.ValidateInput(input); err != nil {
		return nil, fmt.Errorf("invalid tool input: %w", err)
	}
	ctx, cancel := context.WithTimeout(ctx, time.Duration(definition.Limits.TimeoutSeconds+10)*time.Second)
	defer cancel()
	client := *a.HTTP
	client.Timeout = time.Duration(definition.Limits.TimeoutSeconds+10) * time.Second
	result, err := a.apiSendUsingClient(ctx, project, http.MethodPost, path, bytes.NewReader(input), int64(len(input)), "application/json", &client)
	if err != nil {
		return nil, err
	}
	if len(result) > definition.Limits.OutputBytes+1 {
		return nil, errors.New("tool result exceeds its byte limit")
	}
	if err := definition.ValidateOutput(result); err != nil {
		return nil, fmt.Errorf("invalid tool output: %w", err)
	}
	return result, nil
}

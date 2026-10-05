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
	"text/tabwriter"
	"time"

	hex "github.com/crazycatviking/hex/server"
	"github.com/spf13/cobra"
)

var integrationNamePattern = siteNamePattern

func (a *App) integrationsCommand() *cobra.Command {
	options := &siteCommandOptions{}
	command := &cobra.Command{Use: "integrations", Short: "Discover and call the platform's integrations"}
	options.register(command)

	var jsonOutput bool
	list := &cobra.Command{
		Use: "list", Short: "List integrations and the endpoints you may use on a site", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			project, err := options.config(a)
			if err != nil {
				return err
			}
			integrations, err := a.siteIntegrations(cmd.Context(), project, options.site)
			if err != nil {
				return err
			}
			if jsonOutput {
				return a.printJSON(integrations)
			}
			return printIntegrations(a, integrations)
		},
	}
	list.Flags().BoolVar(&jsonOutput, "json", false, "Print JSON, including endpoint schemas")

	catalog := &cobra.Command{
		Use: "catalog", Short: "List every integration, endpoint and permission on the platform", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			project, err := a.commandConfig(options.platform, false)
			if err != nil {
				return err
			}
			data, err := a.apiRequest(cmd.Context(), project, "/api/hex/integrations")
			if err != nil {
				return err
			}
			return a.printJSON(data)
		},
	}

	command.AddCommand(list, catalog, a.integrationCallCommand(options), a.integrationApprovalsCommand(options))
	command.AddCommand(a.integrationCodegenCommand(options))
	command.AddCommand(a.integrationApprovalCommands(options)...)
	return command
}

func (a *App) siteIntegrations(ctx context.Context, project Project, site string) ([]hex.SiteIntegration, error) {
	data, err := a.apiRequest(ctx, project, "/api/sites/"+site+"/integrations")
	if err != nil {
		var status *apiStatusError
		if errors.As(err, &status) && status.Status == http.StatusNotFound {
			return nil, errors.New("this platform has no integrations")
		}
		return nil, err
	}
	var integrations []hex.SiteIntegration
	if err := json.Unmarshal(data, &integrations); err != nil {
		return nil, fmt.Errorf("unexpected integration list: %w", err)
	}
	return integrations, nil
}

func printIntegrations(a *App, integrations []hex.SiteIntegration) error {
	table := tabwriter.NewWriter(a.Out, 0, 4, 2, ' ', 0)
	fmt.Fprintln(table, "ENDPOINT\tALLOWED\tWRITE\tPERMISSION\tNOTES")
	for _, integration := range integrations {
		notes := integrationNotes(integration)
		for _, endpoint := range integration.Endpoints {
			fmt.Fprintf(table, "%s.%s\t%s\t%s\t%s\t%s\n", integration.Name, endpoint.Name,
				yesNo(endpoint.Allowed), yesNo(endpoint.Write), endpoint.Permission, notes)
		}
	}
	return table.Flush()
}

func integrationNotes(integration hex.SiteIntegration) string {
	var notes []string
	if integration.RequiresApproval && integration.Approval != hex.ApprovalApproved {
		status := integration.Approval
		if status == "" {
			status = "not requested"
		}
		notes = append(notes, "needs approval ("+status+")")
	}
	if integration.Connection != nil && !integration.Connection.Connected {
		notes = append(notes, "connect with: hex connections connect "+integration.Connection.Name)
	}
	return strings.Join(notes, "; ")
}

func yesNo(value bool) string {
	if value {
		return "yes"
	}
	return "no"
}

func (a *App) integrationCallCommand(options *siteCommandOptions) *cobra.Command {
	var input string
	command := &cobra.Command{
		Use:   "call <integration>.<endpoint>",
		Short: "Validate input and call an integration endpoint",
		Long: "Call an integration endpoint as yourself on a site. --input takes inline JSON " +
			"or @<file>; it defaults to {}. The input is checked against the endpoint's " +
			"schema before anything is sent.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			integrationName, endpointName, found := strings.Cut(args[0], ".")
			if !found || !integrationNamePattern.MatchString(integrationName) || !integrationNamePattern.MatchString(endpointName) {
				return errors.New("name the endpoint as <integration>.<endpoint>, for example slack.users")
			}
			payload, err := a.jsonInput(input)
			if err != nil {
				return err
			}
			project, err := options.config(a)
			if err != nil {
				return err
			}
			integrations, err := a.siteIntegrations(cmd.Context(), project, options.site)
			if err != nil {
				return err
			}
			endpoint, err := findEndpoint(integrations, integrationName, endpointName)
			if err != nil {
				return err
			}
			definition := hex.ActionDefinition{Name: endpoint.Name, Description: endpoint.Description, InputSchema: endpoint.InputSchema}
			if err := definition.ValidateInput(payload); err != nil {
				return fmt.Errorf("invalid input for %s: %w", args[0], err)
			}

			path := "/api/sites/" + options.site + "/integrations/" + integrationName + "/" + endpointName
			result, err := a.apiSend(cmd.Context(), project, http.MethodPost, path, bytes.NewReader(payload), int64(len(payload)), "application/json")
			if err != nil {
				return connectionHint(err)
			}
			return a.printJSON(result)
		},
	}
	command.Flags().StringVar(&input, "input", "", "Inline JSON input or @<file>")
	return command
}

// jsonInput reads inline JSON or @<file>, defaulting to an empty object.
func (a *App) jsonInput(input string) (json.RawMessage, error) {
	if input == "" {
		return json.RawMessage(`{}`), nil
	}
	data := []byte(input)
	if strings.HasPrefix(input, "@") {
		if len(input) == 1 {
			return nil, errors.New("provide --input @<JSON-file>")
		}
		var err error
		data, err = readLimitedFile(resolvePath(a.Dir, strings.TrimPrefix(input, "@")), hex.MaxActionInputBytes)
		if err != nil {
			return nil, fmt.Errorf("read input: %w", err)
		}
	}
	if !json.Valid(data) {
		return nil, errors.New("input must contain exactly one JSON value")
	}
	return data, nil
}

func findEndpoint(integrations []hex.SiteIntegration, integrationName, endpointName string) (hex.IntegrationEndpointInfo, error) {
	for _, integration := range integrations {
		if integration.Name != integrationName {
			continue
		}
		for _, endpoint := range integration.Endpoints {
			if endpoint.Name == endpointName {
				return endpoint, nil
			}
		}
		return hex.IntegrationEndpointInfo{}, fmt.Errorf("%s has no endpoint %s; see hex integrations list", integrationName, endpointName)
	}
	return hex.IntegrationEndpointInfo{}, fmt.Errorf("the platform has no integration %s; see hex integrations list", integrationName)
}

// connectionHint explains how to connect an account when an endpoint
// answers that the caller has not connected it.
func connectionHint(err error) error {
	var status *apiStatusError
	if !errors.As(err, &status) || status.Status != http.StatusConflict {
		return err
	}
	var body struct {
		Connect struct {
			Connector string `json:"connector"`
			URL       string `json:"url"`
		} `json:"connect"`
	}
	if json.Unmarshal(status.Body, &body) != nil || body.Connect.Connector == "" {
		return err
	}
	return fmt.Errorf("%s\nRun hex connections connect %s, or open %s in your browser", status.Message, body.Connect.Connector, body.Connect.URL)
}

func (a *App) integrationApprovalsCommand(options *siteCommandOptions) *cobra.Command {
	var status string
	command := &cobra.Command{
		Use: "approvals", Short: "List integration approval requests (platform admins)", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			project, err := a.commandConfig(options.platform, false)
			if err != nil {
				return err
			}
			path := "/api/hex/integration-approvals"
			if status != "" {
				path += "?" + url.Values{"status": {status}}.Encode()
			}
			data, err := a.apiRequest(cmd.Context(), project, path)
			if err != nil {
				return err
			}
			return a.printJSON(data)
		},
	}
	command.Flags().StringVar(&status, "status", "", "Only requested or approved entries")
	return command
}

// integrationApprovalCommands are request (site owners), approve and revoke
// (platform admins; owners may also withdraw a pending request).
func (a *App) integrationApprovalCommands(options *siteCommandOptions) []*cobra.Command {
	type approvalCommand struct {
		name, short, method string
	}
	definitions := []approvalCommand{
		{"request", "Ask a platform admin to approve an integration for your site", http.MethodPost},
		{"approve", "Approve an integration for a site (platform admins)", http.MethodPut},
		{"revoke", "Revoke an approval, or withdraw a pending request", http.MethodDelete},
	}
	commands := make([]*cobra.Command, 0, len(definitions))
	for _, definition := range definitions {
		var reason string
		command := &cobra.Command{
			Use: definition.name + " <integration>", Short: definition.short, Args: cobra.ExactArgs(1),
			RunE: func(cmd *cobra.Command, args []string) error {
				if !integrationNamePattern.MatchString(args[0]) {
					return errors.New("invalid integration name")
				}
				project, err := options.config(a)
				if err != nil {
					return err
				}
				var body any
				if definition.method == http.MethodPost {
					body = map[string]string{"reason": reason}
				}
				path := "/api/hex/sites/" + options.site + "/integrations/" + args[0] + "/approval"
				data, err := a.apiCall(cmd.Context(), project, definition.method, path, body)
				if err != nil {
					return err
				}
				if data == nil {
					fmt.Fprintf(a.Out, "Removed the %s approval for %s\n", args[0], options.site)
					return nil
				}
				return a.printJSON(data)
			},
		}
		if definition.method == http.MethodPost {
			command.Flags().StringVar(&reason, "reason", "", "Why the site needs the integration")
		}
		commands = append(commands, command)
	}
	return commands
}

type connectionState struct {
	Name       string `json:"name"`
	Title      string `json:"title"`
	Connected  bool   `json:"connected"`
	Account    string `json:"account,omitempty"`
	ConnectURL string `json:"connectURL"`
}

const (
	connectionWait     = 3 * time.Minute
	connectionPollTime = 2 * time.Second
)

func (a *App) connectionsCommand() *cobra.Command {
	var profile string
	command := &cobra.Command{Use: "connections", Short: "Connect your accounts in other systems for integrations"}
	command.PersistentFlags().StringVar(&profile, "platform", "", "Saved platform profile")

	list := &cobra.Command{
		Use: "list", Short: "List connectable accounts and whether you connected them", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			project, err := a.commandConfig(profile, false)
			if err != nil {
				return err
			}
			data, err := a.apiRequest(cmd.Context(), project, "/api/hex/connections")
			if err != nil {
				return connectionsUnavailable(err)
			}
			return a.printJSON(data)
		},
	}

	var noWait bool
	connect := &cobra.Command{
		Use: "connect <connector>", Short: "Connect an account in your browser", Args: cobra.ExactArgs(1),
		Long: "Open the platform's connection page in your browser. The browser signs in to the " +
			"platform as you, so the account is connected to the same identity the CLI uses.",
		RunE: func(cmd *cobra.Command, args []string) error {
			project, err := a.commandConfig(profile, false)
			if err != nil {
				return err
			}
			state, err := a.connection(cmd.Context(), project, args[0])
			if err != nil {
				return err
			}
			fmt.Fprintf(a.Out, "Connect %s in your browser:\n%s\n", state.Title, state.ConnectURL)
			if err := a.OpenBrowser(state.ConnectURL); err != nil {
				fmt.Fprintln(a.Err, "Could not open a browser; open the address above yourself.")
			}
			if noWait {
				return nil
			}
			return a.waitForConnection(cmd.Context(), project, args[0])
		},
	}
	connect.Flags().BoolVar(&noWait, "no-wait", false, "Return without waiting for the connection")

	disconnect := &cobra.Command{
		Use: "disconnect <connector>", Short: "Remove a connected account", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if !integrationNamePattern.MatchString(args[0]) {
				return errors.New("invalid connector name")
			}
			project, err := a.commandConfig(profile, false)
			if err != nil {
				return err
			}
			if _, err := a.apiCall(cmd.Context(), project, http.MethodDelete, "/api/hex/connections/"+args[0], nil); err != nil {
				return connectionsUnavailable(err)
			}
			fmt.Fprintln(a.Out, "Disconnected", args[0])
			return nil
		},
	}
	command.AddCommand(list, connect, disconnect)
	return command
}

func connectionsUnavailable(err error) error {
	var status *apiStatusError
	if errors.As(err, &status) && status.Status == http.StatusNotFound && status.Message == "" {
		return errors.New("this platform has no connectable accounts")
	}
	return err
}

func (a *App) connection(ctx context.Context, project Project, name string) (connectionState, error) {
	data, err := a.apiRequest(ctx, project, "/api/hex/connections")
	if err != nil {
		return connectionState{}, connectionsUnavailable(err)
	}
	var states []connectionState
	if err := json.Unmarshal(data, &states); err != nil {
		return connectionState{}, fmt.Errorf("unexpected connection list: %w", err)
	}
	for _, state := range states {
		if state.Name == name {
			return state, nil
		}
	}
	return connectionState{}, fmt.Errorf("the platform has no connector %s; see hex connections list", name)
}

func (a *App) waitForConnection(ctx context.Context, project Project, name string) error {
	fmt.Fprintln(a.Err, "Waiting for the connection to complete…")
	deadline := time.Now().Add(connectionWait)
	for time.Now().Before(deadline) {
		state, err := a.connection(ctx, project, name)
		if err != nil {
			return err
		}
		if state.Connected {
			account := ""
			if state.Account != "" {
				account = " as " + state.Account
			}
			fmt.Fprintf(a.Out, "Connected %s%s\n", state.Title, account)
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(connectionPollTime):
		}
	}
	return errors.New("the connection was not completed in time; run hex connections list to check")
}

package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strings"

	"github.com/spf13/cobra"
)

func (a *App) apiRequest(ctx context.Context, project Project, path string) (json.RawMessage, error) {
	server, err := origin(project.Server, false)
	if err != nil {
		return nil, err
	}
	token := os.Getenv("HEX_TOKEN")
	if token == "" && project.Resource != "" {
		command := exec.CommandContext(ctx, "az", "account", "get-access-token", "--resource", project.Resource, "--query", "accessToken", "-o", "tsv")
		command.Stderr = a.Err
		output, err := command.Output()
		if err != nil {
			return nil, fmt.Errorf("obtain Azure API token; check az login and resource permissions: %w", err)
		}
		token = strings.TrimSpace(string(output))
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, server.String()+path, nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("X-Hex-Request", "1")
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	response, err := a.HTTP.Do(request)
	if err != nil {
		return nil, err
	}
	defer closeLogged(a.Err, response.Body)
	if requiresBrowser(response.StatusCode) {
		return nil, fmt.Errorf("gateway access requires authentication or permission (HTTP %d); use the browser or HEX_TOKEN. hex login authenticates storage only", response.StatusCode)
	}
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("Hex API returned HTTP %d", response.StatusCode)
	}
	if !strings.Contains(response.Header.Get("Content-Type"), "application/json") {
		return nil, errors.New("expected a JSON response from Hex")
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, 16<<20))
	if err != nil {
		return nil, err
	}
	if !json.Valid(data) {
		return nil, errors.New("invalid JSON response from Hex")
	}
	return data, nil
}

func (a *App) sitesCommand() *cobra.Command {
	return a.readCommand("sites", "List directories discovered by the platform", "/api/sites", false)
}

func (a *App) capabilitiesCommand() *cobra.Command {
	return a.readCommand("capabilities", "Show cached or live platform capabilities", "/api/hex/capabilities", true)
}

func (a *App) readCommand(name, description, path string, cached bool) *cobra.Command {
	var profile, server, resource string
	var refresh bool
	command := &cobra.Command{
		Use:   name,
		Short: description,
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			project, err := a.commandConfig(profile, false)
			if err != nil {
				return err
			}
			if cached && project.Capabilities != nil && !refresh {
				return a.printJSON(project.Capabilities)
			}
			if server != "" {
				project.Server = server
			}
			if resource != "" {
				project.Resource = resource
			}
			data, err := a.apiRequest(cmd.Context(), project, path)
			if err != nil {
				return err
			}
			return a.printJSON(data)
		},
	}
	command.Flags().StringVar(&profile, "platform", "", "Saved platform profile")
	command.Flags().StringVar(&server, "server", "", "Override the API origin")
	command.Flags().StringVar(&resource, "resource", "", "Optional Azure CLI API resource")
	if cached {
		command.Flags().BoolVar(&refresh, "refresh", false, "Request current capabilities from the API")
	}
	return command
}

func (a *App) loginCommand() *cobra.Command {
	var profile string
	command := &cobra.Command{
		Use:   "login",
		Short: "Delegate publishing login to the storage provider",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			project, err := a.commandConfig(profile, false)
			if err != nil {
				return err
			}
			if project.Publishing == nil {
				return errors.New("no publishing provider configured")
			}
			if err := validatePublishing(*project.Publishing); err != nil {
				return err
			}
			if project.Publishing.Provider == "filesystem" {
				fmt.Fprintln(a.Out, "Filesystem publishing does not require a storage login.")
				return nil
			}
			return a.azureLogin(cmd.Context())
		},
	}
	command.Flags().StringVar(&profile, "platform", "", "Saved platform profile")
	return command
}

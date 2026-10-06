package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"
)

const maxAPIResponseBytes = 16 << 20

var errAPIResponseTooLarge = errors.New("JSON response from Hex exceeds 16 MiB")

// apiStatusError is an unexpected API response status.
type apiStatusError struct {
	Status  int
	Message string
	Body    json.RawMessage
}

func (e *apiStatusError) Error() string {
	if e.Message != "" {
		return fmt.Sprintf("Hex API returned HTTP %d: %s", e.Status, e.Message)
	}
	return fmt.Sprintf("Hex API returned HTTP %d", e.Status)
}

// tokenCache keeps one API token per resource for the life of the command,
// so multi-request commands such as publish ask Azure CLI only once.
type tokenCache struct {
	mu     sync.Mutex
	tokens map[string]string
}

func (a *App) apiRequest(ctx context.Context, project Project, path string) (json.RawMessage, error) {
	return a.apiCall(ctx, project, http.MethodGet, path, nil)
}

func (a *App) apiCall(ctx context.Context, project Project, method, path string, body any) (json.RawMessage, error) {
	var payload io.Reader
	var size int64
	contentType := ""
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		payload = bytes.NewReader(data)
		size = int64(len(data))
		contentType = "application/json"
	}
	return a.apiSend(ctx, project, method, path, payload, size, contentType)
}

func (a *App) apiSend(ctx context.Context, project Project, method, path string, payload io.Reader, size int64, contentType string) (json.RawMessage, error) {
	// Uploads can take longer than the default request timeout.
	long := payload != nil && contentType != "application/json"
	return a.apiSendWith(ctx, project, long, method, path, payload, size, contentType)
}

// apiCallWithoutTimeout is apiCall for requests that may take minutes,
// such as model answers; the context still bounds them.
func (a *App) apiCallWithoutTimeout(ctx context.Context, project Project, method, path string, body any) (json.RawMessage, error) {
	data, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	return a.apiSendWith(ctx, project, true, method, path, bytes.NewReader(data), int64(len(data)), "application/json")
}

func (a *App) apiSendWith(ctx context.Context, project Project, withoutTimeout bool, method, path string, payload io.Reader, size int64, contentType string) (json.RawMessage, error) {
	request, err := a.newAPIRequest(ctx, project, method, path, payload, size, contentType)
	if err != nil {
		return nil, err
	}

	client := a.HTTP
	if withoutTimeout {
		streaming := *a.HTTP
		streaming.Timeout = 0
		client = &streaming
	}
	response, err := client.Do(request)
	if err != nil {
		return nil, err
	}
	defer closeLogged(a.Err, response.Body)

	if err := gatewayError(response); err != nil {
		return nil, err
	}
	if response.StatusCode == http.StatusNoContent {
		return nil, nil
	}
	accepted := response.StatusCode == http.StatusOK || response.StatusCode == http.StatusCreated || response.StatusCode == http.StatusAccepted
	if !accepted {
		return nil, apiResponseError(response)
	}
	if !strings.Contains(response.Header.Get("Content-Type"), "application/json") {
		return nil, errors.New("expected a JSON response from Hex")
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, maxAPIResponseBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxAPIResponseBytes {
		return nil, errAPIResponseTooLarge
	}
	if !json.Valid(data) {
		return nil, errors.New("invalid JSON response from Hex")
	}
	return data, nil
}

// newAPIRequest builds an authenticated request for the platform API.
func (a *App) newAPIRequest(ctx context.Context, project Project, method, path string, payload io.Reader, size int64, contentType string) (*http.Request, error) {
	server, err := origin(project.Server, true)
	if err != nil {
		return nil, err
	}
	token, err := a.apiToken(ctx, project)
	if err != nil {
		return nil, err
	}

	request, err := http.NewRequestWithContext(ctx, method, server.String()+path, payload)
	if err != nil {
		return nil, err
	}
	if payload != nil {
		request.ContentLength = size
		request.Header.Set("Content-Type", contentType)
	}
	request.Header.Set("X-Hex-Request", "1")
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	return request, nil
}

// apiToken returns HEX_TOKEN or uses the platform's configured sign-in flow.
// Profiles without Auth retain the legacy Microsoft/Azure CLI behavior.
func (a *App) apiToken(ctx context.Context, project Project) (string, error) {
	if token := os.Getenv("HEX_TOKEN"); token != "" {
		return token, nil
	}
	if project.Auth != nil {
		if project.Auth.Type == "none" {
			return "", nil
		}
		return a.oidcToken(ctx, project, false)
	}
	if project.Resource == "" {
		return "", nil
	}

	a.tokens.mu.Lock()
	defer a.tokens.mu.Unlock()
	if token, ok := a.tokens.tokens[project.Resource]; ok {
		return token, nil
	}

	var token string
	var err error
	if project.ClientID != "" {
		token, err = a.signedInToken(ctx, project)
	} else {
		token, err = a.azureCLIToken(ctx, project.Resource)
	}
	if err != nil {
		return "", err
	}

	if a.tokens.tokens == nil {
		a.tokens.tokens = make(map[string]string)
	}
	a.tokens.tokens[project.Resource] = token
	return token, nil
}

func (a *App) azureCLIToken(ctx context.Context, resource string) (string, error) {
	token, err := a.azureAccessToken(ctx, resource)
	if err != nil && a.Interactive {
		fmt.Fprintln(a.Err, "Signing in to the platform with Azure CLI...")
		if loginError := a.azureLogin(ctx, resource); loginError != nil {
			return "", loginError
		}
		token, err = a.azureAccessToken(ctx, resource)
	}
	if err != nil {
		return "", fmt.Errorf("obtain a platform token; run hex login first: %w", err)
	}
	return token, nil
}

func (a *App) azureAccessToken(ctx context.Context, resource string) (string, error) {
	if err := validateResource(resource); err != nil {
		return "", err
	}
	args, err := azureTenantArguments([]string{"account", "get-access-token", "--resource", resource, "--query", "accessToken", "--output", "tsv"})
	if err != nil {
		return "", err
	}
	command, err := azureCLICommand(ctx, args...)
	if err != nil {
		return "", err
	}
	command.Dir = a.Dir
	command.Stderr = io.Discard
	output, err := command.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(output)), nil
}

// gatewayError reports responses that need a browser sign-in. A JSON error
// from Hex itself, such as a missing permission, is returned as such, so
// the caller sees why the platform refused.
func gatewayError(response *http.Response) error {
	if !requiresBrowser(response.StatusCode) {
		return nil
	}
	if response.StatusCode == http.StatusForbidden || response.StatusCode == http.StatusUnauthorized {
		var status *apiStatusError
		if errors.As(apiResponseError(response), &status) && status.Message != "" {
			return status
		}
	}
	return fmt.Errorf("gateway access requires authentication or permission (HTTP %d); run hex login, or set HEX_TOKEN", response.StatusCode)
}

// apiResponseError reads a failed response's JSON error, keeping the body
// for commands that act on more than the message.
func apiResponseError(response *http.Response) error {
	statusError := &apiStatusError{Status: response.StatusCode}
	if !strings.Contains(response.Header.Get("Content-Type"), "application/json") {
		return statusError
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, 8<<10))
	if err != nil {
		return statusError
	}
	var body struct {
		Error string `json:"error"`
	}
	if json.Unmarshal(data, &body) == nil {
		statusError.Message = body.Error
		statusError.Body = data
	}
	return statusError
}

func (a *App) sitesCommand() *cobra.Command {
	var profile, server string
	var mine, jsonOutput bool
	command := &cobra.Command{
		Use:   "sites",
		Short: "List the platform's sites, or yours with --mine",
		Long: "List the sites listed in the platform's catalogue. --mine lists every site you " +
			"own instead, including files and folders you published at private links.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			project, err := a.commandConfig(profile, false)
			if err != nil {
				return err
			}
			if server != "" {
				project.Server = server
			}
			if !mine {
				data, err := a.apiRequest(cmd.Context(), project, "/api/sites")
				if err != nil {
					return err
				}
				return a.printJSON(data)
			}

			sites, err := a.mySites(cmd.Context(), project)
			if err != nil {
				return err
			}
			if jsonOutput {
				return a.printJSON(sites)
			}
			return printSites(a.Out, sites)
		},
	}
	command.Flags().BoolVar(&mine, "mine", false, "List the sites you own")
	command.Flags().BoolVar(&jsonOutput, "json", false, "Print JSON with --mine")
	command.Flags().StringVar(&profile, "platform", "", "Saved platform profile")
	command.Flags().StringVar(&server, "server", "", "Override the API origin")
	return command
}

type ownedSite struct {
	Name        string    `json:"name"`
	URL         string    `json:"url"`
	Title       string    `json:"title"`
	Kind        string    `json:"kind"`
	Audience    string    `json:"audience"`
	PublishedBy string    `json:"publishedBy,omitempty"`
	PublishedAt time.Time `json:"publishedAt,omitzero"`
}

func (a *App) mySites(ctx context.Context, project Project) ([]ownedSite, error) {
	data, err := a.apiRequest(ctx, project, "/api/hex/my-sites")
	if err != nil {
		var status *apiStatusError
		if errors.As(err, &status) && status.Status == http.StatusNotFound {
			return nil, errors.New("this platform cannot list your sites; it needs sign-in and access control")
		}
		return nil, err
	}
	var sites []ownedSite
	if err := json.Unmarshal(data, &sites); err != nil {
		return nil, errors.New("unexpected site list from the platform")
	}
	return sites, nil
}

func printSites(output io.Writer, sites []ownedSite) error {
	if len(sites) == 0 {
		_, err := fmt.Fprintln(output, "You don't own any sites yet. Publish a project, file or folder with hex publish.")
		return err
	}
	table := tabwriter.NewWriter(output, 0, 4, 2, ' ', 0)
	fmt.Fprintln(table, "NAME\tTITLE\tKIND\tWHO CAN OPEN IT\tURL")
	for _, site := range sites {
		fmt.Fprintf(table, "%s\t%s\t%s\t%s\t%s\n", site.Name, site.Title, site.Kind, site.Audience, site.URL)
	}
	return table.Flush()
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
			if cached && project.Capabilities != nil && !refresh && !cmd.Flags().Changed("server") && resource == "" {
				return a.printJSON(project.Capabilities)
			}
			if server != "" {
				project.Server = server
			}
			if resource != "" {
				project, err = project.withResource(resource)
				if err != nil {
					return err
				}
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
		Short: "Sign in to the platform in your browser",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			project, err := a.commandConfig(profile, false)
			if err != nil {
				return err
			}
			switch {
			case project.Auth != nil && project.Auth.Type == "oidc":
				_, err := a.oidcToken(cmd.Context(), project, true)
				return err
			case project.Resource == "":
				fmt.Fprintln(a.Out, "This platform does not require a sign-in for the CLI.")
				return nil
			case project.ClientID != "":
				return a.signIn(cmd.Context(), project)
			default:
				return a.azureLogin(cmd.Context(), project.Resource)
			}
		},
	}
	command.Flags().StringVar(&profile, "platform", "", "Saved platform profile")
	return command
}

func (a *App) logoutCommand() *cobra.Command {
	var profile string
	command := &cobra.Command{
		Use:   "logout",
		Short: "Forget the saved platform sign-in",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			project, err := a.commandConfig(profile, false)
			if err != nil {
				return err
			}
			if project.Auth != nil {
				if project.Auth.Type == "oidc" {
					return a.oidcLogout(project)
				}
				fmt.Fprintln(a.Out, "This platform has no saved CLI sign-in.")
				return nil
			}
			if project.ClientID == "" {
				fmt.Fprintln(a.Out, "This platform has no saved Hex sign-in; Azure CLI sessions are managed with az logout.")
				return nil
			}
			return a.signOut(cmd.Context(), project)
		},
	}
	command.Flags().StringVar(&profile, "platform", "", "Saved platform profile")
	return command
}

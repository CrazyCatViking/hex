package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
)

func (a *App) fetchCommand() *cobra.Command {
	var profile, site, path, output string
	command := &cobra.Command{
		Use:   "fetch [url]",
		Short: "Read a protected platform page, asset or API response",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 1 && (site != "" || cmd.Flags().Changed("path")) {
				return errors.New("use a URL or --site/--path, not both")
			}
			if len(args) == 0 && site == "" {
				return errors.New("provide a URL or --site")
			}
			project, err := a.commandConfig(profile, false)
			if err != nil {
				return err
			}
			address := ""
			if len(args) == 1 {
				address = args[0]
			} else {
				base := project.SiteBaseURL
				if base == "" {
					base = project.Server
				}
				address, err = siteURL(base, site)
				if err != nil {
					return err
				}
				relative, err := url.Parse(path)
				if err != nil || !strings.HasPrefix(path, "/") || strings.HasPrefix(path, "//") || relative.IsAbs() || relative.Host != "" || relative.Fragment != "" {
					return errors.New("--path must be an absolute URL path, optionally with a query")
				}
				baseURL, err := url.Parse(address)
				if err != nil {
					return err
				}
				address = baseURL.ResolveReference(relative).String()
			}
			if err := validateFetchURL(project, address); err != nil {
				return err
			}
			return a.downloadResponse(cmd.Context(), project, address, output)
		},
	}
	command.Flags().StringVar(&profile, "platform", "", "Saved platform profile")
	command.Flags().StringVar(&site, "site", "", "Site name")
	command.Flags().StringVar(&path, "path", "/", "URL path and optional query")
	command.Flags().StringVar(&output, "output", "", "Save the response to a file instead of stdout")
	return command
}

// validateFetchURL only permits the configured origins and direct site
// subdomains. A URL must be checked before acquiring or attaching credentials.
func validateFetchURL(project Project, address string) error {
	parsed, err := url.Parse(address)
	if err != nil || parsed.User != nil || parsed.Fragment != "" || parsed.Opaque != "" {
		return errors.New("use an HTTP(S) URL without credentials or a fragment")
	}
	target, err := origin(parsed.Scheme+"://"+parsed.Host, true)
	if err != nil {
		return err
	}
	server, err := origin(project.Server, true)
	if err != nil {
		return err
	}
	if target.String() == server.String() {
		return nil
	}
	base := project.SiteBaseURL
	if base == "" {
		base = project.Server
	}
	parent, err := origin(base, true)
	if err != nil {
		return err
	}
	if target.Scheme == parent.Scheme && target.Port() == parent.Port() {
		if target.Host == parent.Host {
			return nil
		}
		suffix := "." + parent.Hostname()
		if strings.HasSuffix(target.Hostname(), suffix) {
			name := strings.TrimSuffix(target.Hostname(), suffix)
			if validateSiteName(name) == nil {
				return nil
			}
		}
	}
	return errors.New("URL is outside the selected platform; select its profile with --platform")
}

func (a *App) rawGet(ctx context.Context, project Project, address string) (*http.Response, error) {
	token, err := a.apiToken(ctx, project)
	if err != nil {
		return nil, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("X-Hex-Request", "1")
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	client := *a.HTTP
	// Browsers resolve *.localhost themselves; Go's system resolver may not.
	// Keep the site Host/TLS name while connecting directly to loopback.
	if strings.HasSuffix(strings.ToLower(request.URL.Hostname()), ".localhost") {
		transport := http.DefaultTransport.(*http.Transport)
		if configured, ok := client.Transport.(*http.Transport); ok {
			transport = configured
		} else if client.Transport != nil {
			transport = nil
		}
		if transport != nil {
			local := transport.Clone()
			local.Proxy = nil
			local.DisableKeepAlives = true
			dial := local.DialContext
			if dial == nil {
				dial = (&net.Dialer{}).DialContext
			}
			local.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
				_, port, err := net.SplitHostPort(address)
				if err != nil {
					return nil, err
				}
				return dial(ctx, network, net.JoinHostPort("127.0.0.1", port))
			}
			client.Transport = local
		}
	}
	client.CheckRedirect = func(next *http.Request, previous []*http.Request) error {
		if len(previous) >= 10 {
			return errors.New("too many redirects")
		}
		first := previous[0].URL
		if next.URL.Scheme != first.Scheme || !strings.EqualFold(next.URL.Host, first.Host) || next.URL.User != nil {
			return errors.New("redirect leaves the authenticated origin; run hex login if sign-in is required")
		}
		return nil
	}
	response, err := client.Do(request)
	if err != nil {
		return nil, err
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		defer closeLogged(a.Err, response.Body)
		if response.StatusCode == http.StatusUnauthorized {
			return nil, errors.New("authentication required; run hex login in a terminal")
		}
		return nil, &apiStatusError{Status: response.StatusCode, Message: apiErrorMessage(response)}
	}
	return response, nil
}

func (a *App) downloadResponse(ctx context.Context, project Project, address, output string) error {
	response, err := a.rawGet(ctx, project, address)
	if err != nil {
		return err
	}
	defer closeLogged(a.Err, response.Body)
	if output == "" {
		_, err := io.Copy(a.Out, response.Body)
		return err
	}
	return saveResponse(resolvePath(a.Dir, output), response.Body)
}

// Stream to a sibling temporary file so failed downloads preserve existing output.
func saveResponse(path string, body io.Reader) (result error) {
	file, err := os.CreateTemp(filepath.Dir(path), ".hex-download-*")
	if err != nil {
		return fmt.Errorf("create output: %w", err)
	}
	defer func() {
		if err := os.Remove(file.Name()); err != nil && !errors.Is(err, os.ErrNotExist) {
			result = errors.Join(result, err)
		}
	}()
	_, copyError := io.Copy(file, body)
	if err := errors.Join(copyError, file.Close()); err != nil {
		return err
	}
	return os.Rename(file.Name(), path)
}

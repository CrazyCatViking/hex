package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"unicode"

	"github.com/spf13/cobra"
)

type setupResult struct {
	Status      string  `json:"status"`
	Profile     string  `json:"profile,omitempty"`
	Name        string  `json:"name,omitempty"`
	Server      string  `json:"server,omitempty"`
	Publishing  *string `json:"publishing,omitempty"`
	DownloadURL string  `json:"downloadURL,omitempty"`
	Reason      string  `json:"reason,omitempty"`
	Message     string  `json:"message,omitempty"`
	NextCommand string  `json:"nextCommand,omitempty"`
}

func (a *App) setupCommand() *cobra.Command {
	var file, server, name string
	var jsonOutput bool
	command := &cobra.Command{
		Use:   "setup [platform-url]",
		Short: "Download or import non-secret platform settings",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) > 0 {
				if server != "" {
					return errors.New("supply one platform URL")
				}
				server = args[0]
			}
			result, err := a.setup(cmd.Context(), server, file, name, a.Interactive && !jsonOutput)
			if err != nil {
				if !jsonOutput {
					return err
				}
				if writeError := a.printJSON(setupResult{Status: "error", Message: err.Error()}); writeError != nil {
					return writeError
				}
				return &ExitError{Code: 1}
			}
			if jsonOutput {
				if err := a.printJSON(result); err != nil {
					return err
				}
			} else if result.Status == "ready" {
				fmt.Fprintf(a.Out, "Connected to %s. Saved default profile: %s\nNext: %s\n", result.Name, result.Profile, result.NextCommand)
			} else {
				fmt.Fprintln(a.Out, result.Message)
				if result.DownloadURL != "" {
					fmt.Fprintln(a.Out, "Download:", result.DownloadURL)
				}
				if result.NextCommand != "" {
					fmt.Fprintln(a.Out, "Then:", result.NextCommand)
				}
			}
			if result.Status != "ready" {
				return &ExitError{Code: 2}
			}
			return nil
		},
	}
	command.Flags().StringVar(&file, "file", "", "Import a downloaded JSON connection file")
	command.Flags().StringVar(&server, "server", "", "Expected platform origin")
	command.Flags().StringVar(&name, "name", "", "Save as this profile name")
	command.Flags().BoolVar(&jsonOutput, "json", false, "Machine-readable output; never prompt or open a browser")
	return command
}

func (a *App) setup(ctx context.Context, server, file, name string, interactive bool) (setupResult, error) {
	var connection Connection
	var err error
	if file != "" {
		connection, err = a.readConnectionFile(file, server)
	} else {
		if server == "" && interactive {
			server, err = a.ask(ctx, "Company Hex URL: ")
			if err != nil {
				return setupResult{}, err
			}
		}
		if strings.TrimSpace(server) == "" {
			return setupResult{
				Status:  "input_required",
				Message: "Supply a platform URL or use --file with a downloaded connection file.",
			}, nil
		}
		parsed, parseError := origin(server, true)
		if parseError != nil {
			return setupResult{}, parseError
		}
		server = parsed.String()
		var browserRequired bool
		connection, browserRequired, err = a.downloadConnection(ctx, server)
		if err != nil {
			return setupResult{}, err
		}
		if browserRequired {
			downloadURL := server + connectionPath
			if !interactive {
				return setupResult{
					Status:      "download_required",
					DownloadURL: downloadURL,
					Reason:      "browser_authentication",
					Server:      server,
					Message:     "Open downloadURL in your browser, sign in, download the connection file, then import its path.",
					NextCommand: "hex setup --file <downloaded-file> --server <platform-url> --json",
				}, nil
			}
			fmt.Fprintln(a.Out, "Sign in through your hosting provider and download the connection file:", downloadURL)
			if err := a.OpenBrowser(downloadURL); err != nil {
				fmt.Fprintf(a.Out, "Could not open the browser: %v. Open the URL manually.\n", err)
			}
			for {
				path, err := a.ask(ctx, "Drop the downloaded JSON file here, or paste its path, then press Enter: ")
				if err != nil {
					return setupResult{}, err
				}
				connection, err = a.readConnectionFile(path, server)
				if err == nil {
					break
				}
				fmt.Fprintln(a.Out, "Could not import that file:", err)
			}
		}
	}
	if err != nil {
		return setupResult{}, err
	}
	profile, err := saveProfile(connection, name)
	if err != nil {
		return setupResult{}, err
	}
	result := setupResult{
		Status:      "ready",
		Profile:     profile,
		Name:        connection.Name,
		Server:      connection.Server,
		NextCommand: "hex init my-app",
	}
	if connection.Publishing != nil {
		result.Publishing = &connection.Publishing.Provider
	}
	return result, nil
}

func (a *App) downloadConnection(ctx context.Context, server string) (Connection, bool, error) {
	var connection Connection
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, server+connectionPath, nil)
	if err != nil {
		return connection, false, err
	}
	request.Header.Set("Accept", "application/json")
	response, err := a.HTTP.Do(request)
	if err != nil {
		return connection, false, fmt.Errorf("reach platform configuration; check the URL and network/VPN: %w", err)
	}
	defer closeLogged(a.Err, response.Body)
	if requiresBrowser(response.StatusCode) {
		return connection, true, nil
	}
	contentType, _, contentTypeError := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if response.StatusCode == http.StatusOK && contentTypeError == nil && contentType == "text/html" {
		return connection, true, nil
	}
	if response.StatusCode == http.StatusNotFound {
		return connection, false, errors.New("this platform does not expose connection settings at /api/hex/config; ask its operator to configure them")
	}
	if response.StatusCode != http.StatusOK {
		return connection, false, fmt.Errorf("platform configuration returned HTTP %d", response.StatusCode)
	}
	if contentTypeError != nil || (contentType != "application/json" && !strings.HasSuffix(contentType, "+json")) {
		return connection, false, errors.New("expected JSON connection settings, not the platform home page")
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, maxConfigBytes+1))
	if err != nil {
		return connection, false, err
	}
	connection, err = parseConnection(data, server)
	return connection, false, err
}

func requiresBrowser(status int) bool {
	switch status {
	case 301, 302, 303, 307, 308, 401, 403:
		return true
	default:
		return false
	}
}

func closeLogged(output io.Writer, closer io.Closer) {
	if err := closer.Close(); err != nil {
		fmt.Fprintln(output, "Close resource:", err)
	}
}

var escapedPathCharacter = regexp.MustCompile(`\\([\s'"()&;\\])`)

func droppedFilePath(input string) (string, error) {
	value := strings.TrimSpace(input)
	if strings.HasPrefix(value, "& ") {
		value = strings.TrimSpace(value[2:])
	}
	switch {
	case strings.HasPrefix(value, "$'") && strings.HasSuffix(value, "'"):
		value = strings.NewReplacer(`\'`, "'", `\\`, `\`).Replace(value[2 : len(value)-1])
	case strings.HasPrefix(value, "'") && strings.HasSuffix(value, "'"):
		value = strings.ReplaceAll(value[1:len(value)-1], `'\''`, "'")
	case strings.HasPrefix(value, `"`) && strings.HasSuffix(value, `"`):
		value = value[1 : len(value)-1]
	default:
		if !(len(value) > 1 && value[1] == ':') && !strings.HasPrefix(value, `\\`) {
			value = escapedPathCharacter.ReplaceAllString(value, "$1")
		}
	}
	if strings.HasPrefix(value, "file:") {
		parsed, err := url.Parse(value)
		if err != nil {
			return "", err
		}
		value = parsed.Path
		if parsed.Host != "" && parsed.Host != "localhost" {
			value = "//" + parsed.Host + value
		}
		if runtime.GOOS == "windows" && len(value) > 2 && value[0] == '/' && value[2] == ':' {
			value = value[1:]
		}
		value = filepath.FromSlash(value)
	}
	if strings.HasPrefix(value, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		value = filepath.Join(home, value[2:])
	}
	if value == "" || strings.ContainsFunc(value, unicode.IsControl) {
		return "", errors.New("drop or paste the path to one downloaded JSON file")
	}
	return value, nil
}

func (a *App) readConnectionFile(input, server string) (Connection, error) {
	path, err := droppedFilePath(input)
	if err != nil {
		return Connection{}, err
	}
	data, err := readLimitedFile(resolvePath(a.Dir, path), maxConfigBytes)
	if err != nil {
		return Connection{}, err
	}
	return parseConnection(data, server)
}

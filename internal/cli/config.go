package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"unicode"
)

const connectionPath = "/api/hex/config"
const maxConfigBytes = 64 * 1024

var siteNamePattern = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?$`)
var profileNamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]{0,127}$`)

type Publishing struct {
	Provider string `json:"provider"`
	Root     string `json:"root,omitempty"`
	URL      string `json:"url,omitempty"`
}

type Capabilities struct {
	Version        int   `json:"version"`
	Sites          bool  `json:"sites"`
	Files          bool  `json:"files"`
	Database       bool  `json:"database"`
	Realtime       bool  `json:"realtime"`
	MaxUploadBytes int64 `json:"maxUploadBytes"`
}

func (c *Capabilities) UnmarshalJSON(data []byte) error {
	type plain Capabilities
	var decoded plain
	if err := decodeStrict(data, &decoded); err != nil {
		return err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	for _, name := range []string{"sites", "files", "database", "realtime"} {
		if len(fields[name]) == 0 || bytes.Equal(fields[name], []byte("null")) {
			return fmt.Errorf("missing boolean capability %q", name)
		}
	}
	if decoded.Version != 1 || decoded.MaxUploadBytes < 1 {
		return errors.New("invalid capability description")
	}
	*c = Capabilities(decoded)
	return nil
}

type Connection struct {
	Version      int           `json:"version"`
	Name         string        `json:"name"`
	Server       string        `json:"server"`
	SiteBaseURL  string        `json:"siteBaseURL"`
	Publishing   *Publishing   `json:"publishing,omitempty"`
	Capabilities *Capabilities `json:"capabilities"`
}

type Project struct {
	Name         string        `json:"name"`
	Directory    string        `json:"directory"`
	Platform     string        `json:"platform,omitempty"`
	Server       string        `json:"server,omitempty"`
	SiteBaseURL  string        `json:"siteBaseURL,omitempty"`
	Publishing   *Publishing   `json:"publishing,omitempty"`
	Resource     string        `json:"resource,omitempty"`
	Capabilities *Capabilities `json:"-"`
}

func decodeStrict(data []byte, value any) error {
	decoder := json.NewDecoder(bytes.NewReader(bytes.TrimPrefix(data, []byte("\xef\xbb\xbf"))))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		return err
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		return errors.New("expected exactly one JSON document")
	}
	return nil
}

func origin(value string, requireTLS bool) (*url.URL, error) {
	parsed, err := url.Parse(strings.TrimSpace(value))
	if err != nil {
		return nil, errors.New("invalid platform URL")
	}
	validScheme := parsed.Scheme == "http" || parsed.Scheme == "https"
	hasExtraPath := parsed.RawQuery != "" || parsed.Fragment != "" || (parsed.Path != "" && parsed.Path != "/")
	if !validScheme || parsed.Host == "" || parsed.User != nil || hasExtraPath {
		return nil, errors.New("use an HTTP(S) origin without credentials, paths or query parameters")
	}
	if requireTLS && parsed.Scheme == "http" && !isLocalHost(parsed.Hostname()) {
		return nil, errors.New("use HTTPS for remote platforms; HTTP is allowed on loopback")
	}
	parsed.Host = strings.ToLower(parsed.Host)
	if (parsed.Scheme == "https" && parsed.Port() == "443") || (parsed.Scheme == "http" && parsed.Port() == "80") {
		parsed.Host = parsed.Hostname()
		if strings.Contains(parsed.Host, ":") {
			parsed.Host = "[" + parsed.Host + "]"
		}
	}
	parsed.Path = ""
	return parsed, nil
}

func isLocalHost(host string) bool {
	return host == "localhost" || strings.HasSuffix(host, ".localhost") || net.ParseIP(host).IsLoopback()
}

func validateSiteName(name string) error {
	if !siteNamePattern.MatchString(name) {
		return errors.New("site names must be DNS labels: 1–63 lowercase letters, digits or hyphens, starting and ending with a letter or digit")
	}
	return nil
}

func siteURL(base, name string) (string, error) {
	if err := validateSiteName(name); err != nil {
		return "", err
	}
	parsed, err := origin(base, false)
	if err != nil {
		return "", err
	}
	if net.ParseIP(parsed.Hostname()) != nil {
		return "", errors.New("siteBaseURL requires a DNS hostname, such as http://localhost:8080")
	}
	for _, label := range strings.Split(parsed.Hostname(), ".") {
		if err := validateSiteName(label); err != nil {
			return "", err
		}
	}
	parsed.Host = name + "." + parsed.Host
	parsed.Path = "/"
	return parsed.String(), nil
}

func parseConnection(data []byte, expectedServer string) (Connection, error) {
	var connection Connection
	if len(data) > maxConfigBytes {
		return connection, errors.New("connection settings exceed 64 KiB")
	}
	if err := decodeStrict(data, &connection); err != nil {
		return connection, fmt.Errorf("invalid connection document: %w", err)
	}
	if connection.Version != 1 || strings.TrimSpace(connection.Name) == "" || strings.ContainsFunc(connection.Name, unicode.IsControl) || connection.Capabilities == nil {
		return connection, errors.New("connection file requires version 1, a printable name and capabilities")
	}
	server, err := origin(connection.Server, true)
	if err != nil {
		return connection, err
	}
	if expectedServer != "" {
		expected, err := origin(expectedServer, true)
		if err != nil {
			return connection, err
		}
		sameLocal := isLocalHost(expected.Hostname()) && isLocalHost(server.Hostname()) && expected.Port() == server.Port() && expected.Scheme == server.Scheme
		if expected.String() != server.String() && !sameLocal {
			return connection, errors.New("connection file belongs to a different platform URL")
		}
	}
	base, err := origin(connection.SiteBaseURL, true)
	if err != nil {
		return connection, err
	}
	if _, err := siteURL(base.String(), "check"); err != nil {
		return connection, err
	}
	if connection.Publishing != nil {
		if err := validatePublishing(*connection.Publishing); err != nil {
			return connection, err
		}
		if connection.Publishing.Provider == "filesystem" && (!isLocalHost(server.Hostname()) || !absoluteAnyOS(connection.Publishing.Root)) {
			return connection, errors.New("filesystem profiles require a local platform and an absolute publishing root")
		}
	}
	connection.Server = server.String()
	connection.SiteBaseURL = base.String()
	connection.Name = strings.TrimSpace(connection.Name)
	return connection, nil
}

func absoluteAnyOS(path string) bool {
	return filepath.IsAbs(path) || (len(path) > 2 && path[1] == ':' && (path[2] == '\\' || path[2] == '/')) || strings.HasPrefix(path, `\\`)
}

func profileDirectory() (string, error) {
	if directory := os.Getenv("HEX_CONFIG_DIR"); directory != "" {
		return directory, nil
	}
	base := os.Getenv("XDG_CONFIG_HOME")
	if base == "" && runtime.GOOS == "windows" {
		base = os.Getenv("LOCALAPPDATA")
	}
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		base = filepath.Join(home, ".config")
	}
	return filepath.Join(base, "hex"), nil
}

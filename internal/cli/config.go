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

	hex "github.com/crazycatviking/hex/server"
)

const connectionPath = "/api/hex/config"
const maxConfigBytes = 64 * 1024

var siteNamePattern = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?$`)

var apiResourcePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9:/._-]{0,255}$`)
var clientIDPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
var profileNamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]{0,127}$`)

type Capabilities struct {
	Version        int   `json:"version"`
	Sites          bool  `json:"sites"`
	Files          bool  `json:"files"`
	Database       bool  `json:"database"`
	Realtime       bool  `json:"realtime"`
	Identity       bool  `json:"identity,omitempty"`
	AccessControl  bool  `json:"accessControl,omitempty"`
	Publishing     bool  `json:"publishing,omitempty"`
	Artifacts      bool  `json:"artifacts,omitempty"`
	Actions        bool  `json:"actions,omitempty"`
	MaxUploadBytes int64 `json:"maxUploadBytes"`
}

// UnmarshalJSON requires the original capabilities and tolerates new ones,
// so platform upgrades do not break installed CLIs.
func (c *Capabilities) UnmarshalJSON(data []byte) error {
	type plain Capabilities
	var decoded plain
	if err := json.Unmarshal(data, &decoded); err != nil {
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
	Version       int             `json:"version"`
	Name          string          `json:"name"`
	Server        string          `json:"server"`
	SiteBaseURL   string          `json:"siteBaseURL"`
	CLIReleaseURL string          `json:"cliReleaseURL,omitempty"`
	Resource      string          `json:"resource,omitempty"`
	ClientID      string          `json:"clientId,omitempty"`
	TenantID      string          `json:"tenantId,omitempty"`
	Auth          *hex.AuthConfig `json:"auth,omitempty"`
	Capabilities  *Capabilities   `json:"capabilities"`
	// LegacyPublishing is the storage destination older platforms advertised
	// and older CLIs saved in profiles. Publishing now goes through the API,
	// so it is accepted and ignored.
	LegacyPublishing json.RawMessage `json:"publishing,omitempty"`
}

type Project struct {
	Name         string `json:"name"`
	Title        string `json:"title,omitempty"`
	Description  string `json:"description,omitempty"`
	Author       string `json:"author,omitempty"`
	Discoverable *bool  `json:"discoverable,omitempty"`
	Directory    string `json:"directory,omitempty"`
	Platform     string `json:"platform,omitempty"`
	Server       string `json:"server,omitempty"`
	SiteBaseURL  string `json:"siteBaseURL,omitempty"`
	Resource     string `json:"resource,omitempty"`
	// Access is the site's access policy, applied by the platform when the
	// site is published. The platform validates it.
	Access json.RawMessage `json:"access,omitempty"`
	// Actions are operations the app exposes to the CLI and agents, performed
	// by the platform on the app's documents. The platform validates them.
	Actions json.RawMessage `json:"actions,omitempty"`
	// Automations are scheduled or on-demand step lists the platform runs
	// as the site; more can live one per file in automations/*.json.
	Automations json.RawMessage `json:"automations,omitempty"`
	// automations is what publishing sends: nil when the project defines
	// none, which leaves automations deployed through the API unchanged.
	automations *[]hex.Automation
	// ClientID and TenantID come from the platform profile and select the
	// CLI's own browser sign-in.
	ClientID     string          `json:"-"`
	TenantID     string          `json:"-"`
	Auth         *hex.AuthConfig `json:"-"`
	Capabilities *Capabilities   `json:"-"`
}

// withResource overrides the API resource. The profile's sign-in app only
// applies to the platform's own resource, so other resources use Azure CLI.
func (p Project) withResource(resource string) Project {
	if resource != p.Resource && !authMatchesResource(p.Auth, resource) {
		p.ClientID = ""
		p.TenantID = ""
		p.Auth = nil
	}
	p.Resource = resource
	return p
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
	if connection.Resource != "" && !apiResourcePattern.MatchString(connection.Resource) {
		return connection, errors.New("invalid API resource identifier in connection file")
	}
	if err := connection.Auth.Validate(); err != nil {
		return connection, fmt.Errorf("invalid connection auth: %w", err)
	}
	if connection.Auth != nil && (connection.Resource != "" || connection.ClientID != "" || connection.TenantID != "") {
		return connection, errors.New("auth cannot be combined with legacy sign-in settings")
	}
	if connection.ClientID != "" || connection.TenantID != "" {
		if connection.Resource == "" || !clientIDPattern.MatchString(connection.ClientID) || !tenantPattern.MatchString(connection.TenantID) {
			return connection, errors.New("connection file sign-in settings need an API resource, a client ID and a tenant")
		}
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
	if connection.CLIReleaseURL != "" {
		if err := validateReleaseDirectory(connection.CLIReleaseURL); err != nil {
			return connection, fmt.Errorf("invalid CLI release directory: %w", err)
		}
	}
	connection.Server = server.String()
	connection.SiteBaseURL = base.String()
	connection.Name = strings.TrimSpace(connection.Name)
	connection.LegacyPublishing = nil
	return connection, nil
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

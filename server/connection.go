package hex

import (
	"fmt"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"
)

// ConnectionConfig is the non-secret platform description the CLI imports.
// Auth selects the sign-in protocol, issuer, public client and scopes.
// Resource, ClientID and TenantID are legacy Entra settings, supported for
// existing installations. New platforms should configure Auth instead.
type ConnectionConfig struct {
	Name     string
	Server   string
	Resource string
	ClientID string
	TenantID string
	Auth     *AuthConfig
}

// Validate checks a host's complete, non-secret connection settings before
// startup. Request handlers use the same validation when serving them.
func (c *ConnectionConfig) Validate(siteBaseURL string) error {
	if c == nil {
		return fmt.Errorf("platform connection settings are not configured")
	}
	return validateConnection(c, siteBaseURL)
}

type connectionDocument struct {
	Version       int            `json:"version"`
	Name          string         `json:"name"`
	Server        string         `json:"server"`
	SiteBaseURL   string         `json:"siteBaseURL"`
	CLIReleaseURL string         `json:"cliReleaseURL,omitempty"`
	Resource      string         `json:"resource,omitempty"`
	ClientID      string         `json:"clientId,omitempty"`
	TenantID      string         `json:"tenantId,omitempty"`
	Auth          *AuthConfig    `json:"auth,omitempty"`
	Capabilities  map[string]any `json:"capabilities"`
}

func (s *Server) connectionConfig(w http.ResponseWriter, r *http.Request) {
	document, err := s.connectionSettings()
	if err != nil {
		writeServerError(w, err)
		return
	}

	w.Header().Set("Content-Disposition", `attachment; filename="hex-platform.json"`)
	writeJSON(w, http.StatusOK, document)
}

func (s *Server) connectionSettings() (connectionDocument, error) {
	connection := s.config.Connection
	if connection == nil {
		return connectionDocument{}, fmt.Errorf("platform connection settings are not configured")
	}
	if err := connection.Validate(s.config.SiteBaseURL); err != nil {
		return connectionDocument{}, fmt.Errorf("invalid platform connection settings: %w", err)
	}
	if _, err := s.cliReleaseURL(); err != nil {
		return connectionDocument{}, err
	}
	return connectionDocument{
		Version:       1,
		Name:          connection.Name,
		Server:        connection.Server,
		SiteBaseURL:   s.config.SiteBaseURL,
		CLIReleaseURL: s.config.CLIReleaseURL,
		Resource:      connection.Resource,
		ClientID:      connection.ClientID,
		TenantID:      connection.TenantID,
		Auth:          connection.Auth,
		Capabilities:  s.capabilityDescription(),
	}, nil
}

var (
	apiResourcePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9:/._-]{0,255}$`)
	clientIDPattern    = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
	tenantPattern      = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9.-]{0,252}$`)
)

func validateConnection(connection *ConnectionConfig, siteBaseURL string) error {
	if err := connection.Auth.Validate(); err != nil {
		return err
	}
	if connection.Auth != nil && (connection.Resource != "" || connection.ClientID != "" || connection.TenantID != "") {
		return fmt.Errorf("auth cannot be combined with legacy resource/clientId/tenantId settings")
	}
	if strings.TrimSpace(connection.Name) == "" {
		return fmt.Errorf("platform name is required")
	}
	if connection.Resource != "" && !apiResourcePattern.MatchString(connection.Resource) {
		return fmt.Errorf("invalid API resource identifier")
	}
	if connection.ClientID != "" || connection.TenantID != "" {
		if connection.Resource == "" || !clientIDPattern.MatchString(connection.ClientID) || !tenantPattern.MatchString(connection.TenantID) {
			return fmt.Errorf("CLI sign-in needs an API resource, a client ID (GUID) and a tenant ID or domain")
		}
	}
	if _, err := connectionOrigin(connection.Server); err != nil {
		return err
	}
	if _, err := parseSiteBaseURL(siteBaseURL); err != nil {
		return err
	}
	return nil
}

func connectionOrigin(value string) (*url.URL, error) {
	parsed, err := url.Parse(value)
	if err != nil {
		return nil, fmt.Errorf("invalid platform server URL")
	}
	validScheme := parsed.Scheme == "https" || (parsed.Scheme == "http" && loopbackHost(parsed.Hostname()))
	if !validScheme || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || (parsed.Path != "" && parsed.Path != "/") {
		return nil, fmt.Errorf("platform server must be an HTTPS origin, or HTTP on loopback")
	}
	return parsed, nil
}

func loopbackHost(host string) bool {
	return host == "localhost" || strings.HasSuffix(host, ".localhost") || net.ParseIP(host).IsLoopback()
}

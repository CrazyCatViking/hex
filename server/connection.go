package hex

import (
	"fmt"
	"net"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
)

type ConnectionConfig struct {
	Name       string
	Server     string
	Publishing *PublishingConfig
}

type PublishingConfig struct {
	Provider string `json:"provider"`
	Root     string `json:"root,omitempty"`
	URL      string `json:"url,omitempty"`
}

type connectionDocument struct {
	Version       int               `json:"version"`
	Name          string            `json:"name"`
	Server        string            `json:"server"`
	SiteBaseURL   string            `json:"siteBaseURL"`
	CLIReleaseURL string            `json:"cliReleaseURL,omitempty"`
	Publishing    *PublishingConfig `json:"publishing,omitempty"`
	Capabilities  map[string]any    `json:"capabilities"`
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
	if err := validateConnection(connection, s.config.SiteBaseURL); err != nil {
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
		Publishing:    connection.Publishing,
		Capabilities:  s.capabilityDescription(),
	}, nil
}

func validateConnection(connection *ConnectionConfig, siteBaseURL string) error {
	if strings.TrimSpace(connection.Name) == "" {
		return fmt.Errorf("platform name is required")
	}
	server, err := connectionOrigin(connection.Server)
	if err != nil {
		return err
	}
	if _, err := parseSiteBaseURL(siteBaseURL); err != nil {
		return err
	}
	if connection.Publishing == nil {
		return nil
	}

	publishing := connection.Publishing
	switch publishing.Provider {
	case "filesystem":
		if !loopbackHost(server.Hostname()) {
			return fmt.Errorf("filesystem publishing is only advertised by local platforms")
		}
		if !filepath.IsAbs(publishing.Root) || publishing.URL != "" {
			return fmt.Errorf("filesystem publishing requires an absolute root and no URL")
		}
	case "azure-files":
		destination, err := url.Parse(publishing.URL)
		if err != nil || destination.Scheme != "https" || destination.Host == "" || destination.User != nil || destination.RawQuery != "" || destination.Fragment != "" {
			return fmt.Errorf("Azure Files publishing requires an HTTPS URL without credentials or query parameters")
		}
		if destination.Path == "" || destination.Path == "/" || publishing.Root != "" {
			return fmt.Errorf("Azure Files publishing requires a share/prefix URL and no filesystem root")
		}
	default:
		return fmt.Errorf("unsupported publishing provider")
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

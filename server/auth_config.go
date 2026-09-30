package hex

import (
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"slices"
	"strings"
	"unicode"
)

func ParseAuthConfig(value string) (*AuthConfig, error) {
	if value == "" {
		return nil, nil
	}
	var config AuthConfig
	decoder := json.NewDecoder(strings.NewReader(value))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&config); err != nil {
		return nil, fmt.Errorf("decode auth config: %w", err)
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return nil, fmt.Errorf("auth config must be one JSON object")
	}
	if err := config.Validate(); err != nil {
		return nil, err
	}
	return &config, nil
}

// AuthConfig describes public-client sign-in independently of the hosting
// gateway's identity resolver. It contains no secrets and travels with the
// platform connection document and installers.
type AuthConfig struct {
	Type     string   `json:"type"`
	Issuer   string   `json:"issuer,omitempty"`
	ClientID string   `json:"clientId,omitempty"`
	Scopes   []string `json:"scopes,omitempty"`
}

func (a *AuthConfig) Validate() error {
	if a == nil {
		return nil
	}
	switch a.Type {
	case "none":
		if a.Issuer != "" || a.ClientID != "" || len(a.Scopes) != 0 {
			return fmt.Errorf("auth type none cannot have sign-in settings")
		}
	case "oidc":
		issuer, err := url.Parse(a.Issuer)
		if err != nil || issuer.Host == "" || issuer.User != nil || issuer.RawQuery != "" || issuer.Fragment != "" || (issuer.Scheme != "https" && !(issuer.Scheme == "http" && loopbackHost(issuer.Hostname()))) {
			return fmt.Errorf("OIDC issuer must use HTTPS, or HTTP on loopback, without credentials or query parameters")
		}
		if a.ClientID == "" || len(a.ClientID) > 256 || strings.ContainsFunc(a.ClientID, unicode.IsSpace) || strings.ContainsFunc(a.ClientID, unicode.IsControl) {
			return fmt.Errorf("OIDC auth requires a public client ID")
		}
		if len(a.Scopes) > 32 || !slices.Contains(a.Scopes, "openid") {
			return fmt.Errorf("OIDC auth requires scopes including openid (at most 32)")
		}
		for _, scope := range a.Scopes {
			if scope == "" || len(scope) > 256 || strings.ContainsFunc(scope, unicode.IsSpace) || strings.ContainsFunc(scope, unicode.IsControl) {
				return fmt.Errorf("invalid OIDC scope")
			}
		}
	default:
		return fmt.Errorf("unsupported auth type %q", a.Type)
	}
	return nil
}

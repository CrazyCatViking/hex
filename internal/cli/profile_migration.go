package cli

import (
	"encoding/json"
	"fmt"
	"net/url"
	"slices"
	"strings"

	hex "github.com/crazycatviking/hex/server"
)

func migrateConnectionAuth(connection Connection) (Connection, bool) {
	if connection.Auth != nil {
		return connection, false
	}
	if connection.Resource == "" && connection.ClientID == "" && connection.TenantID == "" {
		connection.Auth = &hex.AuthConfig{Type: "none"}
		return connection, true
	}
	// A tenant GUID identifies a concrete OIDC issuer. Domain aliases and
	// multi-tenant authorities need provider discovery to resolve that issuer,
	// so keep their existing sign-in behavior during this offline migration.
	if connection.Resource == "" || !clientIDPattern.MatchString(connection.ClientID) || !clientIDPattern.MatchString(connection.TenantID) {
		return connection, false
	}
	connection.Auth = &hex.AuthConfig{
		Type:     "oidc",
		Issuer:   "https://login.microsoftonline.com/" + strings.ToLower(connection.TenantID) + "/v2.0",
		ClientID: connection.ClientID,
		Scopes:   append([]string{"openid", "profile", "offline_access"}, signInScopes(Project{Resource: connection.Resource, ClientID: connection.ClientID})...),
	}
	connection.Resource = ""
	connection.ClientID = ""
	connection.TenantID = ""
	return connection, true
}

func migrateProfileAuth(store *profiles) (bool, error) {
	changed := false
	for name, data := range store.Profiles {
		connection, err := parseConnection(data, "")
		if err != nil {
			// Keep unsupported or damaged profiles intact. Loading that profile
			// still reports its validation error without blocking other profiles.
			continue
		}
		connection, migrated := migrateConnectionAuth(connection)
		if !migrated {
			continue
		}
		var document map[string]json.RawMessage
		if err := json.Unmarshal(data, &document); err != nil {
			return false, fmt.Errorf("read profile %q for migration: %w", name, err)
		}
		auth, err := json.Marshal(connection.Auth)
		if err != nil {
			return false, fmt.Errorf("encode profile %q auth: %w", name, err)
		}
		document["auth"] = auth
		delete(document, "resource")
		delete(document, "clientId")
		delete(document, "tenantId")
		updated, err := json.Marshal(document)
		if err != nil {
			return false, fmt.Errorf("encode migrated profile %q: %w", name, err)
		}
		store.Profiles[name] = updated
		changed = true
	}
	return changed, nil
}

func authMatchesResource(auth *hex.AuthConfig, resource string) bool {
	resource = strings.TrimRight(resource, "/")
	if auth == nil || auth.Type != "oidc" || resource == "" {
		return false
	}
	resources := []string{resource}
	// Entra accepts a registration's own API by its client GUID as well as
	// api://<client-guid>. Compare those spellings without rewriting scopes.
	if isEntraAuth(auth) && (strings.EqualFold(resource, auth.ClientID) || strings.EqualFold(resource, "api://"+auth.ClientID)) {
		resources = append(resources, auth.ClientID, "api://"+auth.ClientID)
	}
	for _, scope := range auth.Scopes {
		separator := strings.LastIndex(scope, "/")
		if separator > 0 && separator < len(scope)-1 && slices.Contains(resources, scope[:separator]) {
			return true
		}
	}
	return false
}

// isEntraAuth identifies the provider-specific own-API spelling above. The
// issuer alone never permits discarding configured auth or changing scopes.
func isEntraAuth(auth *hex.AuthConfig) bool {
	if auth == nil || auth.Type != "oidc" || !clientIDPattern.MatchString(auth.ClientID) {
		return false
	}
	issuer, err := url.Parse(auth.Issuer)
	if err != nil || issuer.Scheme != "https" || issuer.Host != "login.microsoftonline.com" {
		return false
	}
	parts := strings.Split(strings.Trim(issuer.Path, "/"), "/")
	return len(parts) == 2 && clientIDPattern.MatchString(parts[0]) && parts[1] == "v2.0"
}

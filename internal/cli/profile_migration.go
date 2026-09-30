package cli

import (
	"encoding/json"
	"fmt"
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
	if auth == nil || auth.Type != "oidc" || resource == "" {
		return false
	}
	scope := signInScopes(Project{Resource: resource, ClientID: auth.ClientID})[0]
	return slices.Contains(auth.Scopes, scope)
}

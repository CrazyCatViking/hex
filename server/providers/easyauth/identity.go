// Package easyauth resolves request identities from the headers that Azure
// App Service and Container Apps built-in authentication ("Easy Auth")
// injects into proxied requests. The gateway strips these headers from
// client-supplied values, so they are only trustworthy behind it; never
// enable this resolver on a directly reachable server.
package easyauth

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"

	hex "github.com/crazycatviking/hex/server"
)

const maxPrincipalBytes = 64 << 10

// Claim types that carry the Entra object ID when the identifying headers
// are absent.
var objectIDClaims = []string{
	"oid",
	"http://schemas.microsoft.com/identity/claims/objectidentifier",
}

type Resolver struct{}

type clientPrincipal struct {
	AuthType  string           `json:"auth_typ"`
	Claims    []principalClaim `json:"claims"`
	NameType  string           `json:"name_typ"`
	RolesType string           `json:"role_typ"`
}

type principalClaim struct {
	Type  string `json:"typ"`
	Value string `json:"val"`
}

func (Resolver) ResolveIdentity(r *http.Request) (*hex.Identity, error) {
	encoded := r.Header.Get("X-Ms-Client-Principal")
	if encoded == "" {
		return nil, nil
	}
	if len(encoded) > maxPrincipalBytes {
		return nil, fmt.Errorf("client principal header exceeds %d bytes", maxPrincipalBytes)
	}

	decoded, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return nil, fmt.Errorf("decode client principal header: %w", err)
	}
	var principal clientPrincipal
	if err := json.Unmarshal(decoded, &principal); err != nil {
		return nil, fmt.Errorf("parse client principal document: %w", err)
	}

	identity := &hex.Identity{
		Provider: principal.AuthType,
		ID:       r.Header.Get("X-Ms-Client-Principal-Id"),
		Name:     r.Header.Get("X-Ms-Client-Principal-Name"),
	}
	for _, claim := range principal.Claims {
		switch {
		case claim.Type == "groups":
			identity.Groups = append(identity.Groups, claim.Value)
		case claim.Type == "roles" || (principal.RolesType != "" && claim.Type == principal.RolesType):
			identity.Roles = append(identity.Roles, claim.Value)
		case identity.ID == "" && claimMatches(claim.Type, objectIDClaims):
			identity.ID = claim.Value
		case identity.Name == "" && principal.NameType != "" && claim.Type == principal.NameType:
			identity.Name = claim.Value
		}
	}

	if identity.ID == "" {
		return nil, fmt.Errorf("client principal is missing a stable identifier")
	}
	return identity, nil
}

func claimMatches(claimType string, candidates []string) bool {
	for _, candidate := range candidates {
		if claimType == candidate {
			return true
		}
	}
	return false
}

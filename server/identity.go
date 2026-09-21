package hex

import (
	"log/slog"
	"net/http"
	"slices"
	"strings"
)

// Identity describes the authenticated user behind a request, as resolved
// from the hosting layer. The framework never validates identity-provider
// tokens itself; a resolver translates what the trusted gateway forwarded.
type Identity struct {
	Provider string   `json:"provider,omitempty"`
	ID       string   `json:"id"`
	Name     string   `json:"name,omitempty"`
	Groups   []string `json:"groups,omitempty"`
	Roles    []string `json:"roles,omitempty"`
}

// IdentityResolver extracts the caller's identity from a request. A nil
// identity with a nil error means the request is anonymous. Resolvers must
// only be configured behind a hosting layer that authenticates requests and
// controls the headers the resolver reads.
type IdentityResolver interface {
	ResolveIdentity(r *http.Request) (*Identity, error)
}

// StaticIdentity resolves every request to one fixed identity. It exists for
// local development, where no authenticating gateway runs.
type StaticIdentity struct {
	Identity Identity
}

func (s StaticIdentity) ResolveIdentity(*http.Request) (*Identity, error) {
	identity := s.Identity
	identity.Groups = slices.Clone(identity.Groups)
	identity.Roles = slices.Clone(identity.Roles)
	return &identity, nil
}

// requestIdentity resolves the caller, treating resolver failures as an
// anonymous request so that access decisions fail closed.
func (s *Server) requestIdentity(r *http.Request) *Identity {
	if s.config.Identity == nil {
		return nil
	}

	identity, err := s.config.Identity.ResolveIdentity(r)
	if err != nil {
		slog.Error("resolve request identity", "path", r.URL.Path, "error", err)
		return nil
	}
	return identity
}

func (identity *Identity) memberOf(values []string) bool {
	if identity == nil {
		return false
	}

	for _, value := range values {
		if value == "" {
			continue
		}
		if strings.EqualFold(identity.ID, value) {
			return true
		}
		for _, group := range identity.Groups {
			if strings.EqualFold(group, value) {
				return true
			}
		}
		for _, role := range identity.Roles {
			if strings.EqualFold(role, value) {
				return true
			}
		}
	}

	return false
}

func (s *Server) me(w http.ResponseWriter, r *http.Request) {
	identity, err := s.config.Identity.ResolveIdentity(r)
	if err != nil {
		slog.Error("resolve request identity", "path", r.URL.Path, "error", err)
		writeError(w, http.StatusUnauthorized, "identity could not be resolved")
		return
	}
	if identity == nil {
		writeError(w, http.StatusUnauthorized, "not authenticated")
		return
	}

	writeJSON(w, http.StatusOK, identity)
}

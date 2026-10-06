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
// IDs are opaque, case-sensitive values of 1–256 non-whitespace printable ASCII
// characters. Providers own any claim canonicalization; core never folds case.
type Identity struct {
	Provider string   `json:"provider,omitempty"`
	ID       string   `json:"id"`
	Name     string   `json:"name,omitempty"`
	Email    string   `json:"email,omitempty"`
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
	if identity != nil && !identityIDPattern.MatchString(identity.ID) {
		slog.Error("resolve request identity: invalid stable identifier", "path", r.URL.Path)
		return nil
	}
	s.rememberPerson(r.Context(), identity)
	return identity
}

// matchesAny reports whether the identity is one of the principals. Typed
// principals (user:, group:, role:) match that kind of claim; a user matches
// only by stable identity ID. Untyped values, such as configured admin
// group IDs, match the ID, any group or any role.
func (identity *Identity) matchesAny(principals []string) bool {
	if identity == nil {
		return false
	}

	for _, principal := range principals {
		if identity.matches(principal) {
			return true
		}
	}
	return false
}

func (identity *Identity) matches(principal string) bool {
	kind, value, typed := splitPrincipal(principal)
	if value == "" {
		return false
	}

	matchesUser := identity.ID == value
	matchesGroup := slices.Contains(identity.Groups, value)
	matchesRole := slices.Contains(identity.Roles, value)

	if !typed {
		return matchesUser || matchesGroup || matchesRole
	}
	switch kind {
	case "user":
		return matchesUser
	case "group":
		return matchesGroup
	case "role":
		return matchesRole
	default:
		return false
	}
}

// Only the reserved prefixes identify a claim kind. Opaque untyped IDs may
// themselves contain colons; typed values keep everything after the first one.
func splitPrincipal(principal string) (kind, value string, typed bool) {
	kind, value, typed = strings.Cut(principal, ":")
	if typed && (kind == "user" || kind == "group" || kind == "role") {
		return kind, value, true
	}
	return "", principal, false
}

func (s *Server) me(w http.ResponseWriter, r *http.Request) {
	identity, err := s.config.Identity.ResolveIdentity(r)
	if err != nil {
		slog.Error("resolve request identity", "path", r.URL.Path, "error", err)
		writeError(w, http.StatusUnauthorized, "identity could not be resolved")
		return
	}
	if identity == nil || !identityIDPattern.MatchString(identity.ID) {
		writeError(w, http.StatusUnauthorized, "not authenticated")
		return
	}

	s.rememberPerson(r.Context(), identity)
	writeJSON(w, http.StatusOK, identity)
}

package hex

import (
	"context"
	"log/slog"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

// The directory turns principals into names without reading the identity
// provider's directory: people are remembered from their own sign-ins, and
// groups come from configuration. Only people who have used the platform can
// be found by name or email; policies always refer to their stable identity ID.

// PeopleStore remembers the people who have signed in to the platform.
// FindPeople matches names and emails case-insensitively; GetPeople looks
// people up by exact identity ID or email. Email/name search may ignore case;
// identity IDs must retain the resolver's canonical spelling.
type PeopleStore interface {
	RememberPerson(ctx context.Context, person Person) error
	FindPeople(ctx context.Context, query string, limit int) ([]Person, error)
	GetPeople(ctx context.Context, keys []string) ([]Person, error)
}

// NamedGroup is an identity-provider group the platform offers by name.
type NamedGroup struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

const (
	rememberInterval    = time.Hour
	maxRememberedPeople = 10000
)

// peopleSeen limits how often the same person is written to the store.
type peopleSeen struct {
	mu      sync.Mutex
	entries map[string]seenPerson
}

type seenPerson struct {
	person Person
	at     time.Time
}

// rememberPerson records the caller in the people store at most once an hour,
// or sooner when their name or email changes. Failures are logged; they never
// affect the request.
func (s *Server) rememberPerson(ctx context.Context, identity *Identity) {
	if (s.config.People == nil && s.config.Analytics == nil) || identity == nil || identity.ID == "" {
		return
	}
	person := *personOf(identity)

	now := time.Now()
	attempted := seenPerson{person: person, at: now}
	s.seen.mu.Lock()
	previous, ok := s.seen.entries[person.ID]
	fresh := ok && previous.person == person && now.Sub(previous.at) < rememberInterval
	if !fresh {
		if len(s.seen.entries) >= maxRememberedPeople {
			s.seen.entries = make(map[string]seenPerson)
		}
		s.seen.entries[person.ID] = attempted
	}
	s.seen.mu.Unlock()
	if fresh {
		return
	}

	var err error
	if s.config.People != nil {
		err = s.config.People.RememberPerson(ctx, person)
	}
	if s.config.Analytics != nil {
		if analyticsError := s.config.Analytics.ObservePerson(ctx, person, now.UTC()); analyticsError != nil {
			s.analyticsFailures.Add(1)
			slog.Error("observe analytics person", "error", analyticsError)
			if err == nil {
				err = analyticsError
			}
		}
	}
	if err != nil {
		s.seen.mu.Lock()
		if s.seen.entries[person.ID] == attempted {
			delete(s.seen.entries, person.ID)
		}
		s.seen.mu.Unlock()
		slog.Error("remember person", "id", person.ID, "error", err)
	}
}

type directoryResult struct {
	People []Person     `json:"people"`
	Groups []NamedGroup `json:"groups"`
}

// directory searches the people who have used the platform and the
// configured groups, for pickers.
func (s *Server) directory(w http.ResponseWriter, r *http.Request) {
	if s.requestIdentity(r) == nil {
		writeError(w, http.StatusUnauthorized, "not authenticated")
		return
	}
	limit := 20
	if value := r.URL.Query().Get("limit"); value != "" {
		parsed, err := strconv.Atoi(value)
		if err != nil || parsed < 1 || parsed > 50 {
			writeError(w, http.StatusBadRequest, "limit must be between 1 and 50")
			return
		}
		limit = parsed
	}

	result, err := s.searchDirectory(r.Context(), r.URL.Query().Get("query"), limit)
	if err != nil {
		writeServerError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) searchDirectory(ctx context.Context, query string, limit int) (directoryResult, error) {
	query = strings.TrimSpace(query)
	result := directoryResult{People: []Person{}, Groups: []NamedGroup{}}
	for _, group := range s.config.Groups {
		if query == "" || containsFold(group.Name, query) {
			result.Groups = append(result.Groups, group)
		}
	}
	if len(result.Groups) > limit {
		result.Groups = result.Groups[:limit]
	}

	if s.config.People != nil {
		people, err := s.config.People.FindPeople(ctx, query, limit)
		if err != nil {
			return result, err
		}
		result.People = people
	}
	return result, nil
}

// principalLabel is a readable name for a policy principal, falling back to
// its value.
type principalLabel struct {
	Principal string `json:"principal"`
	Kind      string `json:"kind"`
	Name      string `json:"name"`
	Detail    string `json:"detail,omitempty"`
}

// describePrincipals labels principals with remembered people and configured
// group names.
func (s *Server) describePrincipals(ctx context.Context, principals []string) []principalLabel {
	var userKeys []string
	for _, principal := range principals {
		if kind, value, _ := splitPrincipal(principal); kind == "user" {
			userKeys = append(userKeys, value)
		}
	}
	people := map[string]Person{}
	if s.config.People != nil && len(userKeys) > 0 {
		found, err := s.config.People.GetPeople(ctx, userKeys)
		if err != nil {
			slog.Error("look up people", "error", err)
		}
		for _, person := range found {
			people[person.ID] = person
		}
	}

	labels := make([]principalLabel, 0, len(principals))
	for _, principal := range principals {
		kind, value, typed := splitPrincipal(principal)
		label := principalLabel{Principal: principal, Kind: kind, Name: value}
		if !typed {
			label.Kind = "value"
			label.Name = principal
		}
		switch kind {
		case "user":
			if person, ok := people[value]; ok {
				label.Name = person.Name
				label.Detail = person.Email
			}
		case "group":
			if index := slices.IndexFunc(s.config.Groups, func(group NamedGroup) bool { return group.ID == value }); index >= 0 {
				label.Name = s.config.Groups[index].Name
				label.Detail = "group"
			}
		}
		labels = append(labels, label)
	}
	return labels
}

func containsFold(value, query string) bool {
	return strings.Contains(strings.ToLower(value), strings.ToLower(query))
}

// ParseGroups reads a "name=id,name=id" list, such as HEX_GROUPS.
func ParseGroups(value string) []NamedGroup {
	var groups []NamedGroup
	for _, entry := range strings.Split(value, ",") {
		name, id, ok := strings.Cut(strings.TrimSpace(entry), "=")
		name = strings.TrimSpace(name)
		id = strings.TrimSpace(id)
		if ok && name != "" && id != "" {
			groups = append(groups, NamedGroup{ID: id, Name: name})
		}
	}
	return groups
}

package memory

import (
	"context"
	"slices"
	"strings"
	"sync"

	hex "github.com/crazycatviking/hex/server"
)

// PeopleStore remembers people in process memory, for local development and
// tests.
type PeopleStore struct {
	mu     sync.RWMutex
	people map[string]hex.Person
}

func NewPeopleStore() *PeopleStore {
	return &PeopleStore{people: make(map[string]hex.Person)}
}

func (s *PeopleStore) RememberPerson(ctx context.Context, person hex.Person) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.people[person.ID] = person
	return nil
}

func (s *PeopleStore) FindPeople(ctx context.Context, query string, limit int) ([]hex.Person, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()

	query = strings.ToLower(query)
	found := []hex.Person{}
	for _, person := range s.people {
		if strings.Contains(strings.ToLower(person.Name), query) || strings.Contains(strings.ToLower(person.Email), query) {
			found = append(found, person)
		}
	}
	slices.SortFunc(found, func(left, right hex.Person) int {
		return strings.Compare(strings.ToLower(left.Name), strings.ToLower(right.Name))
	})
	if len(found) > limit {
		found = found[:limit]
	}
	return found, nil
}

func (s *PeopleStore) GetPeople(ctx context.Context, keys []string) ([]hex.Person, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()

	found := []hex.Person{}
	for _, person := range s.people {
		matches := slices.ContainsFunc(keys, func(key string) bool {
			return key == person.ID || (person.Email != "" && strings.EqualFold(key, person.Email))
		})
		if matches {
			found = append(found, person)
		}
	}
	return found, nil
}

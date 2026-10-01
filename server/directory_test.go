package hex_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	hex "github.com/crazycatviking/hex/server"
	"github.com/crazycatviking/hex/server/providers/easyauth"
	"github.com/crazycatviking/hex/server/providers/memory"
)

// signedIn is an Easy Auth caller with a display name and email.
func signedIn(t *testing.T, id, name, email string) http.Header {
	t.Helper()
	document, err := json.Marshal(map[string]any{
		"auth_typ": "aad", "name_typ": "name",
		"claims": []map[string]string{{"typ": "preferred_username", "val": email}},
	})
	if err != nil {
		t.Fatal(err)
	}
	headers := http.Header{}
	headers.Set("X-Ms-Client-Principal", base64.StdEncoding.EncodeToString(document))
	headers.Set("X-Ms-Client-Principal-Id", id)
	headers.Set("X-Ms-Client-Principal-Name", name)
	return headers
}

func TestDirectoryRemembersPeopleAndNamesGroups(t *testing.T) {
	server := hex.New(hex.Config{
		Identity: easyauth.Resolver{},
		People:   memory.NewPeopleStore(),
		Groups:   hex.ParseGroups("hex-users=11111111-0000-0000-0000-000000000001, Sales Team=22222222-0000-0000-0000-000000000002, broken"),
	})
	alex := signedIn(t, "alex-id", "Alex Andersen", "alex@example.test")
	bea := signedIn(t, "bea-id", "Bea Berg", "bea@example.test")

	// Anyone who has used the platform is remembered.
	requestAs(t, server, alex, "GET", "/api/hex/me", nil, 200)
	requestAs(t, server, bea, "GET", "/api/hex/me", nil, 200)

	var result struct {
		People []hex.Person     `json:"people"`
		Groups []hex.NamedGroup `json:"groups"`
	}
	decode := func(body string) {
		t.Helper()
		result.People, result.Groups = nil, nil
		if err := json.Unmarshal([]byte(body), &result); err != nil {
			t.Fatal(err)
		}
	}

	decode(requestAs(t, server, bea, "GET", "/api/hex/directory?query=ALEX@", nil, 200).Body.String())
	if len(result.People) != 1 || result.People[0] != (hex.Person{ID: "alex-id", Name: "Alex Andersen", Email: "alex@example.test"}) {
		t.Fatalf("email search failed: %+v", result.People)
	}

	decode(requestAs(t, server, bea, "GET", "/api/hex/directory?query=sales", nil, 200).Body.String())
	if len(result.Groups) != 1 || result.Groups[0].Name != "Sales Team" || len(result.People) != 0 {
		t.Fatalf("group search failed: %+v", result)
	}

	decode(requestAs(t, server, bea, "GET", "/api/hex/directory", nil, 200).Body.String())
	if len(result.People) != 2 || len(result.Groups) != 2 {
		t.Fatalf("empty query should list everything: %+v", result)
	}

	requestAs(t, server, nil, "GET", "/api/hex/directory", nil, 401)
	requestAs(t, server, bea, "GET", "/api/hex/directory?limit=500", nil, 400)
	if strings.Contains(requestAs(t, server, bea, "GET", "/api/hex/directory?query=%25", nil, 200).Body.String(), "alex") {
		t.Fatal("a percent sign matched everyone")
	}
}

type rememberingPeopleStore struct {
	hex.PeopleStore
	remember func(context.Context, hex.Person) error
}

func (s rememberingPeopleStore) RememberPerson(ctx context.Context, person hex.Person) error {
	return s.remember(ctx, person)
}

func TestRememberPersonRetriesPersistenceFailure(t *testing.T) {
	people := memory.NewPeopleStore()
	calls := 0
	store := rememberingPeopleStore{
		PeopleStore: people,
		remember: func(ctx context.Context, person hex.Person) error {
			calls++
			if calls == 1 {
				return errors.New("temporary persistence failure")
			}
			return people.RememberPerson(ctx, person)
		},
	}
	server := hex.New(hex.Config{Identity: easyauth.Resolver{}, People: store})
	headers := signedIn(t, "alice", "Alice", "alice@example.test")
	requestAs(t, server, headers, "GET", "/api/hex/me", nil, 200)
	requestAs(t, server, headers, "GET", "/api/hex/me", nil, 200)
	requestAs(t, server, headers, "GET", "/api/hex/me", nil, 200)
	if calls != 2 {
		t.Fatalf("got %d persistence calls, want failed attempt and successful retry", calls)
	}
	found, err := people.GetPeople(context.Background(), []string{"alice"})
	if err != nil || len(found) != 1 || found[0].Name != "Alice" {
		t.Fatalf("retry did not persist person: %+v, %v", found, err)
	}
}

func TestRememberPersonOlderFailureKeepsNewerUpdateFresh(t *testing.T) {
	people := memory.NewPeopleStore()
	started := make(chan struct{})
	release := make(chan struct{})
	var calls atomic.Int32
	store := rememberingPeopleStore{
		PeopleStore: people,
		remember: func(ctx context.Context, person hex.Person) error {
			calls.Add(1)
			if person.Name == "Old Name" {
				close(started)
				<-release
				return errors.New("older persistence failure")
			}
			return people.RememberPerson(ctx, person)
		},
	}
	server := hex.New(hex.Config{Identity: easyauth.Resolver{}, People: store})
	r := httptest.NewRequest("GET", "/api/hex/me", nil)
	r.Header = signedIn(t, "alice", "Old Name", "old@example.test")
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		w := httptest.NewRecorder()
		server.ServeHTTP(w, r)
		done <- w
	}()
	<-started
	headers := signedIn(t, "alice", "New Name", "new@example.test")
	w := httptest.NewRecorder()
	updated := httptest.NewRequest("GET", "/api/hex/me", nil)
	updated.Header = headers
	server.ServeHTTP(w, updated)
	close(release)
	old := <-done
	if old.Code != 200 || w.Code != 200 {
		t.Fatalf("persistence affected requests: old=%d, new=%d", old.Code, w.Code)
	}
	requestAs(t, server, headers, "GET", "/api/hex/me", nil, 200)
	if calls.Load() != 2 {
		t.Fatalf("older failure cleared newer freshness: %d persistence calls", calls.Load())
	}
	found, err := people.GetPeople(context.Background(), []string{"alice"})
	if err != nil || len(found) != 1 || found[0].Name != "New Name" || found[0].Email != "new@example.test" {
		t.Fatalf("newer name/email not persisted: %+v, %v", found, err)
	}
}

package postgres

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"sync"
	"testing"
	"time"

	hex "github.com/crazycatviking/hex/server"
)

func TestPostgresPermissionFiltersAndGuardedMutations(t *testing.T) {
	connection := os.Getenv("HEX_TEST_POSTGRES_URL")
	if connection == "" {
		t.Skip("set HEX_TEST_POSTGRES_URL to run against PostgreSQL")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	database, err := New(ctx, connection)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(database.Close)
	if err := database.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	site := "permissions-" + rand.Text()
	t.Cleanup(func() {
		if _, err := database.pool.Exec(context.Background(), "DELETE FROM hex_documents WHERE site=$1", site); err != nil {
			t.Error(err)
		}
	})
	data := []json.RawMessage{
		json.RawMessage(`{"project":"B","n":1,"tags":["a"],"nested":{"x":null}}`),
		json.RawMessage(`{"project":"A","n":1.00,"tags":["b","a"],"nested":{"x":true}}`),
		json.RawMessage(`{"project":"B","n":100000000000000000001,"tags":null}`),
		json.RawMessage(`{"project":"A","n":1e0,"tags":[{"a":2.0}],"nested":{"x":false}}`),
	}
	documents := make([]hex.Document, len(data))
	for i, value := range data {
		id := string(rune('a' + i))
		documents[i], err = database.Put(ctx, site, "expenses", id, value, hex.WriteOptions{Creator: "owner"})
		if err != nil {
			t.Fatal(err)
		}
	}
	conditions := []hex.RecordCondition{
		{Field: "data.project", Op: "eq", Value: json.RawMessage(`"A"`)},
		{Field: "data.project", Op: "ne", Value: json.RawMessage(`"A"`)},
		{Field: "data.n", Op: "eq", Value: json.RawMessage(`1`)},
		{Field: "data.n", Op: "eq", Value: json.RawMessage(`100000000000000000001`)},
		{Field: "data.missing", Op: "ne", Value: json.RawMessage(`null`)},
		{Field: "data.nested.x", Op: "in", Value: json.RawMessage(`[null,false]`)},
		{Field: "data.tags", Op: "contains", Value: json.RawMessage(`"a"`)},
		{Field: "data.tags", Op: "contains", Value: json.RawMessage(`{"a":2}`)},
		{Field: "createdBy", Op: "eq", Value: json.RawMessage(`"owner"`)},
		{Any: []hex.RecordCondition{{Field: "id", Op: "eq", Value: json.RawMessage(`"b"`)}, {Field: "data.project", Op: "eq", Value: json.RawMessage(`"A"`)}}},
	}
	for _, condition := range conditions {
		var expected []string
		for _, document := range documents {
			if condition.Matches(document) {
				expected = append(expected, document.ID)
			}
		}
		var actual []string
		after := ""
		for {
			page, err := database.ListMatching(ctx, site, "expenses", hex.ListOptions{After: after, Limit: 1}, &condition)
			if err != nil {
				t.Fatal(err)
			}
			if len(page) == 0 {
				break
			}
			actual = append(actual, page[0].ID)
			after = page[0].ID
		}
		if !reflect.DeepEqual(actual, expected) {
			t.Fatalf("SQL/evaluator mismatch for %+v: %v != %v", condition, actual, expected)
		}
	}
	_, err = database.Mutate(ctx, site, "expenses", "a", "attacker", func(before *hex.Document) (json.RawMessage, error) {
		before.Data = json.RawMessage(`{"tampered":true}`)
		return nil, hex.ErrForbidden
	})
	if !errors.Is(err, hex.ErrForbidden) {
		t.Fatal(err)
	}
	saved, err := database.Get(ctx, site, "expenses", "a")
	if err != nil {
		t.Fatal(err)
	}
	var original, actual map[string]any
	if err := json.Unmarshal(documents[0].Data, &original); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(saved.Data, &actual); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(actual, original) {
		t.Fatalf("rejected mutation changed state: %+v", saved)
	}
	var wait sync.WaitGroup
	results := make(chan error, 2)
	for range 2 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			_, err := database.Mutate(ctx, site, "expenses", "new", "creator", func(before *hex.Document) (json.RawMessage, error) {
				if before != nil {
					return nil, hex.ErrForbidden
				}
				return json.RawMessage(`{}`), nil
			})
			results <- err
		}()
	}
	wait.Wait()
	close(results)
	succeeded, refused := 0, 0
	for err := range results {
		switch {
		case err == nil:
			succeeded++
		case errors.Is(err, hex.ErrForbidden):
			refused++
		default:
			t.Fatal(err)
		}
	}
	if succeeded != 1 || refused != 1 {
		t.Fatalf("concurrent create authorization raced: %d successful, %d refused", succeeded, refused)
	}
}

func TestStoredPermissionEnvelopeFailsClosedOnOlderServers(t *testing.T) {
	policy := hex.SiteAccess{Version: 2, Owners: []string{"user:owner"}, Admission: hex.Subjects{Owners: true}}
	encoded, err := encodeSitePolicy(policy)
	if err != nil {
		t.Fatal(err)
	}
	var legacy struct {
		Owners []string `json:"owners"`
	}
	if err := json.Unmarshal(encoded, &legacy); err == nil {
		t.Fatal("old servers could interpret the new policy as open access")
	}
	var decoded hex.SiteAccess
	if err := decodeSitePolicy(encoded, &decoded); err != nil || !reflect.DeepEqual(policy, decoded) {
		t.Fatalf("policy did not round trip: %+v %v", decoded, err)
	}
}

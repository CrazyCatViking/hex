package postgres

import (
	"context"
	"fmt"
	"strings"

	hex "github.com/crazycatviking/hex/server"
)

func (d *Database) RememberPerson(ctx context.Context, person hex.Person) error {
	const query = `
		INSERT INTO hex_people (id, name, email, last_seen)
		VALUES ($1, $2, $3, now())
		ON CONFLICT (id)
		DO UPDATE SET name = EXCLUDED.name, email = EXCLUDED.email, last_seen = EXCLUDED.last_seen`

	if _, err := d.pool.Exec(ctx, query, person.ID, person.Name, person.Email); err != nil {
		return fmt.Errorf("remember person %s: %w", person.ID, err)
	}
	return nil
}

func (d *Database) FindPeople(ctx context.Context, query string, limit int) ([]hex.Person, error) {
	// position() rather than LIKE, so search text needs no escaping.
	const statement = `
		SELECT id, name, email FROM hex_people
		WHERE position(lower($1) in lower(name)) > 0 OR position(lower($1) in lower(email)) > 0
		ORDER BY lower(name), id
		LIMIT $2`

	return d.queryPeople(ctx, statement, query, limit)
}

func (d *Database) GetPeople(ctx context.Context, keys []string) ([]hex.Person, error) {
	const statement = `
		SELECT id, name, email FROM hex_people
		WHERE lower(id) = ANY($1) OR lower(email) = ANY($1)`

	lowered := make([]string, len(keys))
	for i, key := range keys {
		lowered[i] = strings.ToLower(key)
	}
	return d.queryPeople(ctx, statement, lowered)
}

func (d *Database) queryPeople(ctx context.Context, statement string, arguments ...any) ([]hex.Person, error) {
	rows, err := d.pool.Query(ctx, statement, arguments...)
	if err != nil {
		return nil, fmt.Errorf("query people: %w", err)
	}
	defer rows.Close()

	people := []hex.Person{}
	for rows.Next() {
		var person hex.Person
		if err := rows.Scan(&person.ID, &person.Name, &person.Email); err != nil {
			return nil, fmt.Errorf("decode person: %w", err)
		}
		people = append(people, person)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read people: %w", err)
	}
	return people, nil
}

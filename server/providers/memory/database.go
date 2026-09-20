package memory

import (
	"context"
	"encoding/json"
	"slices"
	"sync"

	hex "github.com/crazycatviking/hex/server"
)

type documentKey struct {
	site       string
	collection string
	id         string
}

type Database struct {
	mu        sync.RWMutex
	documents map[documentKey]json.RawMessage
}

func NewDatabase() *Database {
	return &Database{documents: make(map[documentKey]json.RawMessage)}
}

func (d *Database) Put(ctx context.Context, site, collection, id string, data json.RawMessage) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	d.mu.Lock()
	defer d.mu.Unlock()

	key := documentKey{site: site, collection: collection, id: id}
	d.documents[key] = slices.Clone(data)
	return nil
}

func (d *Database) Get(ctx context.Context, site, collection, id string) (hex.Document, error) {
	if err := ctx.Err(); err != nil {
		return hex.Document{}, err
	}

	d.mu.RLock()
	defer d.mu.RUnlock()

	key := documentKey{site: site, collection: collection, id: id}
	data, ok := d.documents[key]
	if !ok {
		return hex.Document{}, hex.ErrNotFound
	}

	return hex.Document{ID: id, Data: slices.Clone(data)}, nil
}

func (d *Database) List(ctx context.Context, site, collection, after string, limit int) ([]hex.Document, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	d.mu.RLock()
	defer d.mu.RUnlock()

	var ids []string
	for key := range d.documents {
		if key.site == site && key.collection == collection && key.id > after {
			ids = append(ids, key.id)
		}
	}

	slices.Sort(ids)
	if len(ids) > limit {
		ids = ids[:limit]
	}

	documents := make([]hex.Document, 0, len(ids))
	for _, id := range ids {
		key := documentKey{site: site, collection: collection, id: id}
		documents = append(documents, hex.Document{
			ID:   id,
			Data: slices.Clone(d.documents[key]),
		})
	}

	return documents, nil
}

func (d *Database) Delete(ctx context.Context, site, collection, id string) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	d.mu.Lock()
	defer d.mu.Unlock()

	key := documentKey{site: site, collection: collection, id: id}
	if _, ok := d.documents[key]; !ok {
		return hex.ErrNotFound
	}

	delete(d.documents, key)
	return nil
}

package memory

import (
	"context"
	"encoding/json"
	"sort"
	"sync"

	hex "github.com/hex-platform/hex/server"
)

type Database struct {
	mu   sync.RWMutex
	docs map[[3]string]json.RawMessage
}

func NewDatabase() *Database { return &Database{docs: make(map[[3]string]json.RawMessage)} }

func (d *Database) Put(ctx context.Context, site, collection, id string, data json.RawMessage) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	d.docs[[3]string{site, collection, id}] = append(json.RawMessage(nil), data...)
	return nil
}

func (d *Database) Get(ctx context.Context, site, collection, id string) (hex.Document, error) {
	if err := ctx.Err(); err != nil {
		return hex.Document{}, err
	}
	d.mu.RLock()
	defer d.mu.RUnlock()
	data, ok := d.docs[[3]string{site, collection, id}]
	if !ok {
		return hex.Document{}, hex.ErrNotFound
	}
	return hex.Document{ID: id, Data: append(json.RawMessage(nil), data...)}, nil
}

func (d *Database) List(ctx context.Context, site, collection, after string, limit int) ([]hex.Document, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	d.mu.RLock()
	defer d.mu.RUnlock()
	ids := []string{}
	for key := range d.docs {
		if key[0] == site && key[1] == collection && key[2] > after {
			ids = append(ids, key[2])
		}
	}
	sort.Strings(ids)
	if len(ids) > limit {
		ids = ids[:limit]
	}
	result := []hex.Document{}
	for _, id := range ids {
		result = append(result, hex.Document{ID: id, Data: append(json.RawMessage(nil), d.docs[[3]string{site, collection, id}]...)})
	}
	return result, nil
}

func (d *Database) Delete(ctx context.Context, site, collection, id string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	key := [3]string{site, collection, id}
	if _, ok := d.docs[key]; !ok {
		return hex.ErrNotFound
	}
	delete(d.docs, key)
	return nil
}

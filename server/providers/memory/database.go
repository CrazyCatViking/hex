package memory

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"sync"

	hex "github.com/crazycatviking/hex/server"
)

type documentKey struct {
	site       string
	collection string
	id         string
}

type storedDocument struct {
	data      json.RawMessage
	createdBy string
}

type Database struct {
	mu        sync.RWMutex
	documents map[documentKey]storedDocument
}

func NewDatabase() *Database {
	return &Database{documents: make(map[documentKey]storedDocument)}
}

func (d *Database) Put(ctx context.Context, site, collection, id string, data json.RawMessage, options hex.WriteOptions) (hex.Document, error) {
	if err := ctx.Err(); err != nil {
		return hex.Document{}, err
	}

	d.mu.Lock()
	defer d.mu.Unlock()

	key := documentKey{site: site, collection: collection, id: id}
	stored, exists := d.documents[key]
	if exists && options.CreatorOnly && stored.createdBy != options.Creator {
		return hex.Document{}, hex.ErrForbidden
	}
	if !exists {
		stored.createdBy = options.Creator
	}
	stored.data = slices.Clone(data)
	d.documents[key] = stored

	return hex.Document{ID: id, Data: slices.Clone(data), CreatedBy: stored.createdBy}, nil
}

func (d *Database) Get(ctx context.Context, site, collection, id string) (hex.Document, error) {
	if err := ctx.Err(); err != nil {
		return hex.Document{}, err
	}

	d.mu.RLock()
	defer d.mu.RUnlock()

	key := documentKey{site: site, collection: collection, id: id}
	stored, ok := d.documents[key]
	if !ok {
		return hex.Document{}, hex.ErrNotFound
	}

	return hex.Document{ID: id, Data: slices.Clone(stored.data), CreatedBy: stored.createdBy}, nil
}

func (d *Database) List(ctx context.Context, site, collection string, options hex.ListOptions) ([]hex.Document, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	d.mu.RLock()
	defer d.mu.RUnlock()

	var ids []string
	for key, stored := range d.documents {
		if key.site != site || key.collection != collection || key.id <= options.After {
			continue
		}
		if options.CreatedBy != "" && stored.createdBy != options.CreatedBy {
			continue
		}
		ids = append(ids, key.id)
	}

	slices.Sort(ids)
	if len(ids) > options.Limit {
		ids = ids[:options.Limit]
	}

	documents := make([]hex.Document, 0, len(ids))
	for _, id := range ids {
		stored := d.documents[documentKey{site: site, collection: collection, id: id}]
		documents = append(documents, hex.Document{
			ID:        id,
			Data:      slices.Clone(stored.data),
			CreatedBy: stored.createdBy,
		})
	}

	return documents, nil
}

func (d *Database) Delete(ctx context.Context, site, collection, id string, options hex.WriteOptions) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	d.mu.Lock()
	defer d.mu.Unlock()

	key := documentKey{site: site, collection: collection, id: id}
	stored, ok := d.documents[key]
	if !ok {
		return hex.ErrNotFound
	}
	if options.CreatorOnly && stored.createdBy != options.Creator {
		return hex.ErrForbidden
	}

	delete(d.documents, key)
	return nil
}

func (d *Database) ListCollections(ctx context.Context, site string) ([]hex.Collection, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	d.mu.RLock()
	defer d.mu.RUnlock()

	counts := map[string]int{}
	for key := range d.documents {
		if key.site == site {
			counts[key.collection]++
		}
	}
	collections := make([]hex.Collection, 0, len(counts))
	for name, count := range counts {
		collections = append(collections, hex.Collection{Name: name, Documents: count})
	}
	slices.SortFunc(collections, func(left, right hex.Collection) int {
		return strings.Compare(left.Name, right.Name)
	})
	return collections, nil
}

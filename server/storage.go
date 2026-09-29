package hex

import (
	"context"
	"encoding/json"
	"errors"
	"io"
)

var ErrNotFound = errors.New("not found")

// ErrForbidden reports that a conditional write was refused, such as replacing
// a document created by someone else under a creator-only rule.
var ErrForbidden = errors.New("forbidden")

type SiteDirectory interface {
	ListSites(ctx context.Context) ([]string, error)
}

type Object struct {
	Key  string `json:"key"`
	Size int64  `json:"size"`
}

type ObjectStore interface {
	Put(ctx context.Context, key string, source io.Reader) error
	Open(ctx context.Context, key string) (io.ReadCloser, error)
	List(ctx context.Context, prefix string) ([]Object, error)
	Delete(ctx context.Context, key string) error
}

// Document is a site-scoped JSON object. CreatedBy is the identity ID of the
// caller that first stored it, recorded by the server; it is empty for
// documents written without an identity.
type Document struct {
	ID        string          `json:"id"`
	Data      json.RawMessage `json:"data"`
	CreatedBy string          `json:"createdBy,omitempty"`
}

// WriteOptions controls document writes and deletions. Put returns the stored
// document, including its original creator. Creator is recorded
// when a document is inserted and never changed by later writes. With
// CreatorOnly, an existing document is replaced or deleted only when it was
// created by Creator; otherwise the operation returns ErrForbidden.
type WriteOptions struct {
	Creator     string
	CreatorOnly bool
}

// ListOptions selects a page of documents. After is exclusive, Limit is
// 1–100, and a non-empty CreatedBy restricts results to that creator.
type ListOptions struct {
	After     string
	Limit     int
	CreatedBy string
}

type Database interface {
	Put(ctx context.Context, site, collection, id string, data json.RawMessage, options WriteOptions) (Document, error)
	Get(ctx context.Context, site, collection, id string) (Document, error)
	List(ctx context.Context, site, collection string, options ListOptions) ([]Document, error)
	Delete(ctx context.Context, site, collection, id string, options WriteOptions) error
}

// Collection summarizes one collection of a site's documents.
type Collection struct {
	Name      string `json:"name"`
	Documents int    `json:"documents"`
}

// CollectionLister is implemented by databases that can list a site's
// collections, which the management portal's data browser uses.
type CollectionLister interface {
	ListCollections(ctx context.Context, site string) ([]Collection, error)
}

type Subscription interface {
	Messages() <-chan json.RawMessage
	Close()
}

type Realtime interface {
	Subscribe(ctx context.Context, room string) (Subscription, error)
	Publish(ctx context.Context, room string, message json.RawMessage) error
}

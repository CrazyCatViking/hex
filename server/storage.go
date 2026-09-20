package hex

import (
	"context"
	"encoding/json"
	"errors"
	"io"
)

var ErrNotFound = errors.New("not found")

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

type Document struct {
	ID   string          `json:"id"`
	Data json.RawMessage `json:"data"`
}

type Database interface {
	Put(ctx context.Context, site, collection, id string, data json.RawMessage) error
	Get(ctx context.Context, site, collection, id string) (Document, error)
	List(ctx context.Context, site, collection, after string, limit int) ([]Document, error)
	Delete(ctx context.Context, site, collection, id string) error
}

type Subscription interface {
	Messages() <-chan json.RawMessage
	Close()
}

type Realtime interface {
	Subscribe(ctx context.Context, room string) (Subscription, error)
	Publish(ctx context.Context, room string, message json.RawMessage) error
}

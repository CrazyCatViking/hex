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
	Put(context.Context, string, io.Reader) error
	Open(context.Context, string) (io.ReadCloser, error)
	List(context.Context, string) ([]Object, error)
	Delete(context.Context, string) error
}

type Document struct {
	ID   string          `json:"id"`
	Data json.RawMessage `json:"data"`
}

type Database interface {
	Put(context.Context, string, string, string, json.RawMessage) error
	Get(context.Context, string, string, string) (Document, error)
	List(context.Context, string, string, string, int) ([]Document, error)
	Delete(context.Context, string, string, string) error
}

type Subscription interface {
	Messages() <-chan json.RawMessage
	Close()
}

type Realtime interface {
	Subscribe(context.Context, string) (Subscription, error)
	Publish(context.Context, string, json.RawMessage) error
}

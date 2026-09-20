package memory

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"slices"
	"strings"
	"sync"

	hex "github.com/hex-platform/hex/server"
)

type Store struct {
	mu      sync.RWMutex
	objects map[string][]byte
}

func NewStore() *Store {
	return &Store{objects: make(map[string][]byte)}
}

func (s *Store) Put(ctx context.Context, key string, source io.Reader) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	data, err := io.ReadAll(source)
	if err != nil {
		return fmt.Errorf("read object %q: %w", key, err)
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	s.objects[key] = data
	return nil
}

func (s *Store) Open(ctx context.Context, key string) (io.ReadCloser, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	data, exists := s.objects[key]
	if !exists {
		return nil, hex.ErrNotFound
	}

	return io.NopCloser(bytes.NewReader(data)), nil
}

func (s *Store) List(ctx context.Context, prefix string) ([]hex.Object, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	objects := []hex.Object{}
	for key, data := range s.objects {
		if strings.HasPrefix(key, prefix) {
			objects = append(objects, hex.Object{Key: key, Size: int64(len(data))})
		}
	}
	slices.SortFunc(objects, func(left, right hex.Object) int {
		return strings.Compare(left.Key, right.Key)
	})

	return objects, nil
}

func (s *Store) Delete(ctx context.Context, key string) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if _, exists := s.objects[key]; !exists {
		return hex.ErrNotFound
	}
	delete(s.objects, key)
	return nil
}

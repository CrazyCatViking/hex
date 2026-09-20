package local

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	hex "github.com/hex-platform/hex/server"
)

type Store struct{ root *os.Root }

func New(directory string) (*Store, error) {
	if err := os.MkdirAll(directory, 0750); err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(directory)
	if err != nil {
		return nil, err
	}
	return &Store{root: root}, nil
}

func (s *Store) Close() error { return s.root.Close() }

func valid(key string) bool {
	return key != "." && fs.ValidPath(key) && !strings.Contains(key, "\\")
}

func (s *Store) Put(ctx context.Context, key string, src io.Reader) error {
	if !valid(key) {
		return fs.ErrInvalid
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := s.root.MkdirAll(filepath.Dir(key), 0750); err != nil {
		return err
	}
	id, err := randomName()
	if err != nil {
		return err
	}
	tmp := filepath.Join(filepath.Dir(key), ".hex-"+id)
	f, err := s.root.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0640)
	if err != nil {
		return err
	}
	defer s.root.Remove(tmp)
	_, copyErr := io.Copy(f, src)
	closeErr := f.Close()
	if copyErr != nil {
		return copyErr
	}
	if closeErr != nil {
		return closeErr
	}
	return s.root.Rename(tmp, key)
}

func (s *Store) Open(ctx context.Context, key string) (io.ReadCloser, error) {
	if !valid(key) {
		return nil, fs.ErrInvalid
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	f, err := s.root.Open(key)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, hex.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		f.Close()
		return nil, hex.ErrNotFound
	}
	return f, nil
}

func (s *Store) List(ctx context.Context, prefix string) ([]hex.Object, error) {
	if prefix != "" && !valid(strings.TrimSuffix(prefix, "/")) {
		return nil, fs.ErrInvalid
	}
	result := []hex.Object{}
	err := fs.WalkDir(s.root.FS(), ".", func(key string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if d.IsDir() || !d.Type().IsRegular() || !strings.HasPrefix(key, prefix) || strings.HasPrefix(d.Name(), ".hex-") {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		result = append(result, hex.Object{Key: key, Size: info.Size()})
		return nil
	})
	return result, err
}

func (s *Store) Delete(ctx context.Context, key string) error {
	if !valid(key) {
		return fs.ErrInvalid
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	err := s.root.Remove(key)
	if errors.Is(err, fs.ErrNotExist) {
		return hex.ErrNotFound
	}
	if err == nil {
		for parent := filepath.Dir(key); parent != "."; parent = filepath.Dir(parent) {
			if err := s.root.Remove(parent); err != nil {
				break
			}
		}
	}
	return err
}

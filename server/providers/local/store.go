package local

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	hex "github.com/hex-platform/hex/server"
)

type Store struct {
	root *os.Root
}

func New(directory string) (*Store, error) {
	if err := os.MkdirAll(directory, 0750); err != nil {
		return nil, fmt.Errorf("create storage directory %q: %w", directory, err)
	}

	root, err := os.OpenRoot(directory)
	if err != nil {
		return nil, fmt.Errorf("open storage directory %q: %w", directory, err)
	}

	return &Store{root: root}, nil
}

func (s *Store) Close() error {
	return s.root.Close()
}

func validKey(key string) bool {
	return key != "." && fs.ValidPath(key) && !strings.Contains(key, "\\")
}

func (s *Store) Put(ctx context.Context, key string, source io.Reader) error {
	if !validKey(key) {
		return fs.ErrInvalid
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	directory := filepath.Dir(key)
	if err := s.root.MkdirAll(directory, 0750); err != nil {
		return fmt.Errorf("create object directory %q: %w", directory, err)
	}

	id, err := randomName()
	if err != nil {
		return fmt.Errorf("name temporary object: %w", err)
	}

	temporaryPath := filepath.Join(directory, ".hex-"+id)
	file, err := s.root.OpenFile(temporaryPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0640)
	if err != nil {
		return fmt.Errorf("create temporary object for %q: %w", key, err)
	}
	defer s.removeTemporaryFile(temporaryPath)

	_, copyError := io.Copy(file, source)
	closeError := file.Close()
	if err := errors.Join(copyError, closeError); err != nil {
		return fmt.Errorf("write object %q: %w", key, err)
	}

	if err := s.root.Rename(temporaryPath, key); err != nil {
		return fmt.Errorf("replace object %q: %w", key, err)
	}

	return nil
}

func (s *Store) removeTemporaryFile(key string) {
	err := s.root.Remove(key)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		slog.Error("remove temporary object", "key", key, "error", err)
	}
}

func (s *Store) Open(ctx context.Context, key string) (io.ReadCloser, error) {
	if !validKey(key) {
		return nil, fs.ErrInvalid
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	file, err := s.root.Open(key)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, hex.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("open object %q: %w", key, err)
	}

	info, err := file.Stat()
	if err != nil {
		return nil, fmt.Errorf("inspect object %q: %w", key, errors.Join(err, file.Close()))
	}
	if !info.Mode().IsRegular() {
		return nil, errors.Join(hex.ErrNotFound, file.Close())
	}

	return file, nil
}

func (s *Store) List(ctx context.Context, prefix string) ([]hex.Object, error) {
	if prefix != "" && !validKey(strings.TrimSuffix(prefix, "/")) {
		return nil, fs.ErrInvalid
	}

	objects := []hex.Object{}
	err := fs.WalkDir(s.root.FS(), ".", func(key string, entry fs.DirEntry, walkError error) error {
		if walkError != nil {
			return walkError
		}
		if err := ctx.Err(); err != nil {
			return err
		}

		if !entry.Type().IsRegular() || !strings.HasPrefix(key, prefix) {
			return nil
		}
		if strings.HasPrefix(entry.Name(), ".hex-") {
			return nil
		}

		info, err := entry.Info()
		if err != nil {
			return err
		}

		objects = append(objects, hex.Object{Key: key, Size: info.Size()})
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("list objects under %q: %w", prefix, err)
	}

	return objects, nil
}

func (s *Store) Delete(ctx context.Context, key string) error {
	if !validKey(key) {
		return fs.ErrInvalid
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	err := s.root.Remove(key)
	if errors.Is(err, fs.ErrNotExist) {
		return hex.ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("delete object %q: %w", key, err)
	}

	s.removeEmptyParents(key)
	return nil
}

func (s *Store) removeEmptyParents(key string) {
	for parent := filepath.Dir(key); parent != "."; parent = filepath.Dir(parent) {
		err := s.root.Remove(parent)
		if err == nil {
			continue
		}
		if errors.Is(err, syscall.ENOTEMPTY) || errors.Is(err, fs.ErrNotExist) {
			return
		}

		slog.Error("remove empty object directory", "directory", parent, "error", err)
		return
	}
}

package local

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"path"
)

func (s *Store) ListSites(ctx context.Context) ([]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	directory, err := s.root.Open("public/sites")
	if errors.Is(err, fs.ErrNotExist) {
		return []string{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("open site directory: %w", err)
	}
	defer func() {
		if err := directory.Close(); err != nil {
			slog.Error("close site directory", "error", err)
		}
	}()

	entries, err := directory.ReadDir(-1)
	if err != nil {
		return nil, fmt.Errorf("enumerate site directories: %w", err)
	}

	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if !entry.IsDir() {
			continue
		}

		indexPath := path.Join("public/sites", entry.Name(), "index.html")
		info, err := s.root.Lstat(indexPath)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("inspect site %q: %w", entry.Name(), err)
		}
		if info.Mode().IsRegular() {
			names = append(names, entry.Name())
		}
	}

	return names, nil
}

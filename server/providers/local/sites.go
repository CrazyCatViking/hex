package local

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"path"

	hex "github.com/crazycatviking/hex/server"
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

func (s *Store) ReadSiteMetadata(ctx context.Context, name string) (*hex.SiteMetadata, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if name == "" || name == "." || name == ".." || path.Base(name) != name {
		return nil, errors.New("invalid site name")
	}
	filename := path.Join("public/sites", name, ".hex-site.json")
	info, err := s.root.Lstat(filename)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("inspect metadata for site %q: %w", name, err)
	}
	if !info.Mode().IsRegular() || info.Size() > 64*1024 {
		return nil, fmt.Errorf("metadata for site %q must be a regular file of at most 64 KiB", name)
	}
	file, err := s.root.Open(filename)
	if err != nil {
		return nil, fmt.Errorf("open metadata for site %q: %w", name, err)
	}
	data, readErr := io.ReadAll(io.LimitReader(file, 64*1024+1))
	if err := errors.Join(readErr, file.Close()); err != nil {
		return nil, fmt.Errorf("read metadata for site %q: %w", name, err)
	}
	if len(data) > 64*1024 {
		return nil, fmt.Errorf("metadata for site %q exceeds 64 KiB", name)
	}
	var metadata hex.SiteMetadata
	if err := json.Unmarshal(data, &metadata); err != nil {
		return nil, fmt.Errorf("parse metadata for site %q: %w", name, err)
	}
	return &metadata, nil
}

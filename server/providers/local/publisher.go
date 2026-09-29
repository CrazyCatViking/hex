package local

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path"
	"strings"

	hex "github.com/crazycatviking/hex/server"
)

// The Store publishes sites under public/sites/<site>/ of its root, the same
// layout ListSites discovers and NGINX serves.

func sitePath(site, name string) (string, error) {
	key := path.Join("public/sites", site, name)
	if site == "" || path.Base(site) != site || !strings.HasPrefix(key, "public/sites/"+site+"/") || !validKey(key) {
		return "", fs.ErrInvalid
	}
	return key, nil
}

func (s *Store) ListSiteFiles(ctx context.Context, site string) ([]hex.SiteFile, error) {
	directory := path.Join("public/sites", site)
	if path.Base(site) != site || site == "" || site == "." || site == ".." {
		return nil, fs.ErrInvalid
	}

	files := []hex.SiteFile{}
	err := fs.WalkDir(s.root.FS(), directory, func(key string, entry fs.DirEntry, walkError error) error {
		if errors.Is(walkError, fs.ErrNotExist) && key == directory {
			return fs.SkipAll
		}
		if walkError != nil {
			return walkError
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if !entry.Type().IsRegular() || temporaryName(entry.Name()) {
			return nil
		}

		info, err := entry.Info()
		if err != nil {
			return err
		}
		files = append(files, hex.SiteFile{
			Path: strings.TrimPrefix(key, directory+"/"),
			Size: info.Size(),
		})
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("list files of site %q: %w", site, err)
	}
	return files, nil
}

// temporaryName recognizes in-progress writes (.hex-<random>), as opposed to
// the server-owned .hex-site.json and .hex-manifest.json records.
func temporaryName(name string) bool {
	return strings.HasPrefix(name, ".hex-") && !strings.HasSuffix(name, ".json")
}

func (s *Store) ReadSiteFile(ctx context.Context, site, name string) (io.ReadCloser, error) {
	key, err := sitePath(site, name)
	if err != nil {
		return nil, err
	}
	return s.Open(ctx, key)
}

func (s *Store) WriteSiteFile(ctx context.Context, site, name string, size int64, source io.Reader) error {
	key, err := sitePath(site, name)
	if err != nil {
		return err
	}

	counter := &countingReader{reader: source}
	if err := s.Put(ctx, key, counter); err != nil {
		return err
	}
	if counter.count != size {
		return errors.Join(
			fmt.Errorf("site file %s/%s: received %d bytes, expected %d", site, name, counter.count, size),
			s.Delete(ctx, key),
		)
	}
	return nil
}

func (s *Store) DeleteSiteFile(ctx context.Context, site, name string) error {
	key, err := sitePath(site, name)
	if err != nil {
		return err
	}
	return s.Delete(ctx, key)
}

func (s *Store) DeleteSite(ctx context.Context, site string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if path.Base(site) != site || site == "" || site == "." || site == ".." {
		return fs.ErrInvalid
	}

	if err := s.root.RemoveAll(path.Join("public/sites", site)); err != nil {
		return fmt.Errorf("delete site %q: %w", site, err)
	}
	return nil
}

type countingReader struct {
	reader io.Reader
	count  int64
}

func (c *countingReader) Read(buffer []byte) (int, error) {
	read, err := c.reader.Read(buffer)
	c.count += int64(read)
	return read, err
}

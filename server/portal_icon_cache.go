package hex

import (
	"bytes"
	"context"
	"errors"
	"io"
	"sync"
	"time"
)

// Cache only file bytes, never authorization or rendered icon responses. The
// icon handler still checks current admission and each candidate's path policy.
type portalIconCache struct {
	mu      sync.Mutex
	entries map[string]cachedIconFile
}

type cachedIconFile struct {
	data    []byte
	missing bool
	expires time.Time
}

type cachedIconReader struct {
	server *Server
	reader siteFileReader
}

func (r cachedIconReader) ReadSiteFile(ctx context.Context, site, name string) (io.ReadCloser, error) {
	cache := &r.server.portalIcons
	key := site + "/" + name
	cache.mu.Lock()
	entry, exists := cache.entries[key]
	cache.mu.Unlock()
	if exists && time.Now().Before(entry.expires) {
		if entry.missing {
			return nil, ErrNotFound
		}
		return io.NopCloser(bytes.NewReader(entry.data)), nil
	}
	file, err := r.reader.ReadSiteFile(ctx, site, name)
	entry = cachedIconFile{expires: time.Now().Add(30 * time.Second)}
	if errors.Is(err, ErrNotFound) {
		entry.missing = true
	} else if err != nil {
		return nil, err
	} else {
		data, readError := io.ReadAll(io.LimitReader(file, maxSiteIconBytes+1))
		if err := errors.Join(readError, file.Close()); err != nil {
			return nil, err
		}
		if len(data) > maxSiteIconBytes {
			if name == "index.html" {
				// Icon discovery only needs the bounded document head, even when
				// the application has a large inline bundle or report body.
				data = data[:maxSiteIconBytes]
			} else {
				entry.missing = true
				data = nil
			}
		}
		entry.data = data
	}
	cache.mu.Lock()
	if cache.entries == nil {
		cache.entries = make(map[string]cachedIconFile)
	}
	if len(cache.entries) >= 128 {
		clear(cache.entries)
	}
	cache.entries[key] = entry
	cache.mu.Unlock()
	if entry.missing {
		return nil, ErrNotFound
	}
	return io.NopCloser(bytes.NewReader(entry.data)), nil
}

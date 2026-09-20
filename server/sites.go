package hex

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

type Site struct {
	Name        string    `json:"name"`
	Release     string    `json:"release"`
	URL         string    `json:"url"`
	PublishedAt time.Time `json:"publishedAt"`
}

func (s *Server) deploy(w http.ResponseWriter, r *http.Request) {
	if !validateIdentifiers(w, r) {
		return
	}

	data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, s.config.MaxUploadBytes))
	if err != nil {
		writeError(w, http.StatusRequestEntityTooLarge, "deployment exceeds upload limit")
		return
	}

	archive, files, err := validateArchive(data, s.config.MaxUploadBytes)
	if err != nil {
		writeDeploymentError(w, err)
		return
	}

	releaseID, err := newID()
	if err != nil {
		writeServerError(w, err)
		return
	}

	s.siteWrites.Lock()
	defer s.siteWrites.Unlock()

	site := Site{
		Name:        r.PathValue("site"),
		Release:     releaseID,
		URL:         "/sites/" + r.PathValue("site") + "/",
		PublishedAt: time.Now().UTC(),
	}
	written, err := s.stageRelease(r.Context(), site, archive)
	committed := false
	defer func() {
		if !committed {
			s.removeStagedFiles(r.Context(), written)
		}
	}()
	if err != nil {
		writeDeploymentError(w, err)
		return
	}

	if err := s.publishFiles(r.Context(), site, files); err != nil {
		writeServerError(w, err)
		return
	}

	manifest, err := json.Marshal(site)
	if err != nil {
		writeServerError(w, fmt.Errorf("encode site manifest: %w", err))
		return
	}

	manifestKey := "sites/" + site.Name + ".json"
	if err := s.config.Sites.Put(r.Context(), manifestKey, bytes.NewReader(manifest)); err != nil {
		writeServerError(w, err)
		return
	}

	committed = true
	writeJSON(w, http.StatusCreated, site)
}

func (s *Server) stageRelease(ctx context.Context, site Site, archive *zip.Reader) ([]string, error) {
	prefix := "releases/" + site.Name + "/" + site.Release + "/"
	var written []string

	for _, file := range archive.File {
		if file.FileInfo().IsDir() {
			continue
		}

		content, err := readArchiveFile(file)
		if err != nil {
			return written, err
		}

		key := prefix + file.Name
		if err := s.config.Sites.Put(ctx, key, bytes.NewReader(content)); err != nil {
			return written, fmt.Errorf("stage file %q: %w", file.Name, err)
		}
		written = append(written, key)
	}

	return written, nil
}

func (s *Server) removeStagedFiles(ctx context.Context, keys []string) {
	for _, key := range keys {
		if err := s.config.Sites.Delete(ctx, key); err != nil {
			slog.Error("remove staged file", "key", key, "error", err)
		}
	}
}

func (s *Server) publishFiles(ctx context.Context, site Site, files map[string]bool) error {
	prefix := "public/sites/" + site.Name + "/"
	previous, err := s.config.Sites.List(ctx, prefix)
	if err != nil {
		return fmt.Errorf("list published files: %w", err)
	}

	for _, object := range previous {
		if files[strings.TrimPrefix(object.Key, prefix)] {
			continue
		}

		if err := s.config.Sites.Delete(ctx, object.Key); err != nil {
			return fmt.Errorf("remove obsolete file %q: %w", object.Key, err)
		}
	}

	for key := range files {
		if key == "index.html" {
			continue
		}

		if err := s.copyPublishedFile(ctx, site, key); err != nil {
			return err
		}
	}

	return s.copyPublishedFile(ctx, site, "index.html")
}

func (s *Server) copyPublishedFile(ctx context.Context, site Site, key string) error {
	sourceKey := "releases/" + site.Name + "/" + site.Release + "/" + key
	source, err := s.config.Sites.Open(ctx, sourceKey)
	if err != nil {
		return fmt.Errorf("open staged file %q: %w", key, err)
	}
	defer closeReader(source, sourceKey)

	destinationKey := "public/sites/" + site.Name + "/" + key
	if err := s.config.Sites.Put(ctx, destinationKey, source); err != nil {
		return fmt.Errorf("publish file %q: %w", key, err)
	}

	return nil
}

func (s *Server) listSites(w http.ResponseWriter, r *http.Request) {
	objects, err := s.config.Sites.List(r.Context(), "sites/")
	if err != nil {
		writeServerError(w, err)
		return
	}

	sites := make([]Site, 0, len(objects))
	for _, object := range objects {
		site, err := s.readSite(r.Context(), object.Key)
		if errors.Is(err, ErrNotFound) {
			continue
		}
		if err != nil {
			writeServerError(w, err)
			return
		}
		sites = append(sites, site)
	}

	writeJSON(w, http.StatusOK, sites)
}

func (s *Server) readSite(ctx context.Context, key string) (Site, error) {
	reader, err := s.config.Sites.Open(ctx, key)
	if err != nil {
		return Site{}, fmt.Errorf("open site manifest %q: %w", key, err)
	}
	defer closeReader(reader, key)

	var site Site
	if err := json.NewDecoder(reader).Decode(&site); err != nil {
		return Site{}, fmt.Errorf("decode site manifest %q: %w", key, err)
	}

	return site, nil
}

func (s *Server) deleteSite(w http.ResponseWriter, r *http.Request) {
	if !validateIdentifiers(w, r) {
		return
	}

	s.siteWrites.Lock()
	defer s.siteWrites.Unlock()

	site := r.PathValue("site")
	objects, err := s.config.Sites.List(r.Context(), "public/sites/"+site+"/")
	if err != nil {
		writeServerError(w, err)
		return
	}

	for _, object := range objects {
		if err := s.config.Sites.Delete(r.Context(), object.Key); err != nil {
			writeServerError(w, err)
			return
		}
	}

	if err := s.config.Sites.Delete(r.Context(), "sites/"+site+".json"); err != nil {
		writeServerError(w, err)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

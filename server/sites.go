package hex

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"path"
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
	if !identifiers(w, r) {
		return
	}
	data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, s.config.MaxUploadBytes))
	if err != nil {
		fail(w, 413, "deployment exceeds upload limit")
		return
	}
	archive, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		fail(w, 400, "expected ZIP archive")
		return
	}
	if len(archive.File) > 5000 {
		fail(w, 400, "maximum 5000 entries")
		return
	}
	seen := map[string]bool{}
	var size uint64
	for _, file := range archive.File {
		if file.FileInfo().IsDir() {
			continue
		}
		if !validKey(file.Name) || !file.Mode().IsRegular() || seen[file.Name] {
			fail(w, 400, "invalid or duplicate archive path")
			return
		}
		if file.UncompressedSize64 > uint64(s.config.MaxUploadBytes)-size {
			fail(w, 413, "expanded deployment exceeds limit")
			return
		}
		size += file.UncompressedSize64
		seen[file.Name] = true
	}
	if !seen["index.html"] {
		fail(w, 400, "index.html is required at archive root")
		return
	}
	for key := range seen {
		for parent := path.Dir(key); parent != "."; parent = path.Dir(parent) {
			if seen[parent] {
				fail(w, 400, "archive path is both a file and directory")
				return
			}
		}
	}
	s.siteWrites.Lock()
	defer s.siteWrites.Unlock()
	site := Site{Name: r.PathValue("site"), Release: newID(), URL: "/sites/" + r.PathValue("site") + "/", PublishedAt: time.Now().UTC()}
	prefix := "releases/" + site.Name + "/" + site.Release + "/"
	written := []string{}
	committed := false
	defer func() {
		if !committed {
			for _, key := range written {
				s.config.Sites.Delete(r.Context(), key)
			}
		}
	}()
	for _, file := range archive.File {
		if file.FileInfo().IsDir() {
			continue
		}
		src, err := file.Open()
		if err != nil {
			fail(w, 400, "invalid ZIP entry")
			return
		}
		content, readErr := io.ReadAll(io.LimitReader(src, int64(file.UncompressedSize64)+1))
		src.Close()
		if readErr != nil || uint64(len(content)) != file.UncompressedSize64 {
			fail(w, 400, "corrupt ZIP entry")
			return
		}
		key := prefix + file.Name
		if err := s.config.Sites.Put(r.Context(), key, bytes.NewReader(content)); err != nil {
			serverError(w, err)
			return
		}
		written = append(written, key)
	}
	if err := s.publishFiles(r, site, seen); err != nil {
		serverError(w, err)
		return
	}
	manifest, _ := json.Marshal(site)
	if err := s.config.Sites.Put(r.Context(), "sites/"+site.Name+".json", bytes.NewReader(manifest)); err != nil {
		serverError(w, err)
		return
	}
	committed = true
	respond(w, 201, site)
}

func (s *Server) publishFiles(r *http.Request, site Site, files map[string]bool) error {
	prefix := "public/sites/" + site.Name + "/"
	previous, err := s.config.Sites.List(r.Context(), prefix)
	if err != nil {
		return err
	}
	for _, object := range previous {
		if !files[strings.TrimPrefix(object.Key, prefix)] {
			if err := s.config.Sites.Delete(r.Context(), object.Key); err != nil {
				return err
			}
		}
	}
	copyFile := func(key string) error {
		source, err := s.config.Sites.Open(r.Context(), "releases/"+site.Name+"/"+site.Release+"/"+key)
		if err != nil {
			return err
		}
		defer source.Close()
		return s.config.Sites.Put(r.Context(), prefix+key, source)
	}
	for key := range files {
		if key != "index.html" {
			if err := copyFile(key); err != nil {
				return err
			}
		}
	}
	return copyFile("index.html")
}

func (s *Server) listSites(w http.ResponseWriter, r *http.Request) {
	objects, err := s.config.Sites.List(r.Context(), "sites/")
	if err != nil {
		serverError(w, err)
		return
	}
	sites := []Site{}
	for _, obj := range objects {
		f, err := s.config.Sites.Open(r.Context(), obj.Key)
		if errors.Is(err, ErrNotFound) {
			continue
		}
		if err != nil {
			serverError(w, err)
			return
		}
		var site Site
		err = json.NewDecoder(f).Decode(&site)
		f.Close()
		if err != nil {
			serverError(w, err)
			return
		}
		sites = append(sites, site)
	}
	respond(w, 200, sites)
}

func (s *Server) deleteSite(w http.ResponseWriter, r *http.Request) {
	if !identifiers(w, r) {
		return
	}
	s.siteWrites.Lock()
	defer s.siteWrites.Unlock()
	objects, err := s.config.Sites.List(r.Context(), "public/sites/"+r.PathValue("site")+"/")
	if err != nil {
		serverError(w, err)
		return
	}
	for _, object := range objects {
		if err := s.config.Sites.Delete(r.Context(), object.Key); err != nil {
			serverError(w, err)
			return
		}
	}
	if err := s.config.Sites.Delete(r.Context(), "sites/"+r.PathValue("site")+".json"); err != nil {
		serverError(w, err)
		return
	}
	w.WriteHeader(204)
}

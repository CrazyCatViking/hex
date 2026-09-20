package hex

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
)

type Config struct {
	Files          ObjectStore
	Sites          ObjectStore
	Database       Database
	Realtime       Realtime
	MaxUploadBytes int64
}

type Server struct {
	config     Config
	mux        *http.ServeMux
	siteWrites sync.Mutex
}

var namePattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]{0,63}$`)

func New(config Config) *Server {
	if config.MaxUploadBytes <= 0 {
		config.MaxUploadBytes = 32 << 20
	}
	s := &Server{config: config, mux: http.NewServeMux()}
	s.mux.HandleFunc("GET /api/hex/capabilities", s.capabilities)
	if config.Files != nil {
		s.mux.HandleFunc("GET /api/sites/{site}/files", s.listFiles)
		s.mux.HandleFunc("PUT /api/sites/{site}/files/{key...}", s.putFile)
		s.mux.HandleFunc("GET /api/sites/{site}/files/{key...}", s.getFile)
		s.mux.HandleFunc("DELETE /api/sites/{site}/files/{key...}", s.deleteFile)
	}
	if config.Database != nil {
		s.mux.HandleFunc("GET /api/sites/{site}/db/{collection}", s.listDocuments)
		s.mux.HandleFunc("POST /api/sites/{site}/db/{collection}", s.createDocument)
		s.mux.HandleFunc("PUT /api/sites/{site}/db/{collection}/{id}", s.putDocument)
		s.mux.HandleFunc("GET /api/sites/{site}/db/{collection}/{id}", s.getDocument)
		s.mux.HandleFunc("DELETE /api/sites/{site}/db/{collection}/{id}", s.deleteDocument)
	}
	if config.Realtime != nil {
		s.mux.HandleFunc("GET /api/sites/{site}/realtime/{channel}", s.websocket)
	}
	if config.Sites != nil {
		s.mux.HandleFunc("GET /api/sites", s.listSites)
		s.mux.HandleFunc("POST /api/sites/{site}/deploy", s.deploy)
		s.mux.HandleFunc("DELETE /api/sites/{site}", s.deleteSite)
	}
	return s
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if strings.HasPrefix(r.URL.Path, "/api/") {
		w.Header().Set("Cache-Control", "no-store")
		if origin := r.Header.Get("Origin"); origin != "" {
			u, err := url.Parse(origin)
			if err != nil || u.Host != r.Host || (u.Scheme != "http" && u.Scheme != "https") {
				fail(w, http.StatusForbidden, "cross-origin request rejected")
				return
			}
		}
		if r.Header.Get("Sec-Fetch-Site") == "cross-site" {
			fail(w, http.StatusForbidden, "cross-site request rejected")
			return
		}
		if r.Method != "GET" && r.Method != "HEAD" && r.Header.Get("X-Hex-Request") != "1" {
			fail(w, http.StatusForbidden, "X-Hex-Request header required")
			return
		}
	}
	s.mux.ServeHTTP(w, r)
}

func (s *Server) capabilities(w http.ResponseWriter, r *http.Request) {
	respond(w, 200, map[string]any{"version": 1, "files": s.config.Files != nil, "database": s.config.Database != nil, "realtime": s.config.Realtime != nil, "sites": s.config.Sites != nil, "maxUploadBytes": s.config.MaxUploadBytes})
}

func identifiers(w http.ResponseWriter, r *http.Request) bool {
	for _, key := range []string{"site", "collection", "id", "channel"} {
		if value := r.PathValue(key); value != "" && !namePattern.MatchString(value) {
			fail(w, 400, "invalid "+key)
			return false
		}
	}
	return true
}

func validKey(key string) bool {
	if key == "." || !fs.ValidPath(key) || strings.ContainsAny(key, "\\\x00") {
		return false
	}
	for _, part := range strings.Split(key, "/") {
		if strings.HasPrefix(part, ".") {
			return false
		}
	}
	return true
}

func fileKey(w http.ResponseWriter, r *http.Request) (string, bool) {
	if !identifiers(w, r) {
		return "", false
	}
	key := r.PathValue("key")
	if !validKey(key) {
		fail(w, 400, "invalid file key")
		return "", false
	}
	return r.PathValue("site") + "/" + key, true
}

func (s *Server) listFiles(w http.ResponseWriter, r *http.Request) {
	if !identifiers(w, r) {
		return
	}
	prefix := r.PathValue("site") + "/"
	objects, err := s.config.Files.List(r.Context(), prefix)
	if err != nil {
		serverError(w, err)
		return
	}
	for i := range objects {
		objects[i].Key = strings.TrimPrefix(objects[i].Key, prefix)
	}
	respond(w, 200, objects)
}

func (s *Server) putFile(w http.ResponseWriter, r *http.Request) {
	key, ok := fileKey(w, r)
	if !ok {
		return
	}
	data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, s.config.MaxUploadBytes))
	if err != nil {
		fail(w, 413, "upload exceeds limit or could not be read")
		return
	}
	if err := s.config.Files.Put(r.Context(), key, bytes.NewReader(data)); err != nil {
		serverError(w, err)
		return
	}
	respond(w, 200, Object{Key: r.PathValue("key"), Size: int64(len(data))})
}

func (s *Server) getFile(w http.ResponseWriter, r *http.Request) {
	key, ok := fileKey(w, r)
	if !ok {
		return
	}
	f, err := s.config.Files.Open(r.Context(), key)
	if err != nil {
		serverError(w, err)
		return
	}
	defer f.Close()
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", "attachment")
	io.Copy(w, f)
}

func (s *Server) deleteFile(w http.ResponseWriter, r *http.Request) {
	key, ok := fileKey(w, r)
	if !ok {
		return
	}
	if err := s.config.Files.Delete(r.Context(), key); err != nil {
		serverError(w, err)
		return
	}
	w.WriteHeader(204)
}

func (s *Server) listDocuments(w http.ResponseWriter, r *http.Request) {
	if !identifiers(w, r) {
		return
	}
	limit := 100
	if v := r.URL.Query().Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 100 {
			fail(w, 400, "limit must be between 1 and 100")
			return
		}
		limit = n
	}
	docs, err := s.config.Database.List(r.Context(), r.PathValue("site"), r.PathValue("collection"), r.URL.Query().Get("after"), limit)
	if err != nil {
		serverError(w, err)
		return
	}
	respond(w, 200, docs)
}

func (s *Server) createDocument(w http.ResponseWriter, r *http.Request) {
	r.SetPathValue("id", newID())
	s.writeDocument(w, r, 201)
}

func (s *Server) putDocument(w http.ResponseWriter, r *http.Request) { s.writeDocument(w, r, 200) }

func (s *Server) writeDocument(w http.ResponseWriter, r *http.Request, status int) {
	if !identifiers(w, r) {
		return
	}
	data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
	var object map[string]json.RawMessage
	if err != nil || json.Unmarshal(data, &object) != nil || object == nil {
		fail(w, 400, "expected a JSON object of at most 1 MiB")
		return
	}
	if err := s.config.Database.Put(r.Context(), r.PathValue("site"), r.PathValue("collection"), r.PathValue("id"), data); err != nil {
		serverError(w, err)
		return
	}
	respond(w, status, Document{ID: r.PathValue("id"), Data: data})
}

func (s *Server) getDocument(w http.ResponseWriter, r *http.Request) {
	if !identifiers(w, r) {
		return
	}
	doc, err := s.config.Database.Get(r.Context(), r.PathValue("site"), r.PathValue("collection"), r.PathValue("id"))
	if err != nil {
		serverError(w, err)
		return
	}
	respond(w, 200, doc)
}

func (s *Server) deleteDocument(w http.ResponseWriter, r *http.Request) {
	if !identifiers(w, r) {
		return
	}
	if err := s.config.Database.Delete(r.Context(), r.PathValue("site"), r.PathValue("collection"), r.PathValue("id")); err != nil {
		serverError(w, err)
		return
	}
	w.WriteHeader(204)
}

func newID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b[:])
}
func respond(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(value)
}
func fail(w http.ResponseWriter, status int, message string) {
	respond(w, status, map[string]string{"error": message})
}
func serverError(w http.ResponseWriter, err error) {
	if errors.Is(err, ErrNotFound) {
		fail(w, 404, "not found")
		return
	}
	slog.Error("request failed", "error", fmt.Sprint(err))
	fail(w, 500, "internal server error")
}

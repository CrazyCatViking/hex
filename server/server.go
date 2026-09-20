package hex

import (
	"net/http"
	"net/url"
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

func New(config Config) *Server {
	if config.MaxUploadBytes <= 0 {
		config.MaxUploadBytes = 32 << 20
	}

	server := &Server{
		config: config,
		mux:    http.NewServeMux(),
	}
	server.registerRoutes()

	return server
}

func (s *Server) registerRoutes() {
	s.mux.HandleFunc("GET /api/hex/capabilities", s.capabilities)

	if s.config.Files != nil {
		s.mux.HandleFunc("GET /api/sites/{site}/files", s.listFiles)
		s.mux.HandleFunc("PUT /api/sites/{site}/files/{key...}", s.putFile)
		s.mux.HandleFunc("GET /api/sites/{site}/files/{key...}", s.getFile)
		s.mux.HandleFunc("DELETE /api/sites/{site}/files/{key...}", s.deleteFile)
	}

	if s.config.Database != nil {
		s.mux.HandleFunc("GET /api/sites/{site}/db/{collection}", s.listDocuments)
		s.mux.HandleFunc("POST /api/sites/{site}/db/{collection}", s.createDocument)
		s.mux.HandleFunc("PUT /api/sites/{site}/db/{collection}/{id}", s.putDocument)
		s.mux.HandleFunc("GET /api/sites/{site}/db/{collection}/{id}", s.getDocument)
		s.mux.HandleFunc("DELETE /api/sites/{site}/db/{collection}/{id}", s.deleteDocument)
	}

	if s.config.Realtime != nil {
		s.mux.HandleFunc("GET /api/sites/{site}/realtime/{channel}", s.websocket)
	}

	if s.config.Sites != nil {
		s.mux.HandleFunc("GET /api/sites", s.listSites)
		s.mux.HandleFunc("POST /api/sites/{site}/deploy", s.deploy)
		s.mux.HandleFunc("DELETE /api/sites/{site}", s.deleteSite)
	}
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("X-Content-Type-Options", "nosniff")

	if strings.HasPrefix(r.URL.Path, "/api/") {
		w.Header().Set("Cache-Control", "no-store")
		if !validateRequestOrigin(w, r) {
			return
		}
	}

	s.mux.ServeHTTP(w, r)
}

func validateRequestOrigin(w http.ResponseWriter, r *http.Request) bool {
	if origin := r.Header.Get("Origin"); origin != "" {
		parsedOrigin, err := url.Parse(origin)
		if err != nil || parsedOrigin.Host != r.Host {
			writeError(w, http.StatusForbidden, "cross-origin request rejected")
			return false
		}

		if parsedOrigin.Scheme != "http" && parsedOrigin.Scheme != "https" {
			writeError(w, http.StatusForbidden, "cross-origin request rejected")
			return false
		}
	}

	if r.Header.Get("Sec-Fetch-Site") == "cross-site" {
		writeError(w, http.StatusForbidden, "cross-site request rejected")
		return false
	}

	isRead := r.Method == http.MethodGet || r.Method == http.MethodHead
	if !isRead && r.Header.Get("X-Hex-Request") != "1" {
		writeError(w, http.StatusForbidden, "X-Hex-Request header required")
		return false
	}

	return true
}

func (s *Server) capabilities(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"version":        1,
		"files":          s.config.Files != nil,
		"database":       s.config.Database != nil,
		"realtime":       s.config.Realtime != nil,
		"sites":          s.config.Sites != nil,
		"maxUploadBytes": s.config.MaxUploadBytes,
	})
}

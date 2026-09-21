package hex

import (
	"net/http"
	"net/url"
	"strings"
)

type Config struct {
	Files          ObjectStore
	Sites          SiteDirectory
	SiteBaseURL    string
	Database       Database
	Realtime       Realtime
	Identity       IdentityResolver
	Access         AccessStore
	AdminGroups    []string
	MaxUploadBytes int64
	Connection     *ConnectionConfig
	CLIReleaseURL  string
}

type Server struct {
	config Config
	mux    *http.ServeMux
}

func New(config Config) *Server {
	if config.SiteBaseURL == "" {
		config.SiteBaseURL = "http://localhost:8080"
	}
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
	s.mux.HandleFunc("GET /{$}", s.landingPage)
	s.mux.HandleFunc("GET /api/hex/overview", s.overview)
	s.mux.HandleFunc("GET /api/hex/catalog", s.catalog)
	s.mux.HandleFunc("GET /api/hex/htmx.min.js", s.portalAsset)
	s.mux.HandleFunc("GET /api/hex/portal.css", s.portalAsset)
	s.mux.HandleFunc("GET /api/hex/portal.js", s.portalAsset)
	s.mux.HandleFunc("GET /api/hex/capabilities", s.capabilities)
	s.mux.HandleFunc("GET /api/hex/authz", s.staticAuthz)
	if s.config.Connection != nil {
		s.mux.HandleFunc("GET /api/hex/config", s.connectionConfig)
		s.mux.HandleFunc("GET /api/hex/install/{os}", s.installer)
	}

	if s.config.Identity != nil {
		s.mux.HandleFunc("GET /api/hex/me", s.me)
	}

	if s.config.Identity != nil && s.config.Access != nil {
		s.mux.HandleFunc("GET /api/hex/sites/{site}/access", s.getSiteAccess)
		s.mux.HandleFunc("PUT /api/hex/sites/{site}/access", s.putSiteAccess)
		s.mux.HandleFunc("DELETE /api/hex/sites/{site}/access", s.deleteSiteAccess)
	}

	if s.config.Files != nil {
		s.mux.HandleFunc("GET /api/sites/{site}/files", s.siteScoped(s.listFiles))
		s.mux.HandleFunc("PUT /api/sites/{site}/files/{key...}", s.siteScoped(s.putFile))
		s.mux.HandleFunc("GET /api/sites/{site}/files/{key...}", s.siteScoped(s.getFile))
		s.mux.HandleFunc("DELETE /api/sites/{site}/files/{key...}", s.siteScoped(s.deleteFile))
	}

	if s.config.Database != nil {
		s.mux.HandleFunc("GET /api/sites/{site}/db/{collection}", s.siteScoped(s.listDocuments))
		s.mux.HandleFunc("POST /api/sites/{site}/db/{collection}", s.siteScoped(s.createDocument))
		s.mux.HandleFunc("PUT /api/sites/{site}/db/{collection}/{id}", s.siteScoped(s.putDocument))
		s.mux.HandleFunc("GET /api/sites/{site}/db/{collection}/{id}", s.siteScoped(s.getDocument))
		s.mux.HandleFunc("DELETE /api/sites/{site}/db/{collection}/{id}", s.siteScoped(s.deleteDocument))
	}

	if s.config.Realtime != nil {
		s.mux.HandleFunc("GET /api/sites/{site}/realtime/{channel}", s.siteScoped(s.websocket))
	}

	if s.config.Sites != nil {
		s.mux.HandleFunc("GET /api/sites", s.listSites)
	}
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("X-Content-Type-Options", "nosniff")

	if strings.HasPrefix(r.URL.Path, "/api/") {
		w.Header().Set("Cache-Control", "no-store")
		if !platformDownloadNavigation(r) && !authorizationSubrequest(r) && !validateRequestOrigin(w, r) {
			return
		}
	}

	s.mux.ServeHTTP(w, r)
}

// authorizationSubrequest recognizes NGINX auth_request subrequests for
// static assets. They inherit the original request's headers, so a visitor
// navigating in from another origin carries cross-site Fetch Metadata that
// the API's origin validation would otherwise reject. The endpoint is
// read-only and answers with a status code alone, so exempting it is safe.
func authorizationSubrequest(r *http.Request) bool {
	isRead := r.Method == http.MethodGet || r.Method == http.MethodHead
	return isRead && r.URL.Path == "/api/hex/authz"
}

func platformDownloadNavigation(r *http.Request) bool {
	isRead := r.Method == http.MethodGet || r.Method == http.MethodHead
	isDownload := r.URL.Path == "/api/hex/config" ||
		r.URL.Path == "/api/hex/install/macos" ||
		r.URL.Path == "/api/hex/install/linux" ||
		r.URL.Path == "/api/hex/install/windows"
	return isRead && isDownload &&
		r.Header.Get("Sec-Fetch-Mode") == "navigate" &&
		r.Header.Get("Sec-Fetch-Dest") == "document"
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
	writeJSON(w, http.StatusOK, s.capabilityDescription())
}

func (s *Server) capabilityDescription() map[string]any {
	return map[string]any{
		"version":        1,
		"files":          s.config.Files != nil,
		"database":       s.config.Database != nil,
		"realtime":       s.config.Realtime != nil,
		"sites":          s.config.Sites != nil,
		"identity":       s.config.Identity != nil,
		"accessControl":  s.config.Identity != nil && s.config.Access != nil,
		"maxUploadBytes": s.config.MaxUploadBytes,
	}
}

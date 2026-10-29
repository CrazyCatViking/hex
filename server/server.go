package hex

import (
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type Config struct {
	Analytics AnalyticsStore
	// TrafficCollector is optional operational status for the admin dashboard.
	TrafficCollector *TrafficCollector
	Files            ObjectStore
	Sites            SiteDirectory
	SiteBaseURL      string
	// ResolveOrigin optionally classifies trusted hosting origins. Unknown hosts
	// must return an error; an empty Site explicitly denotes a platform origin.
	ResolveOrigin func(*http.Request) (RequestOrigin, error)
	// PathCaseInsensitive must match the static host's path comparison.
	PathCaseInsensitive bool
	// LogoutURL is the host-owned sign-out destination; empty hides sign-out.
	LogoutURL   string
	Database    Database
	Realtime    Realtime
	Identity    IdentityResolver
	Access      AccessStore
	AdminGroups []string
	// AdminAccess stores explicit, audited, expiring privileges. When omitted,
	// an implementing Access store or Database is used automatically.
	AdminAccess AdminAccessStore
	// Actions exposes contract-validated operations implemented by app backends.
	Actions *ActionRegistry
	// Publisher enables publishing through the API. PublisherGroups limits
	// who may claim new site names; empty lets every signed-in user.
	Publisher       SitePublisher
	PublisherGroups []string
	// People remembers who has signed in, and Groups names the groups
	// offered in pickers; together they label principals without reading
	// the identity provider's directory.
	People              PeopleStore
	Groups              []NamedGroup
	MaxUploadBytes      int64
	MaxPublishFileBytes int64
	MaxPublishBytes     int64
	Connection          *ConnectionConfig
	CLIReleaseURL       string

	// Integrations are the platform's third-party systems, called by apps,
	// AI tool use and automations. IntegrationGrants gives principals their
	// endpoint permissions. IntegrationStore keeps site approvals and
	// connected accounts, whose tokens CredentialSealer encrypts; a 32-byte
	// CredentialKey configures a local KeySealer instead. Connections
	// unused for ConnectionIdleExpiry (90 days by default) are removed.
	Integrations         *IntegrationRegistry
	IntegrationGrants    []IntegrationGrant
	IntegrationStore     IntegrationStore
	CredentialSealer     CredentialSealer
	CredentialKey        []byte
	ConnectionIdleExpiry time.Duration
	// IntegrationAudit keeps the audit log of integrations with Audit set,
	// for IntegrationAuditRetention (365 days by default).
	IntegrationAudit          IntegrationAuditStore
	IntegrationAuditRetention time.Duration

	// AI enables streaming model access for apps and automations. AIUsage
	// records each model call's usage and cost and keeps the monthly AI
	// budgets that are managed in the portal.
	AI      *AIConfig
	AIUsage AIUsageStore
	// Automations stores declared automations and their runs; the host
	// runs the scheduler with Server.RunBackground.
	Automations AutomationStore
}

type Server struct {
	config                 Config
	mux                    *http.ServeMux
	seen                   peopleSeen
	analyticsFailures      atomic.Uint64
	analyticsImportPending atomic.Bool
	integrationCache       responseCache
	portalInventory        portalInventoryCache
	portalIcons            portalIconCache
	automationRuns         sync.WaitGroup
	automationSlots        chan struct{}
}

func New(config Config) *Server {
	if config.AdminAccess == nil {
		config.AdminAccess, _ = config.Access.(AdminAccessStore)
		if config.AdminAccess == nil {
			config.AdminAccess, _ = config.Database.(AdminAccessStore)
		}
	}
	if config.SiteBaseURL == "" {
		config.SiteBaseURL = "http://localhost:8080"
	}
	if config.MaxUploadBytes <= 0 {
		config.MaxUploadBytes = 32 << 20
	}
	if config.MaxPublishFileBytes <= 0 {
		config.MaxPublishFileBytes = 256 << 20
	}
	if config.MaxPublishBytes <= 0 {
		config.MaxPublishBytes = 2 << 30
	}
	if config.IntegrationAuditRetention <= 0 {
		config.IntegrationAuditRetention = defaultIntegrationAuditRetention
	}

	if config.CredentialSealer == nil && len(config.CredentialKey) > 0 {
		sealer, err := NewKeySealer(config.CredentialKey)
		if err != nil {
			slog.Error("connected accounts disabled: invalid credential key", "error", err)
		} else {
			config.CredentialSealer = sealer
		}
	}
	server := &Server{
		config:          config,
		mux:             http.NewServeMux(),
		seen:            peopleSeen{entries: make(map[string]seenPerson)},
		automationSlots: make(chan struct{}, maxConcurrentRuns),
	}
	server.analyticsImportPending.Store(config.Analytics != nil && config.Sites != nil)
	server.registerRoutes()
	server.warnAboutUnauditedIntegrations()

	return server
}

func (s *Server) registerRoutes() {
	s.mux.HandleFunc("GET /{$}", s.landingPage)
	s.mux.HandleFunc("GET /start", s.startPage)
	s.mux.HandleFunc("GET /library", s.libraryPage)
	s.mux.HandleFunc("PUT /api/hex/preferences/pins/{site}", s.updatePin)
	s.mux.HandleFunc("POST /api/hex/preferences/recent/{site}", s.recordRecent)
	s.mux.HandleFunc("GET /api/hex/favicon.svg", s.portalAsset)
	if s.siteIconReader() != nil {
		s.mux.HandleFunc("GET /api/hex/sites/{site}/icon", s.siteIcon)
	}
	s.mux.HandleFunc("GET /api/hex/overview", s.overview)
	s.mux.HandleFunc("GET /api/hex/catalog", s.catalog)
	s.mux.HandleFunc("GET /api/hex/htmx.min.js", s.portalAsset)
	s.mux.HandleFunc("GET /api/hex/portal.css", s.portalAsset)
	s.mux.HandleFunc("GET /api/hex/portal.js", s.portalAsset)
	s.mux.HandleFunc("GET /api/hex/manage.js", s.portalAsset)
	s.mux.HandleFunc("GET /api/hex/permissions.js", s.portalAsset)
	s.mux.HandleFunc("GET /api/hex/permission-matrix.js", s.portalAsset)
	s.mux.HandleFunc("GET /api/hex/permissions.css", s.portalAsset)
	s.mux.HandleFunc("GET /api/hex/capabilities", s.capabilities)
	s.mux.HandleFunc("GET /api/hex/authz", s.staticAuthz)
	if s.config.Connection != nil {
		s.mux.HandleFunc("GET /api/hex/config", s.connectionConfig)
		s.mux.HandleFunc("GET /api/hex/install/{os}", s.installer)
	}

	if s.config.Identity != nil {
		s.mux.HandleFunc("GET /api/hex/me", s.me)
		s.mux.HandleFunc("GET /api/hex/directory", s.directory)
	}

	if s.config.Identity != nil && s.config.Access != nil {
		s.mux.HandleFunc("GET /api/hex/sites/{site}/access", s.getSiteAccess)
		s.mux.HandleFunc("PUT /api/hex/sites/{site}/access", s.putSiteAccess)
		s.mux.HandleFunc("POST /api/hex/sites/{site}/access/baseline", s.reconcileAccess)
		s.mux.HandleFunc("DELETE /api/hex/sites/{site}/access", s.deleteSiteAccess)
	}
	s.mux.HandleFunc("GET /api/hex/sites/{site}/permissions", s.permissions)

	if s.config.Publisher != nil {
		s.mux.HandleFunc("POST /api/hex/sites/{site}/publish", s.startPublish)
		s.mux.HandleFunc("PUT /api/hex/sites/{site}/publish/files/{path...}", s.uploadSiteFile)
		s.mux.HandleFunc("POST /api/hex/sites/{site}/publish/complete", s.completePublish)
		s.mux.HandleFunc("DELETE /api/hex/sites/{site}", s.unpublishSite)
		s.mux.HandleFunc("GET /api/hex/sites/{site}/history", s.siteHistory)
	}

	if s.artifactsEnabled() {
		s.mux.HandleFunc("POST /api/hex/artifacts", s.createArtifact)
	}

	if s.config.Files != nil {
		s.mux.HandleFunc("GET /api/sites/{site}/files", s.siteScoped(s.listFiles))
		s.mux.HandleFunc("PUT /api/sites/{site}/files/{key...}", s.siteScoped(s.putFile))
		s.mux.HandleFunc("GET /api/sites/{site}/files/{key...}", s.siteScoped(s.getFile))
		s.mux.HandleFunc("DELETE /api/sites/{site}/files/{key...}", s.siteScoped(s.deleteFile))
	}

	if s.config.Database != nil {
		s.mux.HandleFunc("POST /api/sites/{site}/permissions/check", s.siteScoped(s.checkPermission))
		s.mux.HandleFunc("GET /api/sites/{site}/db/{collection}", s.siteScoped(s.listDocuments))
		s.mux.HandleFunc("POST /api/sites/{site}/db/{collection}", s.siteScoped(s.createDocument))
		s.mux.HandleFunc("PUT /api/sites/{site}/db/{collection}/{id}", s.siteScoped(s.putDocument))
		s.mux.HandleFunc("GET /api/sites/{site}/db/{collection}/{id}", s.siteScoped(s.getDocument))
		s.mux.HandleFunc("DELETE /api/sites/{site}/db/{collection}/{id}", s.siteScoped(s.deleteDocument))
	}
	if s.actionsEnabled() {
		if _, ok := s.config.Database.(GuardedDatabase); ok {
			s.mux.HandleFunc("POST /api/sites/{site}/site-api/call", s.siteScoped(s.siteCallHTTP))
			s.mux.HandleFunc("GET /api/sites/{site}/site-api/identity", s.siteScoped(s.siteIdentityHTTP))
			s.mux.HandleFunc("GET /api/sites/{site}/site-api/actions/{target}", s.siteScoped(s.siteActionsHTTP))
			s.mux.HandleFunc("GET /api/sites/{site}/site-api/invocations/{id}", s.siteScoped(s.siteInvocationHTTP))
		}
		s.mux.HandleFunc("GET /api/sites/{site}/actions", s.siteScoped(s.listActions))
		s.mux.HandleFunc("GET /api/sites/{site}/actions/{action}", s.siteScoped(s.describeAction))
		s.mux.HandleFunc("POST /api/sites/{site}/actions/{action}", s.siteScoped(s.runAction))
	}

	if s.config.Realtime != nil {
		s.mux.HandleFunc("GET /api/sites/{site}/realtime/{channel}", s.siteScoped(s.websocket))
	}

	if s.config.Sites != nil {
		s.mux.HandleFunc("GET /api/sites", s.listSites)
	}

	s.registerIntegrationRoutes()
	s.registerConnectionRoutes()
	s.registerPortalIntegrationRoutes()
	s.registerAIRoutes()
	s.registerAutomationRoutes()
	s.registerEventRoutes()
	s.registerWorkflowRoutes()
	s.registerManageRoutes()
	s.registerAdminAccessRoutes()
	s.registerPortalConnectionRoutes()
	s.registerAIPortalRoutes()
	s.registerIntegrationAuditRoutes()
	s.registerAnalyticsRoutes()
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if s.config.ResolveOrigin != nil {
		origin, err := s.config.ResolveOrigin(r)
		if err != nil || (origin.Site != "" && !siteNamePattern.MatchString(origin.Site)) {
			writeError(w, http.StatusForbidden, "unknown hosting origin")
			return
		}
		if origin.Site != "" && !strings.HasPrefix(r.URL.Path, "/api/") {
			writeError(w, http.StatusForbidden, "platform pages are not available from an app hostname")
			return
		}
	}
	if s.config.Analytics != nil && strings.HasPrefix(r.URL.Path, "/api/sites/") {
		s.analyticsIdentity(w, s.requestIdentity(r))
	}

	if strings.HasPrefix(r.URL.Path, "/api/") {
		w.Header().Set("Cache-Control", "no-store")
		if !s.allowAppAPI(w, r) {
			return
		}
		exempt := platformDownloadNavigation(r) || authorizationSubrequest(r) || connectionCallback(r)
		if !exempt && !validateRequestOrigin(w, r) {
			return
		}
	}

	s.mux.ServeHTTP(w, r)
}

// App origins may use only their own data namespace and the small set of
// platform endpoints needed by the browser client. In particular, an app must
// not inherit its visitor's authority to manage or read other sites. Non-site
// API hostnames remain available for the platform and CLI (including hosts
// configured separately from SiteBaseURL).
func (s *Server) allowAppAPI(w http.ResponseWriter, r *http.Request) bool {
	if s.config.ResolveOrigin != nil {
		origin, err := s.config.ResolveOrigin(r)
		if err != nil {
			writeError(w, http.StatusForbidden, "unknown hosting origin")
			return false
		}
		if origin.Site == "" {
			return true
		}
		return allowSiteAPI(w, r, origin.Site)
	}
	base, err := parseSiteBaseURL(s.config.SiteBaseURL)
	if err != nil {
		writeServerError(w, err)
		return false
	}
	host := strings.ToLower(strings.TrimSuffix((&url.URL{Host: r.Host}).Hostname(), "."))
	domain := strings.ToLower(strings.TrimSuffix(base.Hostname(), "."))
	suffix := "." + domain
	if !strings.HasSuffix(host, suffix) {
		return true
	}
	site := strings.TrimSuffix(host, suffix)
	return allowSiteAPI(w, r, site)
}

// RequestOrigin is supplied by the host, never by public routing headers.
type RequestOrigin struct {
	Site string
}

func allowSiteAPI(w http.ResponseWriter, r *http.Request, site string) bool {
	path := r.URL.Path
	isRead := r.Method == http.MethodGet || r.Method == http.MethodHead
	allowed := siteNamePattern.MatchString(site) && (strings.HasPrefix(path, "/api/sites/"+site+"/") ||
		isRead && (path == "/api/hex/me" || path == "/api/hex/capabilities" ||
			path == "/api/hex/authz" || path == "/api/hex/sites/"+site+"/permissions"))
	if !allowed {
		writeError(w, http.StatusForbidden, "this API is not available from an app hostname")
	}
	return allowed
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

// artifactsEnabled reports whether files and folders can be published
// without hex.json: such artifacts are private to
// their creator, which needs identities, access policies and publishing.
func (s *Server) artifactsEnabled() bool {
	return s.config.Identity != nil && s.config.Access != nil && s.config.Publisher != nil
}

func (s *Server) capabilities(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.capabilityDescription())
}

func (s *Server) capabilityDescription() map[string]any {
	return map[string]any{
		"version":           1,
		"files":             s.config.Files != nil,
		"database":          s.config.Database != nil,
		"realtime":          s.config.Realtime != nil,
		"sites":             s.config.Sites != nil,
		"identity":          s.config.Identity != nil,
		"accessControl":     s.config.Identity != nil && s.config.Access != nil,
		"permissionVersion": 2,
		"publishing":        s.config.Publisher != nil,
		"artifacts":         s.artifactsEnabled(),
		"actions":           s.actionsEnabled(),
		"analytics":         s.config.Analytics != nil,
		"integrations":      s.integrationsEnabled(),
		"connections":       s.connectionsEnabled(),
		"ai":                s.aiEnabled(),
		"automations":       s.automationsEnabled(),
		"siteAPI":           s.siteAPIEnabled(),
		"durableEvents":     s.eventsEnabled(),
		"maxUploadBytes":    s.config.MaxUploadBytes,
	}
}

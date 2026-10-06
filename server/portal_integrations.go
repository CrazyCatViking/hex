package hex

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strings"
	"time"
)

type integrationsPageView struct {
	Chrome
	Catalog   siteIntegrationsView
	Approvals *portalIntegrationApprovalsView
}

// siteIntegrationsView is also rendered by the site-integrations template,
// either directly on a site tab or through the management fragment route.
type siteIntegrationsView struct {
	Site             string
	Integrations     []portalIntegrationCard
	ApprovalsEnabled bool
	Admin            bool
}

type portalIntegrationCard struct {
	Name             string
	Title            string
	Description      string
	RequiresApproval bool
	Audit            bool
	AuditUnavailable bool
	Connector        string
	Connection       *connectionStatus
	Endpoints        []IntegrationEndpointInfo
	Approval         *portalIntegrationApproval
	ApprovalURL      string
	CanRequest       bool
	CanApprove       bool
	CanRemove        bool
	Admin            bool
}

type portalIntegrationApproval struct {
	Site        string
	Integration string
	Title       string
	Status      string
	Reason      string
	RequestedBy string
	RequestedAt string
	DecidedBy   string
	DecidedAt   string
	ApprovalURL string
}

type portalIntegrationApprovalsView struct {
	Approvals []portalIntegrationApproval
}

func (s *Server) portalIntegrationsEnabled() bool {
	return s.config.Identity != nil && s.integrationsEnabled()
}

func (s *Server) registerPortalIntegrationRoutes() {
	if !s.portalIntegrationsEnabled() {
		return
	}
	s.mux.HandleFunc("GET /integrations", s.integrationsPage)
	s.mux.HandleFunc("GET /api/hex/manage/sites/{site}/integrations", s.manageSiteIntegrations)
	if !s.integrationApprovalsEnabled() {
		return
	}
	s.mux.HandleFunc("GET /api/hex/manage/integration-approvals", s.manageIntegrationApprovals)
	s.mux.HandleFunc("POST /api/hex/manage/sites/{site}/integrations/{integration}/approval/request", s.manageRequestIntegrationApproval)
	s.mux.HandleFunc("POST /api/hex/manage/sites/{site}/integrations/{integration}/approval/approve", s.manageApproveIntegration)
	s.mux.HandleFunc("POST /api/hex/manage/sites/{site}/integrations/{integration}/approval/revoke", s.manageRevokeIntegrationApproval)
}

func (s *Server) integrationsPage(w http.ResponseWriter, r *http.Request) {
	if !s.platformHost(r.Host) {
		http.NotFound(w, r)
		return
	}
	identity := s.requestIdentity(r)
	if identity == nil {
		writeError(w, http.StatusUnauthorized, "not authenticated")
		return
	}
	site := r.URL.Query().Get("site")
	if _, selected := r.URL.Query()["site"]; selected {
		r.SetPathValue("site", site)
		var ok bool
		identity, site, _, _, ok = s.manageCaller(w, r)
		if !ok {
			return
		}
	}
	catalog, err := s.siteIntegrationPortalView(r.Context(), identity, site)
	if err != nil {
		writeServerError(w, err)
		return
	}
	view := integrationsPageView{Chrome: s.chromeFor(identity, "integrations"), Catalog: catalog}
	if site == "" && s.isAdmin(identity) && s.integrationApprovalsEnabled() {
		approvals, err := s.integrationApprovalPortalView(r.Context())
		if err != nil {
			writeServerError(w, err)
			return
		}
		view.Approvals = &approvals
	}
	s.writePortalPage(w, "integrations", view)
}

func (s *Server) manageSiteIntegrations(w http.ResponseWriter, r *http.Request) {
	identity, site, _, _, ok := s.manageCaller(w, r)
	if !ok {
		return
	}
	view, err := s.siteIntegrationPortalView(r.Context(), identity, site)
	if err != nil {
		writeServerError(w, err)
		return
	}
	s.renderFragment(w, "site-integrations", view)
}

func (s *Server) siteIntegrationPortalView(ctx context.Context, identity *Identity, site string) (siteIntegrationsView, error) {
	view := siteIntegrationsView{Site: site, ApprovalsEnabled: s.integrationApprovalsEnabled(), Admin: s.isAdmin(identity)}
	caller := integrationCaller{site: site, identity: identity, role: roleOwner}
	for _, registered := range s.config.Integrations.all() {
		integration := registered.integration
		card := portalIntegrationCard{
			Name: integration.Name, Title: integration.Title, Description: integration.Description,
			RequiresApproval: integration.RequiresApproval, Audit: integration.Audit,
			AuditUnavailable: integration.Audit && s.config.IntegrationAudit == nil, Admin: view.Admin,
		}
		approved := !integration.RequiresApproval
		if site != "" && integration.RequiresApproval && view.ApprovalsEnabled {
			card.ApprovalURL = integrationApprovalPortalURL(site, integration.Name)
			approval, err := s.config.IntegrationStore.GetIntegrationApproval(ctx, site, integration.Name)
			if err != nil && !errors.Is(err, ErrNotFound) {
				return view, err
			}
			if err == nil {
				row := integrationApprovalPortalRow(approval, integration.Title)
				card.Approval = &row
			}
			approved = err == nil && approval.Status == ApprovalApproved
			card.CanRequest = !approved && (!view.Admin || errors.Is(err, ErrNotFound))
			card.CanApprove = view.Admin && !approved && err == nil
			card.CanRemove = err == nil && (view.Admin || approval.Status == ApprovalRequested)
		}
		if integration.Connector != "" {
			connector, _ := s.config.Integrations.connector(integration.Connector)
			card.Connector = connector.Title
			if site != "" && s.connectionsEnabled() {
				status, err := s.connectionStatus(ctx, identity, connector)
				if err != nil {
					return view, err
				}
				card.Connection = &status
			}
		}
		for _, endpoint := range registered.sortedEndpoints() {
			allowed := s.granted(caller, endpoint.permission())
			if site != "" {
				allowed = approved && s.integrationAccess(caller, endpoint) == nil && !card.AuditUnavailable
				if integration.Connector != "" {
					allowed = allowed && card.Connection != nil && card.Connection.Connected
				}
			}
			card.Endpoints = append(card.Endpoints, endpointInfo(endpoint, allowed))
		}
		view.Integrations = append(view.Integrations, card)
	}
	return view, nil
}

func integrationApprovalPortalURL(site, integration string) string {
	return "/api/hex/manage/sites/" + site + "/integrations/" + integration + "/approval"
}

func integrationApprovalPortalRow(approval IntegrationApproval, title string) portalIntegrationApproval {
	row := portalIntegrationApproval{
		Site: approval.Site, Integration: approval.Integration, Title: title,
		Status: approval.Status, Reason: approval.Reason,
		RequestedBy: personName(approval.RequestedBy), DecidedBy: personName(approval.DecidedBy),
		ApprovalURL: integrationApprovalPortalURL(approval.Site, approval.Integration),
	}
	if !approval.RequestedAt.IsZero() {
		row.RequestedAt = approval.RequestedAt.UTC().Format(time.RFC3339)
	}
	if !approval.DecidedAt.IsZero() {
		row.DecidedAt = approval.DecidedAt.UTC().Format(time.RFC3339)
	}
	return row
}

func (s *Server) integrationApprovalPortalView(ctx context.Context) (portalIntegrationApprovalsView, error) {
	view := portalIntegrationApprovalsView{}
	approvals, err := s.config.IntegrationStore.ListIntegrationApprovals(ctx)
	if err != nil {
		return view, err
	}
	for _, approval := range approvals {
		integration := s.config.Integrations.integration(approval.Integration)
		if integration == nil || !integration.integration.RequiresApproval || !siteNamePattern.MatchString(approval.Site) {
			continue
		}
		if approval.Status != ApprovalRequested && approval.Status != ApprovalApproved {
			continue
		}
		view.Approvals = append(view.Approvals, integrationApprovalPortalRow(approval, integration.integration.Title))
	}
	slices.SortFunc(view.Approvals, func(a, b portalIntegrationApproval) int {
		if a.Status != b.Status {
			if a.Status == ApprovalRequested {
				return -1
			}
			return 1
		}
		if order := strings.Compare(a.Site, b.Site); order != 0 {
			return order
		}
		return strings.Compare(a.Integration, b.Integration)
	})
	return view, nil
}

func (s *Server) manageIntegrationApprovals(w http.ResponseWriter, r *http.Request) {
	identity, ok := s.accessCaller(w, r)
	if !ok {
		return
	}
	if !s.isAdmin(identity) {
		writeError(w, http.StatusForbidden, "only platform admins list approvals")
		return
	}
	view, err := s.integrationApprovalPortalView(r.Context())
	if err != nil {
		writeServerError(w, err)
		return
	}
	s.renderFragment(w, "integration-approvals", view)
}

func (s *Server) manageRequestIntegrationApproval(w http.ResponseWriter, r *http.Request) {
	s.manageIntegrationApprovalMutation(w, r, "request")
}

func (s *Server) manageApproveIntegration(w http.ResponseWriter, r *http.Request) {
	s.manageIntegrationApprovalMutation(w, r, "approve")
}

func (s *Server) manageRevokeIntegrationApproval(w http.ResponseWriter, r *http.Request) {
	s.manageIntegrationApprovalMutation(w, r, "revoke")
}

func (s *Server) manageIntegrationApprovalMutation(w http.ResponseWriter, r *http.Request, operation string) {
	identity, integration, ok := s.requestApprovalTarget(w, r)
	if !ok {
		return
	}
	_, site, _, _, ok := s.manageCaller(w, r)
	if !ok {
		return
	}
	scope := r.URL.Query().Get("view")
	if scope != "" && scope != "catalog" {
		writeError(w, http.StatusBadRequest, "invalid integration view")
		return
	}
	if scope == "catalog" && !s.isAdmin(identity) {
		writeError(w, http.StatusForbidden, "only platform admins review approvals")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 8<<10)
	if err := r.ParseForm(); err != nil {
		writeError(w, http.StatusBadRequest, "expected an approval form of at most 8 KiB")
		return
	}
	var err error
	switch operation {
	case "request":
		_, err = s.requestSiteIntegrationApproval(r.Context(), identity, site, integration, r.PostForm.Get("reason"))
	case "approve":
		_, err = s.approveSiteIntegration(r.Context(), identity, site, integration)
	case "revoke":
		err = s.removeSiteIntegrationApproval(r.Context(), identity, site, integration)
	default:
		writeError(w, http.StatusNotFound, "approval operation not found")
		return
	}
	if err != nil {
		writeIntegrationApprovalError(w, err)
		return
	}
	if scope == "catalog" {
		s.manageIntegrationApprovals(w, r)
		return
	}
	s.manageSiteIntegrations(w, r)
}

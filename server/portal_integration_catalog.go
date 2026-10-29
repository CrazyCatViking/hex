package hex

import (
	"encoding/json"
	"fmt"
	"net/http"
)

type portalIntegrationStatus struct {
	Label string
	Tone  string
}

func (card portalIntegrationCard) personalAccess() portalIntegrationStatus {
	switch {
	case card.AuditUnavailable:
		return portalIntegrationStatus{"Unavailable", "denied"}
	case card.GrantedCount == 0:
		return portalIntegrationStatus{"No permission", "denied"}
	case card.Connector != "" && card.Connection == nil:
		return portalIntegrationStatus{"Login unavailable", "warn"}
	case card.Connector != "" && !card.Connection.Connected:
		return portalIntegrationStatus{"Link login to use", "warn"}
	case card.GrantedCount < len(card.Endpoints):
		return portalIntegrationStatus{fmt.Sprintf("%d of %d features", card.GrantedCount, len(card.Endpoints)), "warn"}
	default:
		return portalIntegrationStatus{"All features granted", "good"}
	}
}

func (card portalIntegrationCard) loginStatus() portalIntegrationStatus {
	switch {
	case card.Connector == "":
		return portalIntegrationStatus{"Not required", "muted"}
	case card.Connection == nil:
		return portalIntegrationStatus{"Unavailable", "warn"}
	case card.Connection.Connected:
		return portalIntegrationStatus{"Connected", "good"}
	default:
		return portalIntegrationStatus{"Not connected", "warn"}
	}
}

type integrationDetailView struct {
	Chrome
	Integration portalIntegrationCard
	Endpoints   []portalIntegrationEndpoint
}

type portalIntegrationEndpoint struct {
	IntegrationEndpointInfo
	QualifiedName string
	Status        portalIntegrationStatus
	Reason        string
	Input         string
	Output        string
}

func (s *Server) integrationDetailPage(w http.ResponseWriter, r *http.Request) {
	s.renderIntegrationDetail(w, r, "integration-detail-page")
}

func (s *Server) integrationDetailFragment(w http.ResponseWriter, r *http.Request) {
	s.renderIntegrationDetail(w, r, "integration-detail")
}

// Details are read-only discovery. They never invoke handlers or imply that a
// platform permission also grants access through an arbitrary site's policy.
func (s *Server) renderIntegrationDetail(w http.ResponseWriter, r *http.Request, template string) {
	if !s.platformRequest(r) {
		http.NotFound(w, r)
		return
	}
	identity := s.requestIdentity(r)
	if identity == nil {
		writeError(w, http.StatusUnauthorized, "not authenticated")
		return
	}
	registered := s.config.Integrations.integration(r.PathValue("integration"))
	if registered == nil {
		writeError(w, http.StatusNotFound, "integration not found")
		return
	}
	card, err := s.integrationPortalCard(r.Context(), integrationCaller{identity: identity}, registered)
	if err != nil {
		writeServerError(w, err)
		return
	}
	view := integrationDetailView{Chrome: s.chromeFor(identity, "integrations"), Integration: card}
	for _, endpoint := range card.Endpoints {
		feature, err := integrationPortalEndpoint(card, endpoint)
		if err != nil {
			writeServerError(w, err)
			return
		}
		view.Endpoints = append(view.Endpoints, feature)
	}
	s.writePortalPage(w, template, view)
}

func integrationPortalEndpoint(card portalIntegrationCard, endpoint IntegrationEndpointInfo) (portalIntegrationEndpoint, error) {
	feature := portalIntegrationEndpoint{
		IntegrationEndpointInfo: endpoint, QualifiedName: card.Name + "." + endpoint.Name,
		Status: portalIntegrationStatus{"Granted to you", "good"},
		Reason: "Your platform permission is granted. The calling site must also allow this operation.",
	}
	switch {
	case !endpoint.Allowed:
		feature.Status = portalIntegrationStatus{"Permission needed", "denied"}
		feature.Reason = "Ask a platform administrator for " + endpoint.Permission + ". Linking an account or approving a site does not grant this permission."
	case card.AuditUnavailable:
		feature.Status = portalIntegrationStatus{"Unavailable", "denied"}
		feature.Reason = "Your permission is granted, but the required audit store is not configured. Contact a platform administrator."
	case card.Connector != "" && card.Connection == nil:
		feature.Status = portalIntegrationStatus{"Login unavailable", "warn"}
		feature.Reason = "Your permission is granted, but connected accounts are not configured on this platform. Contact a platform administrator."
	case card.Connector != "" && !card.Connection.Connected:
		feature.Status = portalIntegrationStatus{"Link login", "warn"}
		feature.Reason = "Your platform permission is granted. Link your " + card.Connector + " login before using this feature. The linked account's own permissions also apply."
	}
	input, err := json.MarshalIndent(endpoint.InputSchema, "", "  ")
	if err != nil {
		return feature, fmt.Errorf("format input schema for %s: %w", feature.QualifiedName, err)
	}
	output, err := json.MarshalIndent(endpoint.OutputSchema, "", "  ")
	if err != nil {
		return feature, fmt.Errorf("format output schema for %s: %w", feature.QualifiedName, err)
	}
	feature.Input = string(input)
	feature.Output = string(output)
	return feature, nil
}

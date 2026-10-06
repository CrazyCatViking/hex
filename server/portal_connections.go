package hex

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"time"
)

// The Connected accounts page shows people which third-party accounts they
// have connected, when each was last used, and lets them connect or
// disconnect them. Platform admins also see everyone's connections and can
// remove them, for example when someone leaves.

type connectionsPageView struct {
	Chrome
	Mine       []connectionRow
	Everyone   []connectionRow
	Admin      bool
	IdleExpiry string
}

type connectionRow struct {
	Connector   string
	Title       string
	Description string
	Owner       string
	Person      string
	Connected   bool
	Account     string
	ConnectedAt string
	LastUsed    string
	ConnectURL  string
	RemoveURL   string
	Admin       bool
}

func (s *Server) registerPortalConnectionRoutes() {
	if !s.connectionsEnabled() {
		return
	}
	s.mux.HandleFunc("GET /connections", s.connectionsPage)
	s.mux.HandleFunc("DELETE /api/hex/manage/connections/{connector}", s.manageDisconnect)
	s.mux.HandleFunc("DELETE /api/hex/manage/connections/{connector}/{owner}", s.manageRemoveConnection)
}

func (s *Server) connectionsPage(w http.ResponseWriter, r *http.Request) {
	if !s.platformHost(r.Host) {
		http.NotFound(w, r)
		return
	}
	identity := s.requestIdentity(r)
	if identity == nil {
		writeError(w, http.StatusUnauthorized, "not authenticated")
		return
	}

	view := connectionsPageView{
		Chrome:     s.chromeFor(identity, "connections"),
		Admin:      s.isAdmin(identity),
		IdleExpiry: plural(int(s.connectionIdleExpiry().Hours()/24), "day"),
	}
	now := time.Now()
	for _, connector := range s.config.Integrations.allConnectors() {
		status, err := s.connectionStatus(r.Context(), identity, connector)
		if err != nil {
			writeServerError(w, err)
			return
		}
		view.Mine = append(view.Mine, s.ownConnectionRow(connector, status, now))
	}
	if view.Admin {
		everyone, err := s.everyonesConnections(r.Context(), now)
		if err != nil {
			writeServerError(w, err)
			return
		}
		view.Everyone = everyone
	}
	s.writePortalPage(w, "connections", view)
}

func (s *Server) ownConnectionRow(connector Connector, status connectionStatus, now time.Time) connectionRow {
	row := connectionRow{
		Connector: connector.Name, Title: connector.Title, Description: connector.Description,
		Connected: status.Connected, Account: status.Account,
		ConnectURL: status.ConnectURL + "?return=" + url.QueryEscape(s.connectionsPageURL()),
	}
	if status.Connected {
		row.ConnectedAt = status.ConnectedAt.Format("Jan 2, 2006")
		row.LastUsed = relativeTime(status.LastUsedAt, now)
	}
	return row
}

func (s *Server) connectionsPageURL() string {
	origin, err := s.platformOrigin()
	if err != nil {
		return ""
	}
	return origin.JoinPath("connections").String()
}

// everyonesConnections lists every stored connection, labelled with the
// people the platform remembers.
func (s *Server) everyonesConnections(ctx context.Context, now time.Time) ([]connectionRow, error) {
	records, err := s.config.IntegrationStore.ListCredentials(ctx, "")
	if err != nil {
		return nil, err
	}
	names := s.personNames(ctx, records)
	rows := make([]connectionRow, 0, len(records))
	for _, record := range records {
		if s.connectionIdle(record, now) {
			continue
		}
		title := record.Connector
		if connector, exists := s.config.Integrations.connector(record.Connector); exists {
			title = connector.Title
		}
		person := names[record.Owner]
		if person == "" {
			person = record.Owner
		}
		rows = append(rows, connectionRow{
			Connector: record.Connector, Title: title, Owner: record.Owner, Person: person,
			Connected: true, Account: record.Account, Admin: true,
			ConnectedAt: record.ConnectedAt.Format("Jan 2, 2006"),
			LastUsed:    relativeTime(record.LastUsedAt, now),
			RemoveURL:   "/api/hex/manage/connections/" + url.PathEscape(record.Connector) + "/" + url.PathEscape(record.Owner),
		})
	}
	return rows, nil
}

func (s *Server) personNames(ctx context.Context, records []CredentialRecord) map[string]string {
	names := make(map[string]string)
	if s.config.People == nil || len(records) == 0 {
		return names
	}
	owners := make([]string, 0, len(records))
	for _, record := range records {
		owners = append(owners, record.Owner)
	}
	people, err := s.config.People.GetPeople(ctx, owners)
	if err != nil {
		slog.Error("look up connection owners", "error", err)
		return names
	}
	for _, person := range people {
		names[person.ID] = personName(&person)
	}
	return names
}

// manageDisconnect removes the caller's own connection and answers with the
// connector's updated row.
func (s *Server) manageDisconnect(w http.ResponseWriter, r *http.Request) {
	identity, ok := s.accessCaller(w, r)
	if !ok {
		return
	}
	connector, ok := s.requestConnector(w, r)
	if !ok {
		return
	}
	if err := s.removeCredential(r.Context(), identity.ID, connector.Name, "disconnected by its owner"); err != nil {
		writeServerError(w, err)
		return
	}
	status, err := s.connectionStatus(r.Context(), identity, connector)
	if err != nil {
		writeServerError(w, err)
		return
	}
	s.renderFragment(w, "connection-row", s.ownConnectionRow(connector, status, time.Now()))
}

// manageRemoveConnection lets platform admins remove anyone's connection.
func (s *Server) manageRemoveConnection(w http.ResponseWriter, r *http.Request) {
	identity, ok := s.accessCaller(w, r)
	if !ok {
		return
	}
	if !s.isAdmin(identity) {
		writeError(w, http.StatusForbidden, "only platform admins remove other people's connections")
		return
	}
	owner := r.PathValue("owner")
	connector := r.PathValue("connector")
	err := s.config.IntegrationStore.DeleteCredential(r.Context(), owner, connector)
	if err != nil && !errors.Is(err, ErrNotFound) {
		writeServerError(w, err)
		return
	}
	slog.Info("connected account removed", "connector", connector, "owner", owner, "by", identityName(identity))
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
}

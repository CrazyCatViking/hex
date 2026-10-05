package hex

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"golang.org/x/oauth2"
)

// Connected accounts let integrations call a third-party API as the person
// using an app, so that system's own permissions apply. The platform runs
// the OAuth authorization code flow on its own domain, keeps each person's
// tokens sealed with Config.CredentialKey in the IntegrationStore, and
// refreshes them itself; apps and their browsers never see the tokens.

// accountRejection explains why Account refused a connection, without
// exposing internal failures.
func accountRejection(err error) string {
	var integrationError *IntegrationError
	if errors.As(err, &integrationError) {
		return integrationError.Message
	}
	return "its details could not be verified"
}

// Connector is an OAuth 2.0 authorization server that people connect their
// account with. The platform sets OAuth2.RedirectURL to its own callback,
// {platform}/api/hex/connections/{name}/callback, which must be registered
// with the provider. AuthCodeOptions add provider-specific parameters, such
// as offline access. Account optionally names the connected account, for
// example its email address, using a client authorized as that account; an
// error rejects the connection, so it can also enforce which accounts may
// connect (return an IntegrationError to explain why).
type Connector struct {
	Name            string
	Title           string
	Description     string
	OAuth2          oauth2.Config
	AuthCodeOptions []oauth2.AuthCodeOption
	Account         func(ctx context.Context, client *http.Client) (string, error)
}

// IntegrationApproval records a site's request to use an integration that
// requires approval, and a platform admin's decision.
type IntegrationApproval struct {
	Site        string    `json:"site"`
	Integration string    `json:"integration"`
	Status      string    `json:"status"`
	Reason      string    `json:"reason,omitempty"`
	RequestedBy *Person   `json:"requestedBy,omitempty"`
	RequestedAt time.Time `json:"requestedAt,omitzero"`
	DecidedBy   *Person   `json:"decidedBy,omitempty"`
	DecidedAt   time.Time `json:"decidedAt,omitzero"`
}

const (
	ApprovalRequested = "requested"
	ApprovalApproved  = "approved"
)

// IntegrationStore persists integration approvals and connected-account
// credentials. A credential is the owner's sealed token plus metadata that
// is stored in the clear so connections can be listed without opening
// them. ListCredentials with an empty owner lists everyone's and leaves
// Sealed empty. Missing records return ErrNotFound.
type IntegrationStore interface {
	GetIntegrationApproval(ctx context.Context, site, integration string) (IntegrationApproval, error)
	PutIntegrationApproval(ctx context.Context, approval IntegrationApproval) error
	DeleteIntegrationApproval(ctx context.Context, site, integration string) error
	ListIntegrationApprovals(ctx context.Context) ([]IntegrationApproval, error)

	GetCredential(ctx context.Context, owner, connector string) (CredentialRecord, error)
	PutCredential(ctx context.Context, record CredentialRecord) error
	TouchCredential(ctx context.Context, owner, connector string, usedAt time.Time) error
	DeleteCredential(ctx context.Context, owner, connector string) error
	ListCredentials(ctx context.Context, owner string) ([]CredentialRecord, error)
	DeleteCredentialsUnusedSince(ctx context.Context, cutoff time.Time) (int, error)
}

// CredentialRecord is one person's connection to one connector.
type CredentialRecord struct {
	Owner       string
	Connector   string
	Sealed      []byte
	Account     string
	ConnectedAt time.Time
	LastUsedAt  time.Time
}

// sealedToken is what a credential's sealed bytes hold.
type sealedToken struct {
	Token oauth2.Token `json:"token"`
}

const (
	defaultConnectionIdleExpiry = 90 * 24 * time.Hour
	connectionTouchInterval     = 10 * time.Minute
	connectionCleanupInterval   = time.Hour
)

func (s *Server) connectionIdleExpiry() time.Duration {
	if s.config.ConnectionIdleExpiry <= 0 {
		return defaultConnectionIdleExpiry
	}
	return s.config.ConnectionIdleExpiry
}

type connectState struct {
	State     string    `json:"state"`
	Verifier  string    `json:"verifier"`
	Owner     string    `json:"owner"`
	Connector string    `json:"connector"`
	Return    string    `json:"return,omitempty"`
	Expires   time.Time `json:"expires"`
}

const (
	connectCookie       = "hex_connect"
	connectCookiePath   = "/api/hex/connections/"
	connectStateTimeout = 10 * time.Minute
)

func (s *Server) connectionsEnabled() bool {
	return s.config.Identity != nil && s.config.IntegrationStore != nil &&
		s.config.CredentialSealer != nil && len(s.config.Integrations.allConnectors()) > 0
}

func (s *Server) registerConnectionRoutes() {
	if !s.connectionsEnabled() {
		return
	}
	s.mux.HandleFunc("GET /api/hex/connections", s.listConnections)
	s.mux.HandleFunc("GET /api/hex/connections/{connector}/start", s.startConnection)
	s.mux.HandleFunc("GET /api/hex/connections/{connector}/callback", s.completeConnection)
	s.mux.HandleFunc("DELETE /api/hex/connections/{connector}", s.deleteConnection)
}

// connectionCallback recognizes the provider's redirect back to the
// platform. It arrives as a cross-site navigation, which origin validation
// would otherwise reject; the sealed state cookie authenticates it instead.
func connectionCallback(r *http.Request) bool {
	if r.Method != http.MethodGet {
		return false
	}
	rest, found := strings.CutPrefix(r.URL.Path, connectCookiePath)
	if !found {
		return false
	}
	name, suffix, _ := strings.Cut(rest, "/")
	return integrationNamePattern.MatchString(name) && suffix == "callback"
}

// platformOrigin is where the portal, management API and OAuth callbacks
// live: the configured public URL, or the parent site origin.
func (s *Server) platformOrigin() (*url.URL, error) {
	if s.config.Connection != nil && s.config.Connection.Server != "" {
		parsed, err := url.Parse(s.config.Connection.Server)
		if err != nil || parsed.Host == "" {
			return nil, fmt.Errorf("invalid platform URL %q", s.config.Connection.Server)
		}
		return &url.URL{Scheme: parsed.Scheme, Host: parsed.Host}, nil
	}
	return parseSiteBaseURL(s.config.SiteBaseURL)
}

func (s *Server) connectURL(connector string) string {
	origin, err := s.platformOrigin()
	if err != nil {
		return ""
	}
	return origin.JoinPath("api", "hex", "connections", connector, "start").String()
}

func (s *Server) connectorConfig(connector Connector) (oauth2.Config, error) {
	origin, err := s.platformOrigin()
	if err != nil {
		return oauth2.Config{}, err
	}
	config := connector.OAuth2
	config.Scopes = append([]string(nil), connector.OAuth2.Scopes...)
	config.RedirectURL = origin.JoinPath("api", "hex", "connections", connector.Name, "callback").String()
	return config, nil
}

type connectionStatus struct {
	Name        string    `json:"name"`
	Title       string    `json:"title"`
	Description string    `json:"description,omitempty"`
	Connected   bool      `json:"connected"`
	Account     string    `json:"account,omitempty"`
	ConnectedAt time.Time `json:"connectedAt,omitzero"`
	LastUsedAt  time.Time `json:"lastUsedAt,omitzero"`
	ConnectURL  string    `json:"connectURL"`
}

func (s *Server) connectionStatus(ctx context.Context, identity *Identity, connector Connector) (connectionStatus, error) {
	status := connectionStatus{
		Name: connector.Name, Title: connector.Title, Description: connector.Description,
		ConnectURL: s.connectURL(connector.Name),
	}
	record, err := s.config.IntegrationStore.GetCredential(ctx, identity.ID, connector.Name)
	if errors.Is(err, ErrNotFound) {
		return status, nil
	}
	if err != nil {
		return status, err
	}
	if s.connectionIdle(record, time.Now()) {
		return status, nil
	}
	status.Connected = true
	status.Account = record.Account
	status.ConnectedAt = record.ConnectedAt
	status.LastUsedAt = record.LastUsedAt
	return status, nil
}

func (s *Server) listConnections(w http.ResponseWriter, r *http.Request) {
	identity, ok := s.accessCaller(w, r)
	if !ok {
		return
	}
	result := make([]connectionStatus, 0)
	for _, connector := range s.config.Integrations.allConnectors() {
		status, err := s.connectionStatus(r.Context(), identity, connector)
		if err != nil {
			writeServerError(w, err)
			return
		}
		result = append(result, status)
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) requestConnector(w http.ResponseWriter, r *http.Request) (Connector, bool) {
	connector, exists := s.config.Integrations.connector(r.PathValue("connector"))
	if !exists {
		writeError(w, http.StatusNotFound, "connector not found")
		return Connector{}, false
	}
	return connector, true
}

// startConnection begins the authorization code flow with PKCE. The state,
// verifier, owner and return address travel in a sealed, short-lived cookie
// scoped to the connection routes.
func (s *Server) startConnection(w http.ResponseWriter, r *http.Request) {
	identity, ok := s.accessCaller(w, r)
	if !ok {
		return
	}
	connector, ok := s.requestConnector(w, r)
	if !ok {
		return
	}
	config, err := s.connectorConfig(connector)
	if err != nil {
		writeServerError(w, err)
		return
	}

	stateValue, err := randomToken()
	if err != nil {
		writeServerError(w, err)
		return
	}
	state := connectState{
		State: stateValue, Verifier: oauth2.GenerateVerifier(), Owner: identity.ID,
		Connector: connector.Name, Return: s.safeReturnURL(r.URL.Query().Get("return")),
		Expires: time.Now().Add(connectStateTimeout),
	}
	encoded, err := json.Marshal(state)
	if err != nil {
		writeServerError(w, err)
		return
	}
	sealed, err := s.stateSealer.Seal(r.Context(), encoded, []byte("connect-state"))
	if err != nil {
		writeServerError(w, err)
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name: connectCookie, Value: base64.RawURLEncoding.EncodeToString(sealed),
		Path: connectCookiePath, MaxAge: int(connectStateTimeout.Seconds()),
		HttpOnly: true, Secure: s.secureCookies(), SameSite: http.SameSiteLaxMode,
	})

	options := append([]oauth2.AuthCodeOption{oauth2.S256ChallengeOption(state.Verifier)}, connector.AuthCodeOptions...)
	http.Redirect(w, r, config.AuthCodeURL(state.State, options...), http.StatusFound)
}

func (s *Server) completeConnection(w http.ResponseWriter, r *http.Request) {
	identity, ok := s.accessCaller(w, r)
	if !ok {
		return
	}
	connector, ok := s.requestConnector(w, r)
	if !ok {
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name: connectCookie, Path: connectCookiePath, MaxAge: -1,
		HttpOnly: true, Secure: s.secureCookies(), SameSite: http.SameSiteLaxMode,
	})

	state, err := s.readConnectState(r)
	valid := err == nil && state.Connector == connector.Name && state.Owner == identity.ID &&
		time.Now().Before(state.Expires) && r.URL.Query().Get("state") == state.State
	if !valid {
		writeError(w, http.StatusBadRequest, "the connection attempt expired or was started elsewhere; start it again")
		return
	}
	if providerError := r.URL.Query().Get("error"); providerError != "" {
		writeError(w, http.StatusBadRequest, connector.Title+" did not authorize the connection: "+providerError)
		return
	}
	code := r.URL.Query().Get("code")
	if code == "" {
		writeError(w, http.StatusBadRequest, "the authorization response has no code")
		return
	}

	config, err := s.connectorConfig(connector)
	if err != nil {
		writeServerError(w, err)
		return
	}
	token, err := config.Exchange(r.Context(), code, oauth2.VerifierOption(state.Verifier))
	if err != nil {
		slog.Warn("connect account", "connector", connector.Name, "error", err)
		writeError(w, http.StatusBadGateway, "could not complete the connection with "+connector.Title)
		return
	}
	now := time.Now().UTC()
	record := CredentialRecord{Owner: identity.ID, Connector: connector.Name, ConnectedAt: now, LastUsedAt: now}
	if connector.Account != nil {
		account, err := connector.Account(r.Context(), config.Client(r.Context(), token))
		if err != nil {
			slog.Warn("connected account rejected", "connector", connector.Name, "error", err)
			writeError(w, http.StatusForbidden, "this "+connector.Title+" account cannot be connected: "+accountRejection(err))
			return
		}
		record.Account = account
	}
	if err := s.saveCredential(r.Context(), record, token); err != nil {
		writeServerError(w, err)
		return
	}
	slog.Info("account connected", "connector", connector.Name, "owner", identityName(identity))

	if state.Return != "" {
		http.Redirect(w, r, state.Return, http.StatusFound)
		return
	}
	s.writeConnectedPage(w, connector)
}

func (s *Server) deleteConnection(w http.ResponseWriter, r *http.Request) {
	identity, ok := s.accessCaller(w, r)
	if !ok {
		return
	}
	connector, ok := s.requestConnector(w, r)
	if !ok {
		return
	}
	err := s.config.IntegrationStore.DeleteCredential(r.Context(), identity.ID, connector.Name)
	if err != nil && !errors.Is(err, ErrNotFound) {
		writeServerError(w, err)
		return
	}
	slog.Info("account disconnected", "connector", connector.Name, "owner", identityName(identity))
	w.WriteHeader(http.StatusNoContent)
}

var connectedPage = template.Must(template.New("connected").Parse(`<!doctype html>
<html lang="en"><head><meta charset="utf-8"><title>Connected</title>
<meta name="viewport" content="width=device-width, initial-scale=1">
<style>body{font:16px system-ui,sans-serif;margin:4rem auto;max-width:28rem;padding:0 1rem;color:#1d1d1f}</style>
</head><body><h1>{{.Title}} connected</h1><p>You can close this window and return to the app.</p>
<script nonce="{{.Nonce}}">if (window.opener) { window.close(); }</script>
</body></html>`))

func (s *Server) writeConnectedPage(w http.ResponseWriter, connector Connector) {
	nonce, err := randomToken()
	if err != nil {
		writeServerError(w, err)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; script-src 'nonce-"+nonce+"'")
	if err := connectedPage.Execute(w, map[string]string{"Title": connector.Title, "Nonce": nonce}); err != nil {
		slog.Error("write connected page", "error", err)
	}
}

// safeReturnURL accepts only the platform origin and its site subdomains,
// so the callback cannot be used as an open redirect.
func (s *Server) safeReturnURL(value string) string {
	if value == "" {
		return ""
	}
	parsed, err := url.Parse(value)
	if err != nil || (parsed.Scheme != "https" && parsed.Scheme != "http") || parsed.User != nil {
		return ""
	}
	host := strings.ToLower(parsed.Host)
	for _, origin := range s.trustedOrigins() {
		if parsed.Scheme == origin.Scheme && (host == origin.Host || strings.HasSuffix(host, "."+origin.Host)) {
			return parsed.String()
		}
	}
	return ""
}

func (s *Server) trustedOrigins() []*url.URL {
	var origins []*url.URL
	if origin, err := s.platformOrigin(); err == nil {
		origins = append(origins, origin)
	}
	if base, err := parseSiteBaseURL(s.config.SiteBaseURL); err == nil {
		origins = append(origins, base)
	}
	return origins
}

func (s *Server) secureCookies() bool {
	origin, err := s.platformOrigin()
	return err == nil && origin.Scheme == "https"
}

func (s *Server) readConnectState(r *http.Request) (connectState, error) {
	var state connectState
	cookie, err := r.Cookie(connectCookie)
	if err != nil {
		return state, err
	}
	sealed, err := base64.RawURLEncoding.DecodeString(cookie.Value)
	if err != nil {
		return state, err
	}
	data, err := s.stateSealer.Open(r.Context(), sealed, []byte("connect-state"))
	if err != nil {
		return state, err
	}
	err = json.Unmarshal(data, &state)
	return state, err
}

// connectionClient returns a client authorized as the owner's connected
// account. Refreshes are serialized per account, because providers such as
// Atlassian rotate refresh tokens and reject a superseded one.
func (s *Server) connectionClient(ctx context.Context, connectorName, owner string) (*http.Client, error) {
	connector, exists := s.config.Integrations.connector(connectorName)
	if !exists {
		return nil, fmt.Errorf("connector %s is not registered", connectorName)
	}
	token, err := s.connectionToken(ctx, connector, owner)
	if err != nil {
		return nil, err
	}
	return oauth2.NewClient(ctx, oauth2.StaticTokenSource(token)), nil
}

func (s *Server) connectionToken(ctx context.Context, connector Connector, owner string) (*oauth2.Token, error) {
	if !s.connectionsEnabled() {
		return nil, errors.New("connected accounts need an integration store and a credential sealer")
	}
	unlock := s.lockConnection(owner + "\x00" + connector.Name)
	defer unlock()

	record, err := s.config.IntegrationStore.GetCredential(ctx, owner, connector.Name)
	if errors.Is(err, ErrNotFound) {
		return nil, ErrNotConnected
	}
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	if s.connectionIdle(record, now) {
		s.removeCredential(ctx, owner, connector.Name, "unused for too long")
		return nil, ErrNotConnected
	}
	token, err := s.openCredential(ctx, record)
	if err != nil {
		return nil, err
	}

	if token.Valid() && time.Until(token.Expiry) > time.Minute {
		s.touchCredential(ctx, record, now)
		return token, nil
	}
	if token.RefreshToken == "" {
		if token.Valid() {
			s.touchCredential(ctx, record, now)
			return token, nil
		}
		return nil, ErrNotConnected
	}

	config, err := s.connectorConfig(connector)
	if err != nil {
		return nil, err
	}
	expired := *token
	expired.Expiry = time.Unix(1, 0)
	fresh, err := config.TokenSource(ctx, &expired).Token()
	var retrieveError *oauth2.RetrieveError
	if errors.As(err, &retrieveError) && retrieveError.Response != nil && retrieveError.Response.StatusCode < 500 {
		slog.Warn("connected account was revoked or expired", "connector", connector.Name, "error", err)
		s.removeCredential(ctx, owner, connector.Name, "refused by the provider")
		return nil, ErrNotConnected
	}
	if err != nil {
		return nil, fmt.Errorf("refresh %s token: %w", connector.Name, err)
	}

	record.LastUsedAt = now
	if err := s.saveCredential(ctx, record, fresh); err != nil {
		return nil, err
	}
	return fresh, nil
}

// connectionIdle reports whether a connection went unused for longer than
// the idle expiry; such connections count as disconnected.
func (s *Server) connectionIdle(record CredentialRecord, now time.Time) bool {
	lastUsed := record.LastUsedAt
	if lastUsed.IsZero() {
		lastUsed = record.ConnectedAt
	}
	return now.Sub(lastUsed) > s.connectionIdleExpiry()
}

// touchCredential records use, at most every few minutes per connection.
func (s *Server) touchCredential(ctx context.Context, record CredentialRecord, now time.Time) {
	if now.Sub(record.LastUsedAt) < connectionTouchInterval {
		return
	}
	if err := s.config.IntegrationStore.TouchCredential(ctx, record.Owner, record.Connector, now); err != nil {
		slog.Error("record connected account use", "connector", record.Connector, "error", err)
	}
}

func (s *Server) removeCredential(ctx context.Context, owner, connector, reason string) {
	err := s.config.IntegrationStore.DeleteCredential(ctx, owner, connector)
	if err != nil && !errors.Is(err, ErrNotFound) {
		slog.Error("remove connected account", "connector", connector, "error", err)
		return
	}
	slog.Info("connected account removed", "connector", connector, "owner", owner, "reason", reason)
}

func (s *Server) lockConnection(key string) func() {
	value, _ := s.connectionLocks.LoadOrStore(key, &sync.Mutex{})
	mutex := value.(*sync.Mutex)
	mutex.Lock()
	return mutex.Unlock
}

func credentialBinding(owner, connector string) []byte {
	return []byte("credential\x00" + owner + "\x00" + connector)
}

func (s *Server) openCredential(ctx context.Context, record CredentialRecord) (*oauth2.Token, error) {
	data, err := s.config.CredentialSealer.Open(ctx, record.Sealed, credentialBinding(record.Owner, record.Connector))
	if err != nil {
		return nil, fmt.Errorf("open %s credential: %w", record.Connector, err)
	}
	var sealed sealedToken
	if err := json.Unmarshal(data, &sealed); err != nil {
		return nil, fmt.Errorf("decode %s credential: %w", record.Connector, err)
	}
	return &sealed.Token, nil
}

func (s *Server) saveCredential(ctx context.Context, record CredentialRecord, token *oauth2.Token) error {
	data, err := json.Marshal(sealedToken{Token: *token})
	if err != nil {
		return err
	}
	record.Sealed, err = s.config.CredentialSealer.Seal(ctx, data, credentialBinding(record.Owner, record.Connector))
	if err != nil {
		return fmt.Errorf("seal %s credential: %w", record.Connector, err)
	}
	return s.config.IntegrationStore.PutCredential(ctx, record)
}

// cleanUpConnections removes connections unused for longer than the idle
// expiry until ctx ends.
func (s *Server) cleanUpConnections(ctx context.Context) {
	ticker := time.NewTicker(connectionCleanupInterval)
	defer ticker.Stop()
	for {
		cutoff := time.Now().UTC().Add(-s.connectionIdleExpiry())
		removed, err := s.config.IntegrationStore.DeleteCredentialsUnusedSince(ctx, cutoff)
		if err != nil && ctx.Err() == nil {
			slog.Error("remove unused connected accounts", "error", err)
		}
		if removed > 0 {
			slog.Info("removed unused connected accounts", "count", removed)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func randomToken() (string, error) {
	var data [24]byte
	if _, err := rand.Read(data[:]); err != nil {
		return "", fmt.Errorf("generate random token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(data[:]), nil
}

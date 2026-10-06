package hex_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	hex "github.com/crazycatviking/hex/server"
	"github.com/crazycatviking/hex/server/providers/easyauth"
	"github.com/crazycatviking/hex/server/providers/memory"
	"golang.org/x/oauth2"
)

type connectedRegressionFixture struct {
	servers []*hex.Server
	store   *memory.IntegrationStore
	sealer  hex.CredentialSealer
	calls   atomic.Int32
}

func newConnectedRegressionFixture(t *testing.T, refresh http.HandlerFunc, wrapStore ...func(*memory.IntegrationStore) hex.IntegrationStore) *connectedRegressionFixture {
	t.Helper()
	fixture := &connectedRegressionFixture{store: memory.NewIntegrationStore()}
	var servedStore hex.IntegrationStore = fixture.store
	if len(wrapStore) > 0 {
		servedStore = wrapStore[0](fixture.store)
	}
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/me" {
			fmt.Fprintf(w, `{"token":%q}`, strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
			return
		}
		if err := r.ParseForm(); err != nil {
			t.Error(err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if r.Form.Get("grant_type") == "refresh_token" && refresh != nil {
			refresh(w, r)
			return
		}
		fmt.Fprint(w, `{"access_token":"authorized","refresh_token":"refresh-authorized","token_type":"Bearer","expires_in":3600}`)
	}))
	t.Cleanup(provider.Close)
	registry := new(hex.IntegrationRegistry)
	if err := registry.RegisterConnector(hex.Connector{
		Name: "docs", Title: "Docs", OAuth2: oauth2.Config{ClientID: "client", Endpoint: oauth2.Endpoint{
			AuthURL: provider.URL + "/authorize", TokenURL: provider.URL + "/token", AuthStyle: oauth2.AuthStyleInParams,
		}},
	}); err != nil {
		t.Fatal(err)
	}
	if err := registry.Register(hex.Integration{
		Name: "docs", Title: "Docs", Connector: "docs", Endpoints: []hex.IntegrationEndpoint{{
			Name: "me", Description: "Read connected account.", CacheTTL: time.Hour,
			InputSchema: objectSchema, OutputSchema: objectSchema,
			Handler: func(ctx context.Context, call hex.IntegrationCall, _ json.RawMessage) (any, error) {
				fixture.calls.Add(1)
				client, err := call.HTTPClient(ctx)
				if err != nil {
					return nil, err
				}
				request, err := hex.NewJSONRequest(ctx, http.MethodGet, provider.URL+"/me", nil)
				if err != nil {
					return nil, err
				}
				var result map[string]string
				err = hex.DoJSON(client, request, "Docs", &result)
				return result, err
			},
		}},
	}); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		sealer, err := hex.NewKeySealer(make([]byte, 32))
		if err != nil {
			t.Fatal(err)
		}
		fixture.sealer = sealer
		fixture.servers = append(fixture.servers, hex.New(hex.Config{
			Identity: easyauth.Resolver{}, Access: memory.NewAccessStore(), SiteBaseURL: "https://hex.example.com",
			Integrations: registry, IntegrationGrants: []hex.IntegrationGrant{{Principal: "*", Permissions: []string{"*"}}},
			IntegrationStore: servedStore, CredentialSealer: sealer,
		}))
	}
	return fixture
}

type idleRegressionStore struct {
	*memory.IntegrationStore
	idle atomic.Bool
}

type observedRefreshRegressionStore struct {
	*memory.IntegrationStore
	attempts chan struct{}
}

func (s *observedRefreshRegressionStore) WithCredentialLock(ctx context.Context, owner, connector string, fn func(context.Context) error) error {
	select {
	case s.attempts <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	}
	return s.IntegrationStore.WithCredentialLock(ctx, owner, connector, fn)
}

func (s *idleRegressionStore) GetCredential(ctx context.Context, owner, connector string) (hex.CredentialRecord, error) {
	record, err := s.IntegrationStore.GetCredential(ctx, owner, connector)
	if err == nil && s.idle.Load() {
		record.LastUsedAt = time.Now().Add(-100 * 24 * time.Hour)
	}
	return record, err
}

func TestConnectedCacheRechecksIdleEligibility(t *testing.T) {
	var aged *idleRegressionStore
	fixture := newConnectedRegressionFixture(t, nil, func(store *memory.IntegrationStore) hex.IntegrationStore {
		aged = &idleRegressionStore{IntegrationStore: store}
		return aged
	})
	fixture.putToken(t, "old", false)
	requestAs(t, fixture.servers[0], roleHeaders("person"), "POST", connectedRegressionCall, nil, 200)
	requestAs(t, fixture.servers[0], roleHeaders("person"), "POST", connectedRegressionCall, nil, 200)
	aged.idle.Store(true)
	requestAs(t, fixture.servers[0], roleHeaders("person"), "POST", connectedRegressionCall, nil, 409)
	if fixture.calls.Load() != 1 {
		t.Fatal("an idle connection reached the real handler")
	}
	if _, err := fixture.store.GetCredential(context.Background(), "person", "docs"); !errors.Is(err, hex.ErrNotFound) {
		t.Fatalf("idle connection was not removed: %v", err)
	}
}

func (f *connectedRegressionFixture) putToken(t *testing.T, access string, expired bool) hex.CredentialRecord {
	t.Helper()
	expiry := time.Now().Add(time.Hour)
	if expired {
		expiry = time.Now().Add(-time.Hour)
	}
	data, err := json.Marshal(map[string]any{"token": oauth2.Token{
		AccessToken: access, RefreshToken: "refresh-" + access, TokenType: "Bearer", Expiry: expiry,
	}})
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := f.sealer.Seal(context.Background(), data, []byte("credential\x00person\x00docs"))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if err := f.store.PutCredential(context.Background(), hex.CredentialRecord{
		Owner: "person", Connector: "docs", Sealed: sealed, Account: access, ConnectedAt: now, LastUsedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	record, err := f.store.GetCredential(context.Background(), "person", "docs")
	if err != nil {
		t.Fatal(err)
	}
	return record
}

const connectedRegressionCall = "/api/sites/demo/integrations/docs/me"

func connectedRegressionRequest(server *hex.Server) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodPost, connectedRegressionCall, strings.NewReader(`{}`))
	request.Header = roleHeaders("person")
	request.Header.Set("X-Hex-Request", "1")
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	return response
}

func waitForRegressionSignal(t *testing.T, signal <-chan struct{}, responses <-chan *httptest.ResponseRecorder) {
	t.Helper()
	select {
	case <-signal:
	case response := <-responses:
		t.Fatalf("integration returned before the refresh signal: %d %s", response.Code, response.Body.String())
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for the refresh")
	}
}

func waitForRegressionResponse(t *testing.T, responses <-chan *httptest.ResponseRecorder) *httptest.ResponseRecorder {
	t.Helper()
	select {
	case response := <-responses:
		return response
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for the integration response")
		return nil
	}
}

func TestOAuthStateCanCompleteOnAnotherServer(t *testing.T) {
	fixture := newConnectedRegressionFixture(t, nil)
	start := requestAs(t, fixture.servers[0], roleHeaders("person"), "GET", "/api/hex/connections/docs/start", nil, 302)
	location, err := url.Parse(start.Header().Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	cookie := start.Result().Cookies()[0]
	headers := roleHeaders("person")
	headers.Set("Cookie", cookie.Name+"="+cookie.Value)
	callback := "/api/hex/connections/docs/callback?code=code&state=" + url.QueryEscape(location.Query().Get("state"))
	requestAs(t, fixture.servers[1], headers, "GET", callback, nil, 200)
	record, err := fixture.store.GetCredential(context.Background(), "person", "docs")
	if err != nil || record.Generation == "" || record.Version == "" {
		t.Fatalf("connection was not persisted with its generation/version: %+v, %v", record, err)
	}
	sealed, err := fixture.sealer.Seal(context.Background(), []byte(`{}`), []byte("credential\x00person\x00docs"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.sealer.Open(context.Background(), sealed, []byte("hex\x00oauth-connect-state\x00v1")); err == nil {
		t.Fatal("credential ciphertext opened as OAuth state")
	}
}

func TestConnectedCacheRejectsDisconnectAndReconnectAcrossServers(t *testing.T) {
	fixture := newConnectedRegressionFixture(t, nil)
	old := fixture.putToken(t, "old", false)
	for _, server := range fixture.servers {
		requestAs(t, server, roleHeaders("person"), "POST", connectedRegressionCall, nil, 200)
		cached := requestAs(t, server, roleHeaders("person"), "POST", connectedRegressionCall, nil, 200)
		if cached.Header().Get("X-Hex-Cache") != "hit" {
			t.Fatal("expected a cache hit")
		}
	}
	requestAs(t, fixture.servers[1], roleHeaders("person"), "DELETE", "/api/hex/connections/docs", nil, 204)
	requestAs(t, fixture.servers[0], roleHeaders("person"), "POST", connectedRegressionCall, nil, 409)
	fresh := fixture.putToken(t, "new", false)
	if old.Generation == fresh.Generation {
		t.Fatal("a replacement connection reused its generation")
	}
	for _, server := range fixture.servers {
		response := requestAs(t, server, roleHeaders("person"), "POST", connectedRegressionCall, nil, 200)
		if response.Header().Get("X-Hex-Cache") == "hit" || !strings.Contains(response.Body.String(), `"token":"new"`) {
			t.Fatalf("old cached connection was returned: %s", response.Body.String())
		}
	}
	if fixture.calls.Load() != 4 {
		t.Fatalf("expected four real calls, got %d", fixture.calls.Load())
	}
}

func TestRefreshCannotRestoreOrRemoveAReconnectedAccount(t *testing.T) {
	for _, revoked := range []bool{false, true} {
		for _, reconnect := range []bool{false, true} {
			t.Run(fmt.Sprintf("revoked=%t/reconnect=%t", revoked, reconnect), func(t *testing.T) {
				started := make(chan struct{})
				release := make(chan struct{})
				var released atomic.Bool
				fixture := newConnectedRegressionFixture(t, func(w http.ResponseWriter, r *http.Request) {
					close(started)
					select {
					case <-release:
					case <-r.Context().Done():
						return
					}
					if revoked {
						w.WriteHeader(http.StatusBadRequest)
						fmt.Fprint(w, `{"error":"invalid_grant"}`)
						return
					}
					fmt.Fprint(w, `{"access_token":"stale-refreshed","refresh_token":"rotated","token_type":"Bearer","expires_in":3600}`)
				})
				t.Cleanup(func() {
					if released.CompareAndSwap(false, true) {
						close(release)
					}
				})
				fixture.putToken(t, "old", true)
				responses := make(chan *httptest.ResponseRecorder, 1)
				go func() { responses <- connectedRegressionRequest(fixture.servers[0]) }()
				waitForRegressionSignal(t, started, responses)
				requestAs(t, fixture.servers[1], roleHeaders("person"), "DELETE", "/api/hex/connections/docs", nil, 204)
				var replacement hex.CredentialRecord
				if reconnect {
					replacement = fixture.putToken(t, "replacement", false)
				}
				released.Store(true)
				close(release)
				response := waitForRegressionResponse(t, responses)
				if response.Code != http.StatusConflict {
					t.Fatalf("stale refresh returned %d: %s", response.Code, response.Body.String())
				}
				current, err := fixture.store.GetCredential(context.Background(), "person", "docs")
				if reconnect {
					if err != nil || current.Version != replacement.Version || current.Generation != replacement.Generation {
						t.Fatalf("stale refresh affected replacement: %+v, %v", current, err)
					}
					requestAs(t, fixture.servers[0], roleHeaders("person"), "POST", connectedRegressionCall, nil, 200)
				} else if !errors.Is(err, hex.ErrNotFound) {
					t.Fatalf("stale refresh restored a disconnected account: %+v, %v", current, err)
				}
			})
		}
	}
}

func TestTokenRotationIsSerializedAcrossServers(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	var refreshes atomic.Int32
	var observed *observedRefreshRegressionStore
	fixture := newConnectedRegressionFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if refreshes.Add(1) != 1 {
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprint(w, `{"error":"invalid_grant"}`)
			return
		}
		close(started)
		select {
		case <-release:
		case <-r.Context().Done():
			return
		}
		fmt.Fprint(w, `{"access_token":"rotated","refresh_token":"rotated-refresh","token_type":"Bearer","expires_in":3600}`)
	}, func(store *memory.IntegrationStore) hex.IntegrationStore {
		observed = &observedRefreshRegressionStore{IntegrationStore: store, attempts: make(chan struct{}, 2)}
		return observed
	})
	var released atomic.Bool
	t.Cleanup(func() {
		if released.CompareAndSwap(false, true) {
			close(release)
		}
	})
	before := fixture.putToken(t, "old", true)
	responses := make(chan *httptest.ResponseRecorder, 2)
	go func() { responses <- connectedRegressionRequest(fixture.servers[0]) }()
	waitForRegressionSignal(t, started, responses)
	waitForRegressionSignal(t, observed.attempts, responses)
	go func() { responses <- connectedRegressionRequest(fixture.servers[1]) }()
	waitForRegressionSignal(t, observed.attempts, responses)
	released.Store(true)
	close(release)
	for range 2 {
		response := waitForRegressionResponse(t, responses)
		if response.Code != 200 || !strings.Contains(response.Body.String(), `"token":"rotated"`) {
			t.Fatalf("rotation failed: %d %s", response.Code, response.Body.String())
		}
	}
	after, err := fixture.store.GetCredential(context.Background(), "person", "docs")
	if err != nil || after.Generation != before.Generation || after.Version == before.Version || refreshes.Load() != 1 {
		t.Fatalf("rotation changed connection generation or refreshed twice: %+v %v (%d refreshes)", after, err, refreshes.Load())
	}
}

package cli

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"
	"golang.org/x/oauth2"
)

func TestDirectoryBrowserLoginIsReusedAcrossWorkspaces(t *testing.T) {
	t.Setenv("HEX_CONFIG_DIR", t.TempDir())
	t.Setenv("HEX_TOKEN", "")
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: key}, (&jose.SignerOptions{}).WithHeader("kid", "directory-test"))
	if err != nil {
		t.Fatal(err)
	}
	var issuer string
	var nonce, challenge string
	var browserCalls, exchanges atomic.Int32
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/.well-known/openid-configuration":
			if err := json.NewEncoder(w).Encode(map[string]any{"issuer": issuer, "authorization_endpoint": issuer + "/authorize", "token_endpoint": issuer + "/token", "jwks_uri": issuer + "/jwks", "id_token_signing_alg_values_supported": []string{"RS256"}}); err != nil {
				t.Error(err)
			}
		case "/jwks":
			if err := json.NewEncoder(w).Encode(jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{Key: &key.PublicKey, KeyID: "directory-test", Algorithm: "RS256", Use: "sig"}}}); err != nil {
				t.Error(err)
			}
		case "/token":
			exchanges.Add(1)
			if err := r.ParseForm(); err != nil {
				t.Error(err)
				w.WriteHeader(400)
				return
			}
			if r.Form.Get("client_id") != "hex-cli" || r.Header.Get("Authorization") != "" || r.Form.Get("code") != "fixture-code" || oauth2.S256ChallengeFromVerifier(r.Form.Get("code_verifier")) != challenge {
				t.Error("invalid public-client PKCE exchange")
				w.WriteHeader(400)
				return
			}
			claims, err := json.Marshal(map[string]any{"iss": issuer, "aud": "hex-cli", "sub": "person", "preferred_username": "person@example.test", "nonce": nonce, "iat": time.Now().Unix(), "exp": time.Now().Add(time.Hour).Unix()})
			if err != nil {
				t.Error(err)
				w.WriteHeader(500)
				return
			}
			signed, err := signer.Sign(claims)
			if err != nil {
				t.Error(err)
				w.WriteHeader(500)
				return
			}
			idToken, err := signed.CompactSerialize()
			if err != nil {
				t.Error(err)
				w.WriteHeader(500)
				return
			}
			if err := json.NewEncoder(w).Encode(map[string]any{"access_token": "personal-token", "token_type": "Bearer", "expires_in": 3600, "refresh_token": "fixture-refresh", "id_token": idToken}); err != nil {
				t.Error(err)
			}
		default:
			http.NotFound(w, r)
		}
	}))
	defer provider.Close()
	issuer = provider.URL
	f := newDirectoryFixture(t)
	f.directory.Auth.Issuer = issuer
	for i := range f.entries {
		var connection Connection
		if err := json.Unmarshal(f.entries[i].Connection, &connection); err != nil {
			t.Fatal(err)
		}
		connection.Auth = f.directory.Auth
		f.entries[i].Connection, err = json.Marshal(connection)
		if err != nil {
			t.Fatal(err)
		}
	}
	app, _ := testApp(t, t.TempDir())
	app.OpenBrowser = func(address string) error {
		browserCalls.Add(1)
		authorize, err := url.Parse(address)
		if err != nil {
			return err
		}
		query := authorize.Query()
		nonce, challenge = query.Get("nonce"), query.Get("code_challenge")
		callback := query.Get("redirect_uri") + "?" + url.Values{"code": {"fixture-code"}, "state": {query.Get("state")}}.Encode()
		response, err := http.Get(callback)
		if err != nil {
			return err
		}
		return response.Body.Close()
	}
	f.setup(t, app, "example")
	for _, args := range [][]string{
		{"login", "--directory", "example", "--account", "personal"},
		{"workspace", "use", "development", "--account", "personal"},
		{"whoami"},
		{"whoami", "--workspace", "production"},
	} {
		if err := app.Execute(context.Background(), args, "test"); err != nil {
			t.Fatalf("%v: %v", args, err)
		}
	}
	if browserCalls.Load() != 1 || exchanges.Load() != 1 || f.workspaceRequests.Load() != 2 {
		t.Fatalf("browser calls=%d, exchanges=%d, workspace API calls=%d", browserCalls.Load(), exchanges.Load(), f.workspaceRequests.Load())
	}
}

func TestDirectoryRefreshIsReusedWithoutChangingOtherAccounts(t *testing.T) {
	t.Setenv("HEX_CONFIG_DIR", t.TempDir())
	t.Setenv("HEX_TOKEN", "")
	var issuer string
	var refreshes atomic.Int32
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/.well-known/openid-configuration":
			if err := json.NewEncoder(w).Encode(map[string]any{"issuer": issuer, "authorization_endpoint": issuer + "/authorize", "token_endpoint": issuer + "/token", "jwks_uri": issuer + "/jwks"}); err != nil {
				t.Error(err)
			}
		case "/token":
			refreshes.Add(1)
			if err := r.ParseForm(); err != nil {
				t.Error(err)
				w.WriteHeader(400)
				return
			}
			if r.Form.Get("grant_type") != "refresh_token" || r.Form.Get("refresh_token") != "personal-refresh" || r.Form.Get("client_id") != "hex-cli" || r.Header.Get("Authorization") != "" {
				t.Error("invalid directory refresh exchange")
				w.WriteHeader(400)
				return
			}
			if err := json.NewEncoder(w).Encode(map[string]any{"access_token": "personal-token", "token_type": "Bearer", "expires_in": 3600, "refresh_token": "rotated-personal-refresh"}); err != nil {
				t.Error(err)
			}
		default:
			http.NotFound(w, r)
		}
	}))
	defer provider.Close()
	issuer = provider.URL
	fixture := newDirectoryFixture(t)
	fixture.directory.Auth.Issuer = issuer
	for i := range fixture.entries {
		var connection Connection
		if err := json.Unmarshal(fixture.entries[i].Connection, &connection); err != nil {
			t.Fatal(err)
		}
		connection.Auth = fixture.directory.Auth
		data, err := json.Marshal(connection)
		if err != nil {
			t.Fatal(err)
		}
		fixture.entries[i].Connection = data
	}
	app, _ := testApp(t, t.TempDir())
	app.OpenBrowser = func(string) error {
		t.Error("refresh opened a browser")
		return context.Canceled
	}
	fixture.setup(t, app, "example")
	personal := seedDirectoryAccount(t, fixture.directory, "personal", "expired-token")
	operator := seedDirectoryAccount(t, fixture.directory, "operator", "operator-token")
	if err := saveOIDCSession(personal, oidcSession{Username: "personal", Token: &oauth2.Token{AccessToken: "expired-token", RefreshToken: "personal-refresh", Expiry: time.Now().Add(-time.Hour)}}); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"workspace", "use", "development", "--account", "personal"},
		{"whoami"},
		{"whoami", "--workspace", "production"},
	} {
		if err := app.Execute(context.Background(), args, "test"); err != nil {
			t.Fatalf("%v: %v", args, err)
		}
	}
	if refreshes.Load() != 1 || fixture.workspaceRequests.Load() != 2 {
		t.Fatalf("refresh exchanges=%d, workspace requests=%d", refreshes.Load(), fixture.workspaceRequests.Load())
	}
	session, err := readOIDCSession(personal)
	if err != nil || session.Token == nil || session.Token.RefreshToken != "rotated-personal-refresh" {
		t.Fatal("refresh token rotation was not persisted", err)
	}
	session, err = readOIDCSession(operator)
	if err != nil || session.Token == nil || session.Token.AccessToken != "operator-token" {
		t.Fatal("refresh changed another account", err)
	}
}

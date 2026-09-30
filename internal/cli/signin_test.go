package cli

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const (
	testClientID = "66666666-7777-8888-9999-000000000000"
	testTenantID = "11111111-2222-3333-4444-555555555555"
	testResource = "api://hex"
)

// fakeIdentityProvider implements just enough of the Microsoft identity
// platform for an interactive authorization code sign-in and refreshes.
type fakeIdentityProvider struct {
	server    *httptest.Server
	codes     atomic.Int32
	refreshes atomic.Int32
}

func startIdentityProvider(t *testing.T) *fakeIdentityProvider {
	t.Helper()
	provider := &fakeIdentityProvider{}
	provider.server = httptest.NewTLSServer(http.HandlerFunc(provider.serve))
	t.Cleanup(provider.server.Close)
	return provider
}

func (p *fakeIdentityProvider) serve(w http.ResponseWriter, r *http.Request) {
	authority := p.server.URL + "/" + testTenantID
	w.Header().Set("Content-Type", "application/json")
	switch r.URL.Path {
	case "/" + testTenantID + "/v2.0/.well-known/openid-configuration":
		json.NewEncoder(w).Encode(map[string]string{
			"authorization_endpoint": authority + "/oauth2/v2.0/authorize",
			"token_endpoint":         authority + "/oauth2/v2.0/token",
			"issuer":                 authority + "/v2.0",
		})
	case "/" + testTenantID + "/oauth2/v2.0/token":
		if err := r.ParseForm(); err != nil || r.PostForm.Get("client_id") != testClientID {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		switch r.PostForm.Get("grant_type") {
		case "authorization_code":
			if r.PostForm.Get("code") != "fixture-code" || r.PostForm.Get("code_verifier") == "" {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			p.codes.Add(1)
		case "refresh_token":
			p.refreshes.Add(1)
		default:
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		json.NewEncoder(w).Encode(p.tokens(r.PostForm.Get("scope"), authority))
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func (p *fakeIdentityProvider) tokens(scope, authority string) map[string]any {
	encode := func(value any) string {
		data, err := json.Marshal(value)
		if err != nil {
			panic(err)
		}
		return base64.RawURLEncoding.EncodeToString(data)
	}
	now := time.Now().Unix()
	idToken := encode(map[string]string{"alg": "none", "typ": "JWT"}) + "." + encode(map[string]any{
		"aud": testClientID, "iss": authority + "/v2.0", "tid": testTenantID, "oid": "alex-oid",
		"preferred_username": "alex@example.com", "iat": now, "nbf": now, "exp": now + 3600,
	}) + "."
	return map[string]any{
		"token_type":     "Bearer",
		"scope":          scope,
		"expires_in":     3600,
		"ext_expires_in": 3600,
		"access_token":   "platform-token-" + string(rune('0'+p.codes.Load()+p.refreshes.Load())),
		"refresh_token":  "fixture-refresh",
		"id_token":       idToken,
		"client_info":    encode(map[string]string{"uid": "alex-oid", "utid": testTenantID}),
	}
}

// completeInBrowser plays the browser: it follows the authorization URL
// straight back to the CLI's localhost redirect with a code.
func completeInBrowser(t *testing.T) func(string) error {
	return func(address string) error {
		authorization, err := url.Parse(address)
		if err != nil {
			return err
		}
		query := authorization.Query()
		if query.Get("client_id") != testClientID || !strings.Contains(query.Get("scope"), testResource+"/.default") || query.Get("code_challenge") == "" {
			t.Errorf("unexpected authorization request: %s", address)
		}
		form := url.Values{"code": {"fixture-code"}, "state": {query.Get("state")}}
		go func() {
			response, err := http.PostForm(query.Get("redirect_uri"), form)
			if err == nil {
				response.Body.Close()
			}
		}()
		return nil
	}
}

func signInApp(t *testing.T, provider *fakeIdentityProvider, interactive bool) *App {
	t.Helper()
	app, _ := testApp(t, t.TempDir())
	app.Interactive = interactive
	app.signInHost = provider.server.URL
	app.signInHTTP = provider.server.Client()
	app.OpenBrowser = completeInBrowser(t)
	return app
}

func TestBrowserSignInIsSavedAndReused(t *testing.T) {
	if runtime.GOOS == "linux" {
		t.Setenv("DISPLAY", ":fixture")
	}
	t.Setenv("CODESPACES", "")
	t.Setenv("HEX_TOKEN", "")
	directory := t.TempDir()
	t.Setenv("HEX_CONFIG_DIR", directory)
	// Azure CLI must not be needed.
	t.Setenv("PATH", t.TempDir())
	provider := startIdentityProvider(t)
	project := Project{Resource: testResource, ClientID: testClientID, TenantID: testTenantID}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	first, err := signInApp(t, provider, true).apiToken(ctx, project)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(first, "platform-token-") || provider.codes.Load() != 1 {
		t.Fatalf("browser sign-in did not complete: %q", first)
	}
	info, err := os.Stat(filepath.Join(directory, "sign-in.json"))
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0600 {
		t.Fatalf("saved sign-in is readable by others: %v", info.Mode())
	}

	// A later command, even non-interactive, reuses the saved session.
	second, err := signInApp(t, provider, false).apiToken(ctx, project)
	if err != nil {
		t.Fatal(err)
	}
	if second == "" || provider.codes.Load() != 1 {
		t.Fatalf("saved session was not reused: %q, %d sign-ins", second, provider.codes.Load())
	}

	// After signing out, non-interactive commands ask for hex login.
	if err := signInApp(t, provider, false).signOut(ctx, project); err != nil {
		t.Fatal(err)
	}
	if _, err := signInApp(t, provider, false).apiToken(ctx, project); err == nil || !strings.Contains(err.Error(), "hex login") {
		t.Fatalf("expected a sign-in prompt, got %v", err)
	}
}

func TestSelfSignInNamesTheAPIByClientID(t *testing.T) {
	own := Project{Resource: "api://" + testClientID, ClientID: testClientID, TenantID: testTenantID}
	if scopes := signInScopes(own); len(scopes) != 1 || scopes[0] != testClientID+"/.default" {
		t.Fatalf("a registration's own API must be requested by client ID: %v", scopes)
	}
	other := Project{Resource: "api://other-api/", ClientID: testClientID, TenantID: testTenantID}
	if scopes := signInScopes(other); scopes[0] != "api://other-api/.default" {
		t.Fatalf("another API keeps its URI: %v", scopes)
	}
}

func TestProfilesCarryTheSignInApp(t *testing.T) {
	directory := t.TempDir()
	t.Setenv("HEX_CONFIG_DIR", directory)
	connection := localConnection("http://localhost:8080")
	connection.Resource = testResource
	connection.ClientID = testClientID
	connection.TenantID = testTenantID
	data, err := json.Marshal(connection)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := parseConnection(data, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := saveProfile(connection, "company"); err != nil {
		t.Fatal(err)
	}

	app, _ := testApp(t, directory)
	project, err := app.commandConfig("company", false)
	if err != nil || project.Auth == nil || project.Auth.ClientID != testClientID || project.Auth.Issuer != "https://login.microsoftonline.com/"+testTenantID+"/v2.0" {
		t.Fatalf("sign-in app not resolved: %+v %v", project, err)
	}
	if other := project.withResource("api://other"); other.Auth != nil || other.ClientID != "" {
		t.Fatal("the sign-in app was used for a different resource")
	}

	for _, change := range []func(*Connection){
		func(c *Connection) { c.TenantID = "" },
		func(c *Connection) { c.ClientID = "not-a-guid" },
		func(c *Connection) { c.Resource = "" },
	} {
		invalid := connection
		change(&invalid)
		data, err := json.Marshal(invalid)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := parseConnection(data, ""); err == nil {
			t.Fatalf("incomplete sign-in settings accepted: %+v", invalid)
		}
	}
}

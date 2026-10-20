package cli

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	hex "github.com/crazycatviking/hex/server"
	"golang.org/x/oauth2"
)

func TestOIDCPublicClientTokenRequests(t *testing.T) {
	for _, grant := range []string{"authorization_code", "refresh_token"} {
		for _, reject := range []bool{false, true} {
			name := grant + "/success"
			if reject {
				name = grant + "/rejected"
			}
			t.Run(name, func(t *testing.T) {
				var tokenRequests atomic.Int32
				var issuer string
				provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Content-Type", "application/json")
					switch r.URL.Path {
					case "/.well-known/openid-configuration":
						if err := json.NewEncoder(w).Encode(map[string]string{
							"issuer": issuer, "authorization_endpoint": issuer + "/authorize", "token_endpoint": issuer + "/token",
						}); err != nil {
							t.Errorf("write discovery: %v", err)
						}
					case "/token":
						attempt := tokenRequests.Add(1)
						if r.Method != http.MethodPost {
							t.Errorf("token request method = %s", r.Method)
						}
						if r.Header.Get("Authorization") != "" {
							t.Error("public client sent an Authorization header")
						}
						if err := r.ParseForm(); err != nil {
							t.Errorf("parse token request: %v", err)
							w.WriteHeader(http.StatusBadRequest)
							return
						}
						if r.PostForm.Get("client_id") != "hex-cli" || r.PostForm.Has("client_secret") || r.PostForm.Get("grant_type") != grant {
							t.Errorf("unexpected public client token request: %v", r.PostForm)
						}
						if grant == "authorization_code" {
							if r.PostForm.Get("code") != "fixture-code" || r.PostForm.Get("code_verifier") != "fixture-verifier" || r.PostForm.Get("redirect_uri") != "http://localhost:12345" {
								t.Errorf("unexpected code exchange: %v", r.PostForm)
							}
						} else if r.PostForm.Get("refresh_token") != "fixture-refresh" {
							t.Errorf("unexpected refresh: %v", r.PostForm)
						}
						var response any = map[string]any{"access_token": "fixture-access", "token_type": "Bearer", "expires_in": 3600}
						if reject {
							w.WriteHeader(http.StatusBadRequest)
							description := "Original grant failure"
							if attempt > 1 {
								description = "Code not valid"
							}
							response = map[string]string{"error": "invalid_grant", "error_description": description}
						}
						if err := json.NewEncoder(w).Encode(response); err != nil {
							t.Errorf("write token response: %v", err)
						}
					default:
						http.NotFound(w, r)
					}
				}))
				defer provider.Close()
				issuer = provider.URL

				app, _ := testApp(t, t.TempDir())
				project := Project{Auth: &hex.AuthConfig{Type: "oidc", Issuer: issuer, ClientID: "hex-cli", Scopes: []string{"openid", "offline_access"}}}
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				_, config, err := app.oidcProvider(ctx, project)
				if err != nil {
					t.Fatal(err)
				}
				config.RedirectURL = "http://localhost:12345"
				var token *oauth2.Token
				if grant == "authorization_code" {
					token, err = config.Exchange(ctx, "fixture-code", oauth2.VerifierOption("fixture-verifier"))
				} else {
					token, err = config.TokenSource(ctx, &oauth2.Token{RefreshToken: "fixture-refresh"}).Token()
				}
				if reject {
					var retrieveError *oauth2.RetrieveError
					if !errors.As(err, &retrieveError) || retrieveError.ErrorCode != "invalid_grant" || retrieveError.ErrorDescription != "Original grant failure" {
						t.Fatalf("original token error was not preserved: %v", err)
					}
				} else if err != nil || token == nil || token.AccessToken != "fixture-access" {
					t.Fatalf("token request failed: token=%+v, err=%v", token, err)
				}
				if count := tokenRequests.Load(); count != 1 {
					t.Fatalf("token requests = %d, want exactly one", count)
				}
			})
		}
	}
}

func TestOIDCProfilesCarryAuthAndScopeSavedSessions(t *testing.T) {
	t.Setenv("HEX_CONFIG_DIR", t.TempDir())
	connection := localConnection("http://localhost:8080")
	connection.Auth = &hex.AuthConfig{Type: "oidc", Issuer: "http://localhost:9090", ClientID: "hex-cli", Scopes: []string{"openid", "hex-api"}}
	data, err := json.Marshal(connection)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := parseConnection(data, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := saveProfile(parsed, "local"); err != nil {
		t.Fatal(err)
	}
	app, _ := testApp(t, t.TempDir())
	project, err := app.commandConfig("local", false)
	if err != nil || project.Auth == nil || project.Auth.ClientID != "hex-cli" {
		t.Fatalf("auth lost when resolving profile: %+v, %v", project, err)
	}
	first, err := oidcSessionPath(project)
	if err != nil {
		t.Fatal(err)
	}
	project.Server = "http://localhost:8085"
	second, err := oidcSessionPath(project)
	if err != nil {
		t.Fatal(err)
	}
	if first == second {
		t.Fatal("platforms share a saved session")
	}
	connection.Resource = "api://legacy"
	data, err = json.Marshal(connection)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := parseConnection(data, ""); err == nil {
		t.Fatal("mixed OIDC and legacy auth accepted")
	}
}

package cli

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"
)

type oidcSession struct {
	Token    *oauth2.Token `json:"token"`
	Username string        `json:"username"`
}

func oidcSessionPath(project Project) (string, error) {
	directory, err := profileDirectory()
	if err != nil {
		return "", err
	}
	settings, err := json.Marshal(struct {
		Server string
		Auth   any
	}{project.Server, project.Auth})
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(settings)
	return filepath.Join(directory, "oidc-"+hex.EncodeToString(digest[:])+".json"), nil
}

func readOIDCSession(path string) (oidcSession, error) {
	var session oidcSession
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return session, nil
	}
	if err != nil {
		return session, fmt.Errorf("read saved OIDC sign-in: %w", err)
	}
	if err := json.Unmarshal(data, &session); err != nil {
		return session, fmt.Errorf("decode saved OIDC sign-in: %w", err)
	}
	return session, nil
}

func saveOIDCSession(path string, session oidcSession) error {
	data, err := json.Marshal(session)
	if err != nil {
		return err
	}
	return atomicWrite(path, data, 0600)
}

func (a *App) oidcProvider(ctx context.Context, project Project) (*oidc.Provider, *oauth2.Config, error) {
	if err := project.Auth.Validate(); err != nil {
		return nil, nil, err
	}
	provider, err := oidc.NewProvider(ctx, project.Auth.Issuer)
	if err != nil {
		return nil, nil, fmt.Errorf("discover identity provider: %w", err)
	}
	endpoint := provider.Endpoint()
	for _, address := range []string{endpoint.AuthURL, endpoint.TokenURL} {
		parsed, err := url.Parse(address)
		if err != nil || parsed.Host == "" || parsed.User != nil || (parsed.Scheme != "https" && !(parsed.Scheme == "http" && isLocalHost(parsed.Hostname()))) {
			return nil, nil, errors.New("identity provider endpoints must use HTTPS, or HTTP on loopback")
		}
	}
	return provider, &oauth2.Config{ClientID: project.Auth.ClientID, Endpoint: endpoint, Scopes: project.Auth.Scopes}, nil
}

func (a *App) oidcToken(ctx context.Context, project Project, forceLogin bool) (string, error) {
	path, err := oidcSessionPath(project)
	if err != nil {
		return "", err
	}
	session, err := readOIDCSession(path)
	if err != nil {
		return "", err
	}
	if !forceLogin && session.Token != nil && session.Token.Valid() {
		return session.Token.AccessToken, nil
	}
	ctx = context.WithValue(ctx, oauth2.HTTPClient, &http.Client{Timeout: 30 * time.Second})
	provider, config, err := a.oidcProvider(ctx, project)
	if err != nil {
		return "", err
	}
	if !forceLogin && session.Token != nil && session.Token.RefreshToken != "" {
		token, err := config.TokenSource(ctx, session.Token).Token()
		if err == nil {
			session.Token = token
			if err := saveOIDCSession(path, session); err != nil {
				return "", err
			}
			return token.AccessToken, nil
		}
		fmt.Fprintln(a.Err, "The saved sign-in could not be refreshed; sign in again.")
	}
	if !forceLogin && !a.Interactive {
		return "", errors.New("not signed in to the platform; run hex login in a terminal, or set HEX_TOKEN")
	}
	session, err = a.oidcBrowserLogin(ctx, provider, config)
	if err != nil {
		return "", err
	}
	if err := saveOIDCSession(path, session); err != nil {
		return "", err
	}
	if forceLogin {
		fmt.Fprintf(a.Out, "Signed in as %s.\n", session.Username)
	}
	return session.Token.AccessToken, nil
}

type oidcCallback struct {
	Code string
	Err  error
}

func (a *App) oidcBrowserLogin(ctx context.Context, provider *oidc.Provider, config *oauth2.Config) (oidcSession, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return oidcSession{}, fmt.Errorf("listen for browser sign-in: %w", err)
	}
	_, port, err := net.SplitHostPort(listener.Addr().String())
	if err != nil {
		listener.Close()
		return oidcSession{}, err
	}
	config.RedirectURL = "http://localhost:" + port
	state, nonce, verifier := rand.Text(), rand.Text(), oauth2.GenerateVerifier()
	results := make(chan oidcCallback, 1)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		query := r.URL.Query()
		if query.Get("state") != state {
			http.Error(w, "invalid sign-in state", http.StatusBadRequest)
			return
		}
		result := oidcCallback{Code: query.Get("code")}
		if query.Get("error") != "" || result.Code == "" {
			result.Err = fmt.Errorf("identity provider sign-in failed: %s", query.Get("error"))
		}
		select {
		case results <- result:
		default:
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		fmt.Fprintln(w, "Sign-in received. Return to the Hex terminal.")
	})
	server := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	defer server.Close()
	go func() {
		if err := server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			select {
			case results <- oidcCallback{Err: err}:
			default:
			}
		}
	}()
	address := config.AuthCodeURL(state, oauth2.S256ChallengeOption(verifier), oauth2.SetAuthURLParam("nonce", nonce), oauth2.SetAuthURLParam("prompt", "login"))
	fmt.Fprintf(a.Err, "Sign in to the platform in your browser:\n%s\n", address)
	if err := a.OpenBrowser(address); err != nil {
		fmt.Fprintf(a.Err, "Could not open the browser (%v). Open the URL above to continue.\n", err)
	}
	var callback oidcCallback
	select {
	case <-ctx.Done():
		return oidcSession{}, fmt.Errorf("wait for browser sign-in: %w", ctx.Err())
	case callback = <-results:
	}
	if callback.Err != nil {
		return oidcSession{}, callback.Err
	}
	token, err := config.Exchange(ctx, callback.Code, oauth2.VerifierOption(verifier))
	if err != nil {
		return oidcSession{}, fmt.Errorf("exchange sign-in code: %w", err)
	}
	rawIDToken, ok := token.Extra("id_token").(string)
	if !ok {
		return oidcSession{}, errors.New("identity provider did not return an ID token")
	}
	idToken, err := provider.Verifier(&oidc.Config{ClientID: config.ClientID}).Verify(ctx, rawIDToken)
	if err != nil {
		return oidcSession{}, fmt.Errorf("verify identity provider ID token: %w", err)
	}
	if idToken.Nonce != nonce {
		return oidcSession{}, errors.New("identity provider returned an invalid nonce")
	}
	var claims struct {
		PreferredUsername string `json:"preferred_username"`
		Email             string `json:"email"`
	}
	if err := idToken.Claims(&claims); err != nil {
		return oidcSession{}, fmt.Errorf("read identity claims: %w", err)
	}
	username := claims.PreferredUsername
	if username == "" {
		username = claims.Email
	}
	if username == "" {
		username = idToken.Subject
	}
	return oidcSession{Token: token, Username: username}, nil
}

func (a *App) oidcLogout(project Project) error {
	path, err := oidcSessionPath(project)
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	fmt.Fprintln(a.Out, "Signed out.")
	return nil
}

package cli

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	hex "github.com/crazycatviking/hex/server"
)

func TestProjectRejectsMismatchedPinnedServerBeforeAuthentication(t *testing.T) {
	t.Setenv("HEX_TOKEN", "")
	for _, authType := range []string{"oidc", "legacy-resource"} {
		t.Run(authType, func(t *testing.T) {
			directory := t.TempDir()
			t.Setenv("HEX_CONFIG_DIR", filepath.Join(directory, "profiles"))
			requests := 0
			foreign := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests++
				t.Errorf("foreign server or token issuer received a request: %s", r.URL)
			}))
			defer foreign.Close()
			connection := localConnection("https://saved.example.com")
			if authType == "oidc" {
				connection.Auth = &hex.AuthConfig{Type: "oidc", Issuer: foreign.URL, ClientID: "hex-cli", Scopes: []string{"openid"}}
			} else {
				connection.Resource = "api://saved-resource"
			}
			if _, err := saveProfile(connection, "company"); err != nil {
				t.Fatal(err)
			}
			tools := t.TempDir()
			marker := filepath.Join(tools, "token-acquired")
			if err := os.WriteFile(filepath.Join(tools, "az"), []byte("#!/bin/sh\n: > \""+marker+"\"\nexit 1\n"), 0700); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", tools)
			if err := writeJSONFile(filepath.Join(directory, "hex.json"), Project{Name: "demo", Platform: "company", Server: foreign.URL}); err != nil {
				t.Fatal(err)
			}
			for _, args := range [][]string{{"sites"}, {"actions", "list", "--site", "demo"}, {"publish"}} {
				app, _ := testApp(t, directory)
				err := app.Execute(context.Background(), args, "test")
				if err == nil || !strings.Contains(err.Error(), "project server differs") || !strings.Contains(err.Error(), "separate profile") || !strings.Contains(err.Error(), "--platform") {
					t.Fatalf("%v did not reject the pinned server: %v", args, err)
				}
			}
			if requests != 0 {
				t.Fatal("mismatched configuration made foreign requests")
			}
			if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("mismatched configuration acquired an Azure CLI token: %v", err)
			}
		})
	}
}

func TestProjectPinnedServerNormalizedOrigins(t *testing.T) {
	for _, test := range []struct{ saved, pinned string }{
		{"https://hex.example.com", "https://HEX.example.com:443/"},
		{"http://localhost", "http://localhost:80/"},
		{"http://[::1]", "http://[::1]:80/"},
	} {
		t.Run(test.pinned, func(t *testing.T) {
			directory := t.TempDir()
			t.Setenv("HEX_CONFIG_DIR", filepath.Join(directory, "profiles"))
			connection := localConnection(test.saved)
			connection.Resource = "api://same-resource"
			if _, err := saveProfile(connection, "company"); err != nil {
				t.Fatal(err)
			}
			if err := writeJSONFile(filepath.Join(directory, "hex.json"), Project{Name: "demo", Platform: "company", Server: test.pinned}); err != nil {
				t.Fatal(err)
			}
			app, _ := testApp(t, directory)
			project, err := app.readProjectIn(directory, false)
			if err != nil || project.Server != test.saved || project.Resource != connection.Resource {
				t.Fatalf("equivalent origin lost profile settings: %+v %v", project, err)
			}
		})
	}
}

func TestFetchRejectsMismatchedPinnedSiteBaseBeforeAuthentication(t *testing.T) {
	for _, token := range []string{"", "existing-token"} {
		t.Run("token="+token, func(t *testing.T) {
			t.Setenv("HEX_TOKEN", token)
			directory := t.TempDir()
			t.Setenv("HEX_CONFIG_DIR", filepath.Join(directory, "profiles"))
			issuerRequests := 0
			issuer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				issuerRequests++
			}))
			defer issuer.Close()
			connection := localConnection("https://saved.example.com")
			connection.SiteBaseURL = "https://sites.example.com"
			connection.Auth = &hex.AuthConfig{Type: "oidc", Issuer: issuer.URL, ClientID: "hex-cli", Scopes: []string{"openid"}}
			if _, err := saveProfile(connection, "company"); err != nil {
				t.Fatal(err)
			}
			project := Project{Name: "demo", Platform: "company", Server: connection.Server, SiteBaseURL: "https://attacker.example.com"}
			if err := writeJSONFile(filepath.Join(directory, "hex.json"), project); err != nil {
				t.Fatal(err)
			}
			app, output := testApp(t, directory)
			networkRequests := 0
			app.HTTP.Transport = cliRoundTripFunc(func(request *http.Request) (*http.Response, error) {
				networkRequests++
				return nil, errors.New("unexpected request to " + request.URL.String())
			})
			err := app.Execute(context.Background(), []string{"fetch", "--site", "demo"}, "test")
			if err == nil || !strings.Contains(err.Error(), "project siteBaseURL differs") || !strings.Contains(err.Error(), "separate profile") || !strings.Contains(err.Error(), "--platform") {
				t.Fatalf("fetch did not reject the pinned site origin: %v", err)
			}
			if networkRequests != 0 || issuerRequests != 0 || output.Len() != 0 {
				t.Fatalf("mismatched site origin made requests or output: network=%d issuer=%d output=%q", networkRequests, issuerRequests, output.String())
			}
			resolved, err := app.readProjectIn(directory, false)
			if err == nil || resolved.Auth != nil || resolved.Resource != "" {
				t.Fatalf("mismatched site origin inherited profile credentials: %+v %v", resolved, err)
			}
		})
	}
}

func TestProjectPinnedSiteBaseNormalizedOrigins(t *testing.T) {
	t.Setenv("HEX_TOKEN", "")
	for _, test := range []struct{ saved, pinned, normalized, site string }{
		{"https://sites.example.com:443/", "https://SITES.example.com:443/", "https://sites.example.com", "https://demo.sites.example.com/"},
		{"http://localhost:80/", "http://localhost:80/", "http://localhost", "http://demo.localhost/"},
	} {
		t.Run(test.pinned, func(t *testing.T) {
			directory := t.TempDir()
			t.Setenv("HEX_CONFIG_DIR", filepath.Join(directory, "profiles"))
			connection := localConnection("https://saved.example.com")
			connection.SiteBaseURL = test.saved
			if _, err := saveProfile(connection, "company"); err != nil {
				t.Fatal(err)
			}
			if err := writeJSONFile(filepath.Join(directory, "hex.json"), Project{Name: "demo", Platform: "company", Server: connection.Server, SiteBaseURL: test.pinned}); err != nil {
				t.Fatal(err)
			}
			app, output := testApp(t, directory)
			project, err := app.readProjectIn(directory, false)
			if err != nil || project.SiteBaseURL != test.normalized || project.Auth == nil || project.Auth.Type != "none" {
				t.Fatalf("equivalent site origin lost profile settings: %+v %v", project, err)
			}
			requests := 0
			app.HTTP.Transport = cliRoundTripFunc(func(request *http.Request) (*http.Response, error) {
				requests++
				if request.URL.String() != test.site {
					t.Errorf("unexpected fetch destination: %s", request.URL)
				}
				return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("page"))}, nil
			})
			if err := app.Execute(context.Background(), []string{"fetch", "--site", "demo"}, "test"); err != nil {
				t.Fatal(err)
			}
			if requests != 1 || output.String() != "page" {
				t.Fatalf("normalized site fetch failed: requests=%d output=%q", requests, output.String())
			}
		})
	}
}

func TestProjectPinnedSiteBaseRequiresHTTPSOrLoopback(t *testing.T) {
	directory := t.TempDir()
	t.Setenv("HEX_CONFIG_DIR", filepath.Join(directory, "profiles"))
	connection := localConnection("https://saved.example.com")
	connection.SiteBaseURL = "https://sites.example.com"
	if _, err := saveProfile(connection, "company"); err != nil {
		t.Fatal(err)
	}
	if err := writeJSONFile(filepath.Join(directory, "hex.json"), Project{Name: "demo", Platform: "company", SiteBaseURL: "http://sites.example.com"}); err != nil {
		t.Fatal(err)
	}
	app, _ := testApp(t, directory)
	project, err := app.readProjectIn(directory, false)
	if err == nil || !strings.Contains(err.Error(), "use HTTPS") || project.Auth != nil {
		t.Fatalf("remote plaintext site origin accepted: %+v %v", project, err)
	}
}

func TestExplicitPlatformOverridesMismatchedProjectPin(t *testing.T) {
	directory := t.TempDir()
	t.Setenv("HEX_CONFIG_DIR", filepath.Join(directory, "profiles"))
	requests := 0
	live := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.Header.Get("Authorization") != "" {
			t.Error("override inherited credentials from the pinned profile")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `[]`)
	}))
	defer live.Close()
	t.Setenv("HEX_TOKEN", "")
	pinned := localConnection("https://pinned.example.com")
	pinned.Resource = "api://pinned-resource"
	if _, err := saveProfile(pinned, "pinned"); err != nil {
		t.Fatal(err)
	}
	if _, err := saveProfile(localConnection(live.URL), "selected"); err != nil {
		t.Fatal(err)
	}
	if err := writeJSONFile(filepath.Join(directory, "hex.json"), Project{Name: "demo", Platform: "pinned", Server: "https://foreign.example.com", SiteBaseURL: "https://foreign-sites.example.com", Title: "Keep metadata"}); err != nil {
		t.Fatal(err)
	}
	app, _ := testApp(t, directory)
	for _, needsProject := range []bool{false, true} {
		project, err := app.commandConfig("selected", needsProject)
		if err != nil || project.Server != live.URL || project.SiteBaseURL != "http://localhost:8080" || project.Resource != "" || project.Auth == nil || project.Auth.Type != "none" {
			t.Fatalf("explicit platform did not replace pinned connection: %+v %v", project, err)
		}
		if needsProject && (project.Name != "demo" || project.Title != "Keep metadata") {
			t.Fatalf("override lost project metadata: %+v", project)
		}
	}
	run(t, directory, "sites", "--platform", "selected")
	if requests != 1 {
		t.Fatalf("override did not reach selected server: %d requests", requests)
	}
}

func TestStandaloneLegacyProjectKeepsExplicitServerAndResource(t *testing.T) {
	directory := t.TempDir()
	t.Setenv("HEX_CONFIG_DIR", filepath.Join(directory, "no-profiles"))
	expected := Project{Name: "demo", Server: "https://legacy.example.com", SiteBaseURL: "https://legacy-sites.example.com", Resource: "api://legacy-resource"}
	if err := writeJSONFile(filepath.Join(directory, "hex.json"), expected); err != nil {
		t.Fatal(err)
	}
	app, _ := testApp(t, directory)
	project, err := app.commandConfig("", true)
	if err != nil || project.Server != expected.Server || project.SiteBaseURL != expected.SiteBaseURL || project.Resource != expected.Resource || project.Auth != nil {
		t.Fatalf("standalone legacy settings changed: %+v %v", project, err)
	}
}

func TestAPISendRejectsRemoteHTTPBeforeTokenAcquisition(t *testing.T) {
	t.Setenv("HEX_TOKEN", "")
	requests := 0
	issuer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
	}))
	defer issuer.Close()
	app, _ := testApp(t, t.TempDir())
	app.HTTP.Transport = cliRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		requests++
		return nil, errors.New("unexpected HTTP request")
	})
	project := Project{Server: "http://foreign.example.com", Auth: &hex.AuthConfig{Type: "oidc", Issuer: issuer.URL, ClientID: "hex-cli", Scopes: []string{"openid"}}}
	if _, err := app.apiRequest(context.Background(), project, "/api/sites"); err == nil || !strings.Contains(err.Error(), "use HTTPS") {
		t.Fatalf("remote plaintext API accepted: %v", err)
	}
	if requests != 0 {
		t.Fatal("remote HTTP validation happened after authentication or a request")
	}
}

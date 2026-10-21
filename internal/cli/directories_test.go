package cli

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	hex "github.com/crazycatviking/hex/server"
	"golang.org/x/oauth2"
)

type directoryFixture struct {
	directory           workspaceDirectory
	entries             []workspaceEntry
	server              *httptest.Server
	revoked             atomic.Bool
	mismatch            atomic.Bool
	redirect            atomic.Bool
	catalogRequests     atomic.Int32
	workspaceRequests   atomic.Int32
	developmentRequests atomic.Int32
	productionRequests  atomic.Int32
	unexpectedRequests  atomic.Int32
}

func newDirectoryFixture(t *testing.T) *directoryFixture {
	t.Helper()
	f := &directoryFixture{}
	auth := &hex.AuthConfig{Type: "oidc", Issuer: "https://identity.example/realm", ClientID: "hex-cli", Scopes: []string{"openid", "profile", "offline_access"}}
	for _, id := range []string{"development", "production"} {
		workspace := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			f.workspaceRequests.Add(1)
			if id == "development" {
				f.developmentRequests.Add(1)
			} else {
				f.productionRequests.Add(1)
			}
			if r.Header.Get("Authorization") != "Bearer personal-token" && r.Header.Get("Authorization") != "Bearer operator-token" {
				t.Error("workspace did not receive the selected account token")
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			if strings.HasSuffix(r.URL.Path, "/publish") {
				if err := json.NewEncoder(w).Encode(publishPlan{Uploads: []uploadTarget{}, Unchanged: 1}); err != nil {
					t.Error(err)
				}
				return
			}
			if strings.HasSuffix(r.URL.Path, "/publish/complete") {
				if err := json.NewEncoder(w).Encode(publishResult{URL: "https://" + id + ".example/demo"}); err != nil {
					t.Error(err)
				}
				return
			}
			if err := json.NewEncoder(w).Encode(map[string]string{"workspace": id, "identity": r.Header.Get("Authorization")}); err != nil {
				t.Error(err)
			}
		}))
		t.Cleanup(workspace.Close)
		connection := localConnection(workspace.URL)
		connection.SiteBaseURL, connection.Auth = strings.Replace(workspace.URL, "127.0.0.1", "localhost", 1), auth
		data, err := json.Marshal(connection)
		if err != nil {
			t.Fatal(err)
		}
		f.entries = append(f.entries, workspaceEntry{ID: id, Name: id, Organization: "acme", OrganizationName: "Acme", Status: "running", Role: "member", Connection: data})
	}
	f.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == connectionPath {
			if r.Header.Get("Authorization") != "" {
				t.Error("public setup sent credentials")
			}
			if err := json.NewEncoder(w).Encode(f.directory); err != nil {
				t.Error(err)
			}
			return
		}
		if r.URL.Path != "/api/hex/workspaces" {
			f.unexpectedRequests.Add(1)
			http.NotFound(w, r)
			return
		}
		f.catalogRequests.Add(1)
		if r.Header.Get("Authorization") != "Bearer personal-token" && r.Header.Get("Authorization") != "Bearer operator-token" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if f.redirect.Load() {
			http.Redirect(w, r, f.server.URL+"/not-a-catalog", http.StatusTemporaryRedirect)
			return
		}
		entries := append([]workspaceEntry{}, f.entries...)
		if f.revoked.Load() {
			entries = []workspaceEntry{}
		}
		if f.mismatch.Load() {
			connection := localConnection("https://untrusted.example")
			connection.Auth = &hex.AuthConfig{Type: "oidc", Issuer: auth.Issuer, ClientID: "another-client", Scopes: auth.Scopes}
			data, err := json.Marshal(connection)
			if err != nil {
				t.Error(err)
				return
			}
			entries[0].Connection = data
		}
		if err := json.NewEncoder(w).Encode(entries); err != nil {
			t.Error(err)
		}
	}))
	t.Cleanup(f.server.Close)
	f.directory = workspaceDirectory{Version: 1, Kind: "workspace-directory", Name: "Example directory", Server: f.server.URL, Auth: auth, WorkspacesPath: "/api/hex/workspaces"}
	return f
}

func (f *directoryFixture) setup(t *testing.T, app *App, name string) {
	t.Helper()
	result, err := app.setup(context.Background(), f.server.URL, "", name, false)
	if err != nil || result.Kind != "workspace-directory" || result.Status != "ready" {
		t.Fatalf("directory setup: %+v, %v", result, err)
	}
}

func seedDirectoryAccount(t *testing.T, directory workspaceDirectory, account, token string) string {
	t.Helper()
	file, err := oidcSessionPath(directoryProject(directory, account))
	if err != nil {
		t.Fatal(err)
	}
	if err := saveOIDCSession(file, oidcSession{Username: account, Token: &oauth2.Token{AccessToken: token, Expiry: time.Now().Add(time.Hour)}}); err != nil {
		t.Fatal(err)
	}
	return file
}

func TestDirectorySetupPreservesDirectProfilesAndDefaults(t *testing.T) {
	t.Setenv("HEX_CONFIG_DIR", t.TempDir())
	t.Setenv("HEX_TOKEN", "")
	connection := localConnection("http://localhost:8080")
	if _, err := saveProfile(connection, "direct"); err != nil {
		t.Fatal(err)
	}
	_, profilesFile, _ := readProfiles()
	before, err := os.ReadFile(profilesFile)
	if err != nil {
		t.Fatal(err)
	}
	app, _ := testApp(t, t.TempDir())
	f := newDirectoryFixture(t)
	f.setup(t, app, "example")
	after, err := os.ReadFile(profilesFile)
	if err != nil || string(before) != string(after) {
		t.Fatal("directory setup changed direct profiles", err)
	}
	project, err := app.commandConfig("", false)
	if err != nil || project.Server != connection.Server || project.directorySession != nil {
		t.Fatal("direct default changed", project, err)
	}
	project, err = app.authCommandConfig("")
	if err != nil || project.directorySession != nil {
		t.Fatal("direct login changed", err)
	}
	if f.catalogRequests.Load() != 0 {
		t.Fatal("setup or direct commands queried the directory")
	}
}

func TestDirectorySelectionSharesOnlyItsAccountSession(t *testing.T) {
	t.Setenv("HEX_CONFIG_DIR", t.TempDir())
	t.Setenv("HEX_TOKEN", "")
	app, output := testApp(t, t.TempDir())
	f := newDirectoryFixture(t)
	f.setup(t, app, "example")
	seedDirectoryAccount(t, f.directory, "personal", "personal-token")
	seedDirectoryAccount(t, f.directory, "operator", "operator-token")
	if err := app.Execute(context.Background(), []string{"workspace", "list", "--directory", "example", "--account", "personal", "--json"}, "test"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "production") {
		t.Fatal(output.String())
	}
	if err := app.Execute(context.Background(), []string{"workspace", "use", "acme/development", "--directory", "example", "--account", "personal"}, "test"); err != nil {
		t.Fatal(err)
	}
	first, err := app.commandConfig("", false)
	if err != nil {
		t.Fatal(err)
	}
	firstPath, err := oidcSessionPath(first)
	if err != nil {
		t.Fatal(err)
	}
	if err := app.Execute(context.Background(), []string{"whoami", "--workspace", "production"}, "test"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), `"workspace": "production"`) || !strings.Contains(output.String(), "personal-token") {
		t.Fatal(output.String())
	}
	second, err := app.commandConfig("", false)
	if err != nil {
		t.Fatal(err)
	}
	secondPath, _ := oidcSessionPath(second)
	if firstPath != secondPath {
		t.Fatal("workspaces did not share directory sign-in")
	}
	app.account = "operator"
	operator, err := app.commandConfig("", false)
	if err != nil {
		t.Fatal(err)
	}
	operatorPath, _ := oidcSessionPath(operator)
	if operatorPath == firstPath {
		t.Fatal("account aliases share credentials")
	}
	direct := first
	direct.directorySession = nil
	directPath, _ := oidcSessionPath(direct)
	if directPath == firstPath {
		t.Fatal("direct and directory sessions share credentials")
	}
	other := newDirectoryFixture(t)
	otherPath, _ := oidcSessionPath(directoryProject(other.directory, "personal"))
	if otherPath == firstPath {
		t.Fatal("different directories share credentials despite identical issuer/client/scopes")
	}
}

func TestDirectoryProjectPinsBeatGlobalSelectionAndPreserveProjectData(t *testing.T) {
	t.Setenv("HEX_CONFIG_DIR", t.TempDir())
	t.Setenv("HEX_TOKEN", "")
	app, _ := testApp(t, t.TempDir())
	f := newDirectoryFixture(t)
	f.setup(t, app, "example")
	seedDirectoryAccount(t, f.directory, "default", "personal-token")
	projectFile := filepath.Join(app.Dir, "hex.json")
	if err := os.WriteFile(projectFile, []byte(`{"name":"demo","custom":{"keep":true}}`), 0644); err != nil {
		t.Fatal(err)
	}
	if err := app.Execute(context.Background(), []string{"workspace", "use", "development", "--project"}, "test"); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(projectFile)
	if !strings.Contains(string(data), `"keep": true`) || strings.Contains(string(data), "personal-token") {
		t.Fatal(string(data))
	}
	if err := app.Execute(context.Background(), []string{"workspace", "use", "production"}, "test"); err != nil {
		t.Fatal(err)
	}
	project, err := app.commandConfig("", false)
	if err != nil || project.Workspace.ID != "development" {
		t.Fatal("global use replaced project pin", err)
	}
	app.workspace = "production"
	project, err = app.commandConfig("", false)
	if err != nil || project.Workspace.ID != "production" {
		t.Fatal("explicit workspace did not override project pin", err)
	}
	app.workspace = ""
	before, _ := os.ReadFile(projectFile)
	if err := app.Execute(context.Background(), []string{"workspace", "list", "--json"}, "test"); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(projectFile)
	if string(before) != string(after) {
		t.Fatal("listing changed project destination")
	}
}

func TestDirectoryPublishingHonorsPinAndExplicitOverride(t *testing.T) {
	t.Setenv("HEX_CONFIG_DIR", t.TempDir())
	t.Setenv("HEX_TOKEN", "")
	app, output := testApp(t, t.TempDir())
	f := newDirectoryFixture(t)
	f.setup(t, app, "example")
	seedDirectoryAccount(t, f.directory, "default", "personal-token")
	if err := os.WriteFile(filepath.Join(app.Dir, "hex.json"), []byte(`{"name":"demo"}`), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(app.Dir, "index.html"), []byte("<h1>Demo</h1>"), 0644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"workspace", "use", "development", "--project"},
		{"workspace", "use", "production"},
		{"publish"},
	} {
		if err := app.Execute(context.Background(), args, "test"); err != nil {
			t.Fatalf("%v: %v", args, err)
		}
	}
	if f.developmentRequests.Load() != 2 || f.productionRequests.Load() != 0 || !strings.Contains(output.String(), "https://development.example/demo") {
		t.Fatal("publish ignored project pin", output.String())
	}
	if err := app.Execute(context.Background(), []string{"publish", "--workspace", "production"}, "test"); err != nil {
		t.Fatal(err)
	}
	if f.developmentRequests.Load() != 2 || f.productionRequests.Load() != 2 || !strings.Contains(output.String(), "https://production.example/demo") {
		t.Fatal("publish ignored explicit workspace", output.String())
	}
	if err := app.Execute(context.Background(), []string{"publish", "--server", "https://untrusted.example"}, "test"); err == nil {
		t.Fatal("publish redirected shared credentials")
	}
}

func TestDirectoryRevocationAndOverridesFailBeforeWorkspaceRequests(t *testing.T) {
	t.Setenv("HEX_CONFIG_DIR", t.TempDir())
	t.Setenv("HEX_TOKEN", "")
	app, _ := testApp(t, t.TempDir())
	f := newDirectoryFixture(t)
	f.setup(t, app, "example")
	seedDirectoryAccount(t, f.directory, "default", "personal-token")
	app.workspace = "development"
	project, err := app.commandConfig("", false)
	if err != nil {
		t.Fatal(err)
	}
	project.Server = "https://untrusted.example"
	if _, err := app.apiToken(context.Background(), project); err == nil {
		t.Fatal("directory credential accepted for an overridden origin")
	}
	f.mismatch.Store(true)
	if _, err := app.commandConfig("", false); err == nil {
		t.Fatal("different workspace auth accepted")
	}
	f.mismatch.Store(false)
	f.revoked.Store(true)
	if _, err := app.commandConfig("", false); err == nil {
		t.Fatal("revoked workspace still selectable")
	}
	if f.workspaceRequests.Load() != 0 {
		t.Fatal("unauthorized workspace was contacted")
	}
}

func TestDirectoryLogoutWorksWithoutMembershipAndOnlyRemovesSelectedAlias(t *testing.T) {
	t.Setenv("HEX_CONFIG_DIR", t.TempDir())
	t.Setenv("HEX_TOKEN", "")
	app, _ := testApp(t, t.TempDir())
	f := newDirectoryFixture(t)
	f.setup(t, app, "example")
	personal := seedDirectoryAccount(t, f.directory, "personal", "personal-token")
	operator := seedDirectoryAccount(t, f.directory, "operator", "operator-token")
	f.revoked.Store(true)
	if err := app.Execute(context.Background(), []string{"logout", "--directory", "example", "--account", "personal"}, "test"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(personal); !os.IsNotExist(err) {
		t.Fatal("selected session not removed", err)
	}
	if _, err := os.Stat(operator); err != nil {
		t.Fatal("other alias was removed", err)
	}
	if f.catalogRequests.Load() != 0 {
		t.Fatal("logout requires workspace membership")
	}
}

func TestDirectoryLoginBeforeSelectionAndNoninteractiveMissingSession(t *testing.T) {
	t.Setenv("HEX_CONFIG_DIR", t.TempDir())
	t.Setenv("HEX_TOKEN", "")
	app, _ := testApp(t, t.TempDir())
	f := newDirectoryFixture(t)
	f.setup(t, app, "example")
	project, err := app.authCommandConfig("")
	if err != nil || project.directorySession == nil || project.Server != f.server.URL {
		t.Fatal("cannot log in before selecting a workspace", err)
	}
	if err := app.Execute(context.Background(), []string{"workspace", "list"}, "test"); err == nil || !strings.Contains(err.Error(), "hex login") {
		t.Fatal("missing noninteractive login was not reported", err)
	}
	if f.catalogRequests.Load() != 0 {
		t.Fatal("directory contacted without credentials")
	}
}

func TestDirectorySetupAndCatalogDoNotFollowRedirects(t *testing.T) {
	t.Setenv("HEX_CONFIG_DIR", t.TempDir())
	t.Setenv("HEX_TOKEN", "")
	app, _ := testApp(t, t.TempDir())
	app.HTTP = &http.Client{Timeout: 5 * time.Second}
	f := newDirectoryFixture(t)
	f.setup(t, app, "example")
	seedDirectoryAccount(t, f.directory, "default", "personal-token")
	f.redirect.Store(true)
	if _, err := app.workspaceEntries(context.Background(), f.directory, "default"); err == nil {
		t.Fatal("followed a catalog redirect")
	}
	if f.unexpectedRequests.Load() != 0 {
		t.Fatal("catalog redirect was followed")
	}
	var redirected atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { redirected.Add(1); w.WriteHeader(500) }))
	defer target.Close()
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, target.URL, http.StatusFound) }))
	defer source.Close()
	result, err := app.setup(context.Background(), source.URL, "", "redirecting", false)
	if err != nil || result.Status != "download_required" || redirected.Load() != 0 {
		t.Fatal("setup followed redirect", result, err)
	}
}

func TestDirectorySelectionDoesNotGuessAWorkspaceOrCrossPinnedDirectories(t *testing.T) {
	t.Setenv("HEX_CONFIG_DIR", t.TempDir())
	t.Setenv("HEX_TOKEN", "")
	app, _ := testApp(t, t.TempDir())
	first, second := newDirectoryFixture(t), newDirectoryFixture(t)
	first.setup(t, app, "first")
	second.setup(t, app, "second")
	seedDirectoryAccount(t, first.directory, "default", "personal-token")
	seedDirectoryAccount(t, second.directory, "default", "personal-token")
	if _, err := app.commandConfig("", false); err == nil || !strings.Contains(err.Error(), "no workspace selected") {
		t.Fatal("directory guessed a workspace", err)
	}
	if err := app.Execute(context.Background(), []string{"workspace", "use", "development", "--directory", "first"}, "test"); err != nil {
		t.Fatal(err)
	}
	app.directory = "second"
	if _, err := app.commandConfig("", false); err == nil {
		t.Fatal("directory flag silently retargeted active workspace")
	}
	if second.catalogRequests.Load() != 0 {
		t.Fatal("other directory contacted without explicit workspace")
	}
	app.directory = ""
	if err := os.WriteFile(filepath.Join(app.Dir, "hex.json"), []byte(`{"name":"demo","workspace":{"directory":"`+first.server.URL+`","id":"production"}}`), 0644); err != nil {
		t.Fatal(err)
	}
	app.directory = "second"
	if _, err := app.commandConfig("", false); err == nil {
		t.Fatal("directory flag silently retargeted pinned workspace")
	}
	app.workspace = "production"
	project, err := app.commandConfig("", false)
	if err != nil || project.Workspace.Directory != second.server.URL {
		t.Fatal("explicit directory/workspace override failed", err)
	}
}

func TestDirectoryValidationAndOriginReplacement(t *testing.T) {
	t.Setenv("HEX_CONFIG_DIR", t.TempDir())
	f := newDirectoryFixture(t)
	for _, endpoint := range []string{"https://evil.example/api/workspaces", "//evil.example/api/workspaces", "/api/../workspaces", "/api/workspaces?next=evil", "/api/%2fworkspaces"} {
		directory := f.directory
		directory.WorkspacesPath = endpoint
		if err := directory.validate(directory.Server); err == nil {
			t.Fatal("accepted unsafe directory API path", endpoint)
		}
	}
	data, _ := json.Marshal(f.directory)
	if _, handled, err := saveDirectory(data, "https://another.example", "example"); !handled || err == nil {
		t.Fatal("accepted different discovery origin")
	}
	if _, _, err := saveDirectory(data, f.server.URL, "example"); err != nil {
		t.Fatal(err)
	}
	changed := f.directory
	changed.Server = "https://another.example"
	data, _ = json.Marshal(changed)
	if _, _, err := saveDirectory(data, changed.Server, "example"); err == nil {
		t.Fatal("silently replaced trusted directory origin")
	}
}

func TestDirectSetupExplicitlyRestoresDirectDefault(t *testing.T) {
	t.Setenv("HEX_CONFIG_DIR", t.TempDir())
	t.Setenv("HEX_TOKEN", "")
	app, _ := testApp(t, t.TempDir())
	f := newDirectoryFixture(t)
	f.setup(t, app, "example")
	seedDirectoryAccount(t, f.directory, "default", "personal-token")
	if err := app.Execute(context.Background(), []string{"workspace", "use", "development"}, "test"); err != nil {
		t.Fatal(err)
	}
	direct := localConnection("http://localhost:8080")
	data, _ := json.Marshal(direct)
	file := filepath.Join(t.TempDir(), "direct.json")
	if err := os.WriteFile(file, data, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := app.setup(context.Background(), "", file, "direct", false); err != nil {
		t.Fatal(err)
	}
	project, err := app.commandConfig("", false)
	if err != nil || project.directorySession != nil || project.Server != direct.Server {
		t.Fatal("direct setup did not restore direct behavior", err)
	}
}

func TestDirectProjectPinAndExplicitPlatformRemainIndependent(t *testing.T) {
	t.Setenv("HEX_CONFIG_DIR", t.TempDir())
	t.Setenv("HEX_TOKEN", "")
	app, _ := testApp(t, t.TempDir())
	f := newDirectoryFixture(t)
	f.setup(t, app, "example")
	seedDirectoryAccount(t, f.directory, "default", "personal-token")
	if _, err := saveProfile(localConnection("http://localhost:8080"), "direct"); err != nil {
		t.Fatal(err)
	}
	if err := app.Execute(context.Background(), []string{"workspace", "use", "development"}, "test"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(app.Dir, "hex.json"), []byte(`{"name":"demo","platform":"direct"}`), 0644); err != nil {
		t.Fatal(err)
	}
	project, err := app.commandConfig("", false)
	if err != nil || project.Server != "http://localhost:8080" || project.directorySession != nil {
		t.Fatal("directory default overrode direct project pin", err)
	}
	before := f.catalogRequests.Load()
	if _, err := app.commandConfig("direct", false); err != nil {
		t.Fatal(err)
	}
	if f.catalogRequests.Load() != before {
		t.Fatal("explicit direct profile queried the directory")
	}
	if err := app.Execute(context.Background(), []string{"workspace", "use", "production", "--project"}, "test"); err == nil {
		t.Fatal("silently rewrote direct project pin")
	}
}

func TestDirectoryInitPinsStableWorkspaceWithoutAccountCredentials(t *testing.T) {
	t.Setenv("HEX_CONFIG_DIR", t.TempDir())
	t.Setenv("HEX_TOKEN", "")
	app, _ := testApp(t, t.TempDir())
	f := newDirectoryFixture(t)
	f.setup(t, app, "example")
	seedDirectoryAccount(t, f.directory, "personal", "personal-token")
	target := filepath.Join(app.Dir, "demo")
	if err := app.Execute(context.Background(), []string{"init", target, "--workspace", "development", "--account", "personal"}, "test"); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(target, "hex.json"))
	if err != nil {
		t.Fatal(err)
	}
	var project Project
	if err := json.Unmarshal(data, &project); err != nil {
		t.Fatal(err)
	}
	if project.Workspace == nil || project.Workspace.ID != "development" || project.Workspace.Directory != f.server.URL || strings.Contains(string(data), "personal") {
		t.Fatal("project pin contains account details or lacks stable identity", string(data))
	}
}

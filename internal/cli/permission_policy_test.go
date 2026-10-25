package cli

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	hex "github.com/crazycatviking/hex/server"
	"github.com/crazycatviking/hex/server/providers/memory"
)

func TestPublishingPermissionPoliciesRequireVersionTwoAndExplicitBaseline(t *testing.T) {
	directory := t.TempDir()
	t.Setenv("HEX_CONFIG_DIR", filepath.Join(directory, "profiles"))
	policies := memory.NewAccessStore()
	platform := startPlatform(t, func(config *hex.Config) {
		config.Identity = hex.StaticIdentity{Identity: hex.Identity{ID: "alex"}}
		config.Access = policies
	})
	project := filepath.Join(directory, "project")
	run(t, directory, "init", project, "--name", "demo", "--server", platform.URL)
	createBuild(t, project)
	config := Project{Name: "demo", Server: platform.URL, Access: json.RawMessage(`{"viewers":[]}`)}
	if err := writeJSONFile(filepath.Join(project, "hex.json"), config); err != nil {
		t.Fatal(err)
	}
	app, _ := testApp(t, project)
	err := app.Execute(context.Background(), []string{"publish"}, "test")
	if err == nil || !strings.Contains(err.Error(), "legacy permission configuration") {
		t.Fatalf("legacy publishing not rejected: %v", err)
	}
	for _, guidance := range []string{"upgrade it with 'hex update'", "rerun the platform installer", "hex access show <site>", "copy the migrated policy into hex.json", "Upgrading the CLI alone does not migrate hex.json", "Changing only the version number is not sufficient"} {
		if !strings.Contains(err.Error(), guidance) {
			t.Fatalf("legacy publication error missing %q: %v", guidance, err)
		}
	}
	if _, err := policies.GetSiteAccess(t.Context(), "demo"); !errors.Is(err, hex.ErrNotFound) {
		t.Fatalf("legacy publication claimed ownership: %v", err)
	}
	policy := hex.MigrateSiteAccess(hex.SiteAccess{Owners: []string{"user:alex"}, Viewers: []string{"user:alex"}})
	if err := policies.PutSiteAccess(t.Context(), "demo", policy); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(policy)
	if err != nil {
		t.Fatal(err)
	}
	config.Access = data
	if err := writeJSONFile(filepath.Join(project, "hex.json"), config); err != nil {
		t.Fatal(err)
	}
	app, _ = testApp(t, project)
	err = app.Execute(t.Context(), []string{"publish"}, "test")
	if err == nil || !strings.Contains(err.Error(), "no recorded local permission baseline") {
		t.Fatalf("missing baseline silently guessed: %v", err)
	}
	if err := os.WriteFile(filepath.Join(project, "policy.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	run(t, project, "access", "reconcile", "demo", "--file", "policy.json", "--server", platform.URL)
	run(t, project, "publish")
	stored, err := policies.GetSiteAccess(t.Context(), "demo")
	if err != nil || stored.Published == nil || stored.Published.Version != 2 {
		t.Fatalf("baseline not recorded: %+v %v", stored, err)
	}
}

func TestPermissionPoliciesNeverReachAnOlderServer(t *testing.T) {
	t.Setenv("HEX_CONFIG_DIR", t.TempDir())
	for _, scenario := range []struct {
		name   string
		status int
		body   string
	}{
		{"missing version", http.StatusOK, `{"version":1,"sites":true,"files":true,"database":true,"realtime":false,"maxUploadBytes":1024,"accessControl":true,"publishing":true}`},
		{"old version", http.StatusOK, `{"version":1,"sites":true,"files":true,"database":true,"realtime":false,"maxUploadBytes":1024,"accessControl":true,"publishing":true,"permissionVersion":1}`},
		{"missing capabilities endpoint", http.StatusNotFound, `{"error":"not found"}`},
		{"authentication required", http.StatusUnauthorized, `{"error":"original failure"}`},
		{"permission denied", http.StatusForbidden, `{"error":"original failure"}`},
		{"server outage", http.StatusInternalServerError, `{"error":"original failure"}`},
	} {
		for _, operation := range []string{"publish", "set", "reconcile"} {
			t.Run(scenario.name+"/"+operation, func(t *testing.T) {
				requests := 0
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					requests++
					if r.Method != http.MethodGet || r.URL.Path != "/api/hex/capabilities" {
						t.Errorf("unsafe request before version support: %s %s", r.Method, r.URL.Path)
					}
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(scenario.status)
					if _, err := w.Write([]byte(scenario.body)); err != nil {
						t.Error(err)
					}
				}))
				defer server.Close()
				directory := t.TempDir()
				// Cached capability claims must never bypass the live check.
				project := Project{Name: "demo", Server: server.URL, Capabilities: &Capabilities{PermissionVersion: 2}}
				if err := writeJSONFile(filepath.Join(directory, "hex.json"), project); err != nil {
					t.Fatal(err)
				}
				app, _ := testApp(t, directory)
				policy := json.RawMessage(`{"version":2}`)
				var err error
				switch operation {
				case "publish":
					_, err = app.publishFiles(t.Context(), project, "demo", nil, nil, policy, false)
				case "set":
					err = app.accessRequest(t.Context(), &accessOptions{}, "demo", http.MethodPut, "/access", policy)
				case "reconcile":
					err = app.accessRequest(t.Context(), &accessOptions{}, "demo", http.MethodPost, "/access/baseline", policy)
				}
				if err == nil {
					t.Fatal("policy write was not blocked")
				}
				if requests != 1 {
					t.Fatalf("expected only the capability request, got %d", requests)
				}
				if scenario.status == http.StatusOK || scenario.status == http.StatusNotFound {
					want := "this platform does not support version 2 permission policies; ask the platform operator to upgrade the Hex server/workspace runtime before publishing or changing access. Updating the CLI alone will not fix this. No policy was sent. Sending the policy to an older server could open the site"
					if err.Error() != want {
						t.Fatalf("unexpected upgrade guidance: %v", err)
					}
				} else {
					var status *apiStatusError
					if !errors.As(err, &status) || status.Status != scenario.status || !strings.Contains(err.Error(), "original failure") || strings.Contains(err.Error(), "upgrade") {
						t.Fatalf("real failure replaced with upgrade advice: %v", err)
					}
				}
			})
		}
	}
}

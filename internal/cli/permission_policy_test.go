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
	writes := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writes++
		}
		if r.URL.Path != "/api/hex/capabilities" {
			t.Errorf("unexpected request to older server: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		if _, err := w.Write([]byte(`{"version":1,"sites":true,"files":true,"database":true,"realtime":false,"maxUploadBytes":1024,"accessControl":true,"publishing":true}`)); err != nil {
			t.Error(err)
		}
	}))
	defer server.Close()
	app, _ := testApp(t, t.TempDir())
	_, err := app.publishFiles(t.Context(), Project{Server: server.URL}, "demo", nil, nil, json.RawMessage(`{"version":2}`), false)
	if err == nil || !strings.Contains(err.Error(), "does not support version 2") {
		t.Fatalf("old server accepted a new policy: %v", err)
	}
	if writes != 0 {
		t.Fatal("new policy was sent to a server that could interpret it as open legacy access")
	}
}

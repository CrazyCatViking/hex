package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	hex "github.com/crazycatviking/hex/server"
)

func legacyProfileDocument(t *testing.T, connection Connection) json.RawMessage {
	t.Helper()
	data, err := json.Marshal(connection)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestLoadingProfilesAutomaticallyMigratesLegacyAuth(t *testing.T) {
	directory := t.TempDir()
	t.Setenv("HEX_CONFIG_DIR", directory)
	company := localConnection("https://hex.example.com")
	company.Resource = "api://" + testClientID
	company.ClientID = testClientID
	company.TenantID = testTenantID
	company.CLIReleaseURL = "https://releases.example.com/hex"
	var companyDocument map[string]any
	if err := json.Unmarshal(legacyProfileDocument(t, company), &companyDocument); err != nil {
		t.Fatal(err)
	}
	companyDocument["capabilities"].(map[string]any)["futureCapability"] = map[string]any{"enabled": true}
	companyDocument["publishing"] = map[string]any{"provider": "azure-files", "url": "https://files.example.com/sites"}
	companyData, err := json.Marshal(companyDocument)
	if err != nil {
		t.Fatal(err)
	}
	local := localConnection("http://localhost:8080")
	modern := localConnection("https://other.example.com")
	modern.Auth = &hex.AuthConfig{Type: "oidc", Issuer: "https://identity.example.com", ClientID: "other-client", Scopes: []string{"openid", "api"}}
	store := profiles{
		Version: 1, DefaultProfile: "company",
		Profiles: map[string]json.RawMessage{
			"company": companyData,
			"local":   legacyProfileDocument(t, local),
			"modern":  legacyProfileDocument(t, modern),
			"future":  json.RawMessage(`{"version":99,"futureField":"keep-me"}`),
		},
	}
	path := filepath.Join(directory, "profiles.json")
	if err := writeJSONFile(path, store); err != nil {
		t.Fatal(err)
	}
	// Loading through a normal command also migrates the other saved profiles.
	app, output := testApp(t, t.TempDir())
	if err := app.Execute(context.Background(), []string{"capabilities"}, "test"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), `"sites": true`) {
		t.Fatal(output.String())
	}
	loaded, _, err := readProfiles()
	if err != nil {
		t.Fatal(err)
	}
	if loaded.DefaultProfile != "company" || len(loaded.Profiles) != 4 {
		t.Fatalf("profile selection or profiles lost: %+v", loaded)
	}
	var migrated map[string]any
	if err := json.Unmarshal(loaded.Profiles["company"], &migrated); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"resource", "clientId", "tenantId"} {
		if _, exists := migrated[key]; exists {
			t.Errorf("legacy field %s remains", key)
		}
	}
	expectedAuth := map[string]any{
		"type": "oidc", "issuer": "https://login.microsoftonline.com/" + testTenantID + "/v2.0", "clientId": testClientID,
		"scopes": []any{"openid", "profile", "offline_access", testClientID + "/.default"},
	}
	if !reflect.DeepEqual(migrated["auth"], expectedAuth) {
		t.Fatalf("unexpected migrated auth: %+v", migrated["auth"])
	}
	for key, value := range companyDocument {
		if key == "resource" || key == "clientId" || key == "tenantId" {
			continue
		}
		if !reflect.DeepEqual(value, migrated[key]) {
			t.Errorf("migration changed %s", key)
		}
	}
	localProfile, _, err := loadProfile("local", false)
	if err != nil || localProfile.Auth == nil || localProfile.Auth.Type != "none" {
		t.Fatalf("local auth not migrated: %+v %v", localProfile, err)
	}
	for _, name := range []string{"modern", "future"} {
		var before, after any
		if err := json.Unmarshal(store.Profiles[name], &before); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(loaded.Profiles[name], &after); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(before, after) {
			t.Errorf("migration changed profile %s", name)
		}
	}
	if _, _, err := loadProfile("future", false); err == nil {
		t.Fatal("unsupported profile became valid")
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0600 {
		t.Fatalf("profile file permissions: %v", info.Mode())
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	modified := time.Unix(1234567890, 0)
	if err := os.Chtimes(path, modified, modified); err != nil {
		t.Fatal(err)
	}
	if _, _, err := readProfiles(); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	info, err = os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) || !info.ModTime().Equal(modified) {
		t.Fatal("already-migrated profile store was rewritten")
	}
}

func TestLegacySetupAndResourcePinsUseMigratedAuth(t *testing.T) {
	t.Setenv("HEX_CONFIG_DIR", t.TempDir())
	for _, resource := range []string{"api://" + testClientID, "api://other-api/"} {
		t.Run(resource, func(t *testing.T) {
			connection := localConnection("https://hex.example.com")
			connection.Resource, connection.ClientID, connection.TenantID = resource, testClientID, testTenantID
			if _, err := saveProfile(connection, "company"); err != nil {
				t.Fatal(err)
			}
			loaded, _, err := loadProfile("company", false)
			if err != nil {
				t.Fatal(err)
			}
			if loaded.Auth == nil || loaded.Resource != "" || loaded.ClientID != "" || loaded.TenantID != "" {
				t.Fatalf("legacy setup did not save new auth: %+v", loaded)
			}
			directory := t.TempDir()
			if err := writeJSONFile(filepath.Join(directory, "hex.json"), Project{Name: "demo", Platform: "company", Resource: resource}); err != nil {
				t.Fatal(err)
			}
			app, _ := testApp(t, directory)
			project, err := app.commandConfig("", true)
			if err != nil || project.Auth == nil {
				t.Fatalf("resource pin lost sign-in app: %+v %v", project, err)
			}
			if override, err := project.withResource(strings.TrimRight(resource, "/")); err != nil || override.Auth == nil {
				t.Fatal("same API override lost sign-in app")
			}
			if override, err := project.withResource("api://different-api"); err == nil || !reflect.DeepEqual(override.Auth, project.Auth) {
				t.Fatal("different API override did not fail with configured auth preserved")
			}
		})
	}
}

func TestOfflineMigrationKeepsLegacySettingsWithoutAConcreteIssuer(t *testing.T) {
	t.Setenv("HEX_CONFIG_DIR", t.TempDir())
	for _, tenant := range []string{"", "company.example.com", "common", "organizations"} {
		t.Run(tenant, func(t *testing.T) {
			connection := localConnection("https://hex.example.com")
			connection.Resource = testResource
			if tenant != "" {
				connection.ClientID, connection.TenantID = testClientID, tenant
			}
			if _, err := saveProfile(connection, "company"); err != nil {
				t.Fatal(err)
			}
			loaded, _, err := loadProfile("company", false)
			if err != nil || loaded.Auth != nil || loaded.Resource != connection.Resource || loaded.ClientID != connection.ClientID || loaded.TenantID != tenant {
				t.Fatalf("inferred unavailable OIDC settings: %+v %v", loaded, err)
			}
		})
	}
}

func TestProfileMigrationWriteFailureLeavesOriginalStoreIntact(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("requires Unix directory permissions")
	}
	directory := filepath.Join(t.TempDir(), "profiles")
	t.Setenv("HEX_CONFIG_DIR", directory)
	path := filepath.Join(directory, "profiles.json")
	store := profiles{Version: 1, DefaultProfile: "local", Profiles: map[string]json.RawMessage{"local": legacyProfileDocument(t, localConnection("http://localhost:8080"))}}
	if err := writeJSONFile(path, store); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(directory, 0500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Chmod(directory, 0700); err != nil {
			t.Error(err)
		}
	})
	if _, _, err := readProfiles(); err == nil || !strings.Contains(err.Error(), "save migrated Hex profiles") {
		t.Fatalf("migration write failure not reported: %v", err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("failed migration changed original profile store")
	}
}

func TestGenericOIDCResourcePinPreservesProfileAuth(t *testing.T) {
	for _, auth := range []*hex.AuthConfig{
		{Type: "oidc", Issuer: "https://identity.example.com", ClientID: "hex-cli", Scopes: []string{"openid", "hex-api"}},
		{Type: "oidc", Issuer: "https://identity.example.com", ClientID: testClientID, Scopes: []string{"openid", "api://different-resource/.default"}},
		{Type: "none"},
	} {
		t.Run(auth.Type+"/"+auth.ClientID, func(t *testing.T) {
			directory := t.TempDir()
			t.Setenv("HEX_CONFIG_DIR", filepath.Join(directory, "profiles"))
			connection := localConnection("https://hex.example.com")
			connection.Auth = auth
			if _, err := saveProfile(connection, "company"); err != nil {
				t.Fatal(err)
			}
			if err := writeJSONFile(filepath.Join(directory, "hex.json"), Project{Name: "demo", Platform: "company", Resource: testResource}); err != nil {
				t.Fatal(err)
			}
			app, _ := testApp(t, directory)
			project, err := app.commandConfig("", true)
			if err != nil || !reflect.DeepEqual(project.Auth, auth) || project.Resource != "" || project.ClientID != "" || project.TenantID != "" {
				t.Fatalf("generic auth lost to legacy resource pin: %+v %v", project, err)
			}
			if updated, err := project.withResource(testResource); err == nil || !reflect.DeepEqual(updated.Auth, auth) {
				t.Fatalf("generic override discarded auth or failed to reject resource: %+v %v", updated, err)
			}
			if authMatchesResource(auth, testResource) {
				t.Fatal("generic OIDC scope inferred Entra auth")
			}
		})
	}
}

func TestGenericOIDCResourceOverrideFailsBeforeAuthentication(t *testing.T) {
	t.Setenv("HEX_TOKEN", "")
	directory := t.TempDir()
	t.Setenv("HEX_CONFIG_DIR", filepath.Join(directory, "profiles"))
	connection := localConnection("https://hex.example.com")
	connection.Auth = &hex.AuthConfig{Type: "oidc", Issuer: "https://identity.example.com", ClientID: "hex-cli", Scopes: []string{"openid", "hex-api"}}
	if _, err := saveProfile(connection, "company"); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"whoami", "--resource", testResource},
		{"access", "check", "demo", "--resource", testResource},
		{"capabilities", "--resource", testResource},
		{"capabilities", "--refresh", "--resource", testResource},
	} {
		app, _ := testApp(t, directory)
		err := app.Execute(context.Background(), args, "test")
		if err == nil || !strings.Contains(err.Error(), "incompatible with the configured auth scopes") {
			t.Fatalf("%v did not reject generic OIDC resource override: %v", args, err)
		}
	}
}

package cli

import (
	"context"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	hex "github.com/crazycatviking/hex/server"
)

func entraAuth(scopes ...string) *hex.AuthConfig {
	return &hex.AuthConfig{
		Type: "oidc", Issuer: "https://login.microsoftonline.com/" + testTenantID + "/v2.0",
		ClientID: testClientID, Scopes: append([]string{"openid", "profile", "offline_access"}, scopes...),
	}
}

func TestAuthResourceCompatibilityUsesConfiguredScopes(t *testing.T) {
	for _, test := range []struct {
		name     string
		auth     *hex.AuthConfig
		resource string
		want     bool
	}{
		{"default scope", entraAuth("api://reports/.default"), "api://reports", true},
		{"delegated scope", entraAuth("api://reports/access_as_user"), "api://reports", true},
		{"trailing slash", entraAuth("api://reports/read"), "api://reports/", true},
		{"other resource", entraAuth("api://reports/read"), "api://other", false},
		{"nearby resource", entraAuth("api://reports-other/read"), "api://reports", false},
		{"nested resource", entraAuth("api://reports/nested/read"), "api://reports", false},
		{"empty scope name", entraAuth("api://reports/"), "api://reports", false},
		{"identity scope", entraAuth(), "openid", false},
		{"empty resource", entraAuth("api://reports/read"), "", false},
		{"own GUID default", entraAuth(testClientID + "/.default"), "api://" + testClientID, true},
		{"own URI default", entraAuth("api://" + testClientID + "/.default"), testClientID, true},
		{"own GUID delegated", entraAuth(testClientID + "/read"), "api://" + testClientID, true},
		{"own URI delegated", entraAuth("api://" + testClientID + "/read"), testClientID, true},
		{"generic literal scope", &hex.AuthConfig{Type: "oidc", Issuer: "https://identity.example.com", ClientID: "public-client", Scopes: []string{"openid", "api://reports/read"}}, "api://reports", true},
		{"generic literal default", &hex.AuthConfig{Type: "oidc", Issuer: "https://identity.example.com", ClientID: "public-client", Scopes: []string{"openid", "api://reports/.default"}}, "api://reports", true},
		{"generic has no Entra alias", &hex.AuthConfig{Type: "oidc", Issuer: "https://identity.example.com", ClientID: testClientID, Scopes: []string{"openid", testClientID + "/.default"}}, "api://" + testClientID, false},
		{"URL resource path", entraAuth("https://api.example.com/v1/read"), "https://api.example.com/v1", true},
		{"anonymous profile", &hex.AuthConfig{Type: "none"}, "api://reports", false},
		{"legacy auth", nil, "api://reports", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := authMatchesResource(test.auth, test.resource); got != test.want {
				t.Fatalf("resource %q compatibility=%v, want %v", test.resource, got, test.want)
			}
		})
	}
}

func TestExplicitEntraDelegatedAuthSurvivesProjectResourcePins(t *testing.T) {
	const resource = "api://reports"
	auth := entraAuth(resource + "/access_as_user")
	expectedScopes := append([]string(nil), auth.Scopes...)
	for _, pin := range []string{"", resource, resource + "/", "api://different-api"} {
		t.Run("pin="+pin, func(t *testing.T) {
			directory := t.TempDir()
			t.Setenv("HEX_CONFIG_DIR", filepath.Join(directory, "profiles"))
			connection := localConnection("https://hex.example.com")
			connection.Auth = auth
			if _, err := saveProfile(connection, "company"); err != nil {
				t.Fatal(err)
			}
			if err := writeJSONFile(filepath.Join(directory, "hex.json"), Project{Name: "demo", Platform: "company", Resource: pin}); err != nil {
				t.Fatal(err)
			}
			app, _ := testApp(t, directory)
			project, err := app.commandConfig("", true)
			if err != nil || !reflect.DeepEqual(project.Auth, auth) || project.ClientID != "" || project.TenantID != "" {
				t.Fatalf("resource pin discarded explicit delegated auth: %+v %v", project, err)
			}
			wantResource := pin
			if pin == "api://different-api" {
				wantResource = ""
			}
			if project.Resource != wantResource {
				t.Fatalf("resolved resource %q, want %q", project.Resource, wantResource)
			}
			compatible, err := project.withResource(resource)
			if err != nil || compatible.Auth != project.Auth || !reflect.DeepEqual(compatible.Auth.Scopes, expectedScopes) {
				t.Fatalf("compatible override changed delegated auth: %+v %v", compatible, err)
			}
			incompatible, err := project.withResource("api://different-api")
			if err == nil || incompatible.Auth != project.Auth || incompatible.Resource != project.Resource {
				t.Fatalf("incompatible override changed auth or selected a resource: %+v %v", incompatible, err)
			}
			loaded, _, err := loadProfile("company", false)
			if err != nil || !reflect.DeepEqual(loaded.Auth, auth) || !reflect.DeepEqual(loaded.Auth.Scopes, expectedScopes) {
				t.Fatalf("resource resolution mutated the stored auth: %+v %v", loaded, err)
			}
		})
	}
}

func TestExplicitEntraIncompatibleOverrideFailsBeforeAuthentication(t *testing.T) {
	t.Setenv("HEX_TOKEN", "")
	directory := t.TempDir()
	t.Setenv("HEX_CONFIG_DIR", filepath.Join(directory, "profiles"))
	connection := localConnection("https://hex.example.com")
	connection.Auth = entraAuth("api://reports/access_as_user")
	if _, err := saveProfile(connection, "company"); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"whoami", "--resource", "api://other"},
		{"access", "check", "demo", "--resource", "api://other"},
		{"capabilities", "--resource", "api://other"},
		{"capabilities", "--refresh", "--resource", "api://other"},
	} {
		app, _ := testApp(t, directory)
		err := app.Execute(context.Background(), args, "test")
		if err == nil || !strings.Contains(err.Error(), "incompatible with the configured auth scopes") {
			t.Fatalf("%v did not reject the incompatible override before sign-in: %v", args, err)
		}
	}
}

func TestConfiguredAuthCannotBeBypassedByMatchingProjectResource(t *testing.T) {
	project := Project{Resource: "api://other", Auth: entraAuth("api://reports/read")}
	updated, err := project.withResource(project.Resource)
	if err == nil || updated.Auth != project.Auth {
		t.Fatalf("matching project resource bypassed scope compatibility: %+v %v", updated, err)
	}
}

func TestLegacyAuthResourceOverridesRemainSupported(t *testing.T) {
	project := Project{Resource: "api://legacy", ClientID: testClientID, TenantID: "organizations"}
	same, err := project.withResource(project.Resource)
	if err != nil || same.Auth != nil || same.ClientID != project.ClientID || same.TenantID != project.TenantID {
		t.Fatalf("same legacy resource lost Microsoft sign-in settings: %+v %v", same, err)
	}
	other, err := project.withResource("api://other")
	if err != nil || other.Auth != nil || other.Resource != "api://other" || other.ClientID != "" || other.TenantID != "" {
		t.Fatalf("legacy Azure CLI resource override was not retained: %+v %v", other, err)
	}
	resourceOnly := Project{Resource: "api://legacy"}
	if updated, err := resourceOnly.withResource("api://other"); err != nil || updated.Auth != nil || updated.Resource != "api://other" {
		t.Fatalf("resource-only Azure CLI profile changed: %+v %v", updated, err)
	}
}

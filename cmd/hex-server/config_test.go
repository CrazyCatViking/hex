package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	hex "github.com/crazycatviking/hex/server"
	"github.com/crazycatviking/hex/server/providers/postgres"
)

func TestDisabledProvidersDoNotFallback(t *testing.T) {
	values := map[string]string{
		"HEX_SITES_PROVIDER":    "none",
		"HEX_FILES_PROVIDER":    "none",
		"HEX_DATABASE_PROVIDER": "none",
		"HEX_REALTIME_PROVIDER": "none",
		"DATABASE_URL":          "not-a-real-database",
		"AZURE_BLOB_ENDPOINT":   "not-a-real-endpoint",
	}
	getenv := func(key string) string {
		return values[key]
	}

	config, cleanup, err := configure(context.Background(), getenv)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()

	s := hex.New(config)
	w := httptest.NewRecorder()
	s.ServeHTTP(w, httptest.NewRequest("GET", "/api/hex/capabilities", nil))

	var capabilities map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &capabilities); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"sites", "files", "database", "realtime"} {
		if capabilities[key] != false {
			t.Fatalf("%s unexpectedly enabled", key)
		}
	}

	paths := []string{
		"/api/sites",
		"/api/sites/demo/files",
		"/api/sites/demo/db/tasks",
		"/api/sites/demo/realtime/updates",
	}
	for _, path := range paths {
		w := httptest.NewRecorder()
		s.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		if w.Code != http.StatusNotFound {
			t.Fatalf("%s: %d", path, w.Code)
		}
	}
}

func TestSeparateAnalyticsDoesNotMoveAccessPolicies(t *testing.T) {
	documents := &postgres.Database{}
	analytics := &postgres.Database{}
	config := hex.Config{Database: documents, Analytics: analytics}
	configureIdentity(&config, providerSelection{identity: "easyauth"}, func(string) string { return "" })
	if config.Access != documents || config.People != documents {
		t.Fatal("a separate analytics connection must not move existing policies or the people directory")
	}
}

func TestExplicitProviderConfigurationFailsEarly(t *testing.T) {
	cases := []struct {
		variable string
		provider string
	}{
		{"HEX_FILES_PROVIDER", "azureblob"},
		{"HEX_DATABASE_PROVIDER", "postgres"},
		{"HEX_REALTIME_PROVIDER", "redis"},
		{"HEX_ANALYTICS_PROVIDER", "postgres"},
		{"HEX_ANALYTICS_PROVIDER", "unknown"},
	}

	for _, testCase := range cases {
		t.Run(testCase.variable, func(t *testing.T) {
			_, _, err := configure(context.Background(), func(key string) string {
				if key == testCase.variable {
					return testCase.provider
				}

				return ""
			})
			if err == nil {
				t.Fatal("expected configuration error")
			}
		})
	}
}

func TestAnalyticsProviderAndCollectorSelection(t *testing.T) {
	for _, provider := range []string{"none", "memory"} {
		t.Run(provider, func(t *testing.T) {
			values := map[string]string{
				"HEX_SITES_PROVIDER": "none", "HEX_DATABASE_PROVIDER": "none", "HEX_ANALYTICS_PROVIDER": provider,
				"HEX_FILES_PROVIDER": "none", "HEX_REALTIME_PROVIDER": "none",
			}
			if provider == "memory" {
				values["HEX_ANALYTICS_ADDR"] = "127.0.0.1:0"
			}
			config, cleanup, err := configure(context.Background(), func(key string) string { return values[key] })
			if err != nil {
				t.Fatal(err)
			}
			defer cleanup()
			if (config.Analytics != nil) != (provider == "memory") || (config.TrafficCollector != nil) != (provider == "memory") {
				t.Fatal("analytics selection ignored")
			}
		})
	}
}

func TestDurableAnalyticsWithoutAppDatabase(t *testing.T) {
	connection := os.Getenv("HEX_TEST_POSTGRES_URL")
	if connection == "" {
		t.Skip("set HEX_TEST_POSTGRES_URL")
	}
	values := map[string]string{
		"HEX_SITES_PROVIDER": "none", "HEX_FILES_PROVIDER": "none", "HEX_DATABASE_PROVIDER": "none", "HEX_REALTIME_PROVIDER": "none",
		"HEX_ANALYTICS_PROVIDER": "postgres", "HEX_ANALYTICS_DATABASE_URL": connection, "HEX_IDENTITY_PROVIDER": "static",
	}
	config, cleanup, err := configure(context.Background(), func(key string) string { return values[key] })
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	if config.Database != nil || config.Analytics == nil || config.Access == nil || config.People == nil {
		t.Fatal("analytics must enable durable platform storage independently of app documents")
	}
}

func TestIdentityConfiguration(t *testing.T) {
	environments := map[string]struct {
		values         map[string]string
		expectIdentity bool
		expectAccess   bool
	}{
		"default off": {
			values: map[string]string{"HEX_SITES_PROVIDER": "none"},
		},
		"static uses ephemeral access entries": {
			values: map[string]string{
				"HEX_SITES_PROVIDER":    "none",
				"HEX_IDENTITY_PROVIDER": "static",
				"HEX_IDENTITY_GROUPS":   "sales, ops",
			},
			expectIdentity: true,
			expectAccess:   true,
		},
		"easyauth without postgres disables access control": {
			values: map[string]string{
				"HEX_SITES_PROVIDER":    "none",
				"HEX_IDENTITY_PROVIDER": "easyauth",
				"HEX_ADMIN_GROUPS":      "admin-group",
			},
			expectIdentity: true,
			expectAccess:   false,
		},
	}

	for name, environment := range environments {
		t.Run(name, func(t *testing.T) {
			getenv := func(key string) string {
				return environment.values[key]
			}
			config, cleanup, err := configure(context.Background(), getenv)
			if err != nil {
				t.Fatal(err)
			}
			defer cleanup()

			if (config.Identity != nil) != environment.expectIdentity {
				t.Fatalf("identity resolver configured: %v", config.Identity != nil)
			}
			if (config.Access != nil) != environment.expectAccess {
				t.Fatalf("access store configured: %v", config.Access != nil)
			}
		})
	}

	if _, _, err := configure(context.Background(), func(key string) string {
		if key == "HEX_IDENTITY_PROVIDER" {
			return "oauth"
		}
		return "none"
	}); err == nil {
		t.Fatal("expected an unsupported identity provider error")
	}
}

func TestSiteOnlyConfiguration(t *testing.T) {
	values := map[string]string{
		"HEX_SITES_PROVIDER":    "filesystem",
		"HEX_SITES_DIR":         filepath.Join(t.TempDir(), "sites"),
		"HEX_FILES_PROVIDER":    "none",
		"HEX_DATABASE_PROVIDER": "none",
		"HEX_REALTIME_PROVIDER": "none",
	}
	getenv := func(key string) string {
		return values[key]
	}

	config, cleanup, err := configure(context.Background(), getenv)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()

	if config.Sites == nil || config.Files != nil || config.Database != nil || config.Realtime != nil {
		t.Fatal("unexpected provider configuration")
	}
}

func TestConnectionConfigurationFailsAtStartup(t *testing.T) {
	for name, settings := range map[string]map[string]string{
		"mixed modern and legacy": {"HEX_AUTH_CONFIG": `{"type":"none"}`, "HEX_API_RESOURCE": "api://legacy"},
		"invalid legacy client":   {"HEX_API_RESOURCE": "api://legacy", "HEX_CLI_CLIENT_ID": "not-a-guid", "HEX_CLI_TENANT_ID": "tenant"},
		"missing legacy resource": {"HEX_CLI_CLIENT_ID": "11111111-2222-3333-4444-555555555555", "HEX_CLI_TENANT_ID": "tenant"},
		"malformed modern auth":   {"HEX_AUTH_CONFIG": `{"type":"oidc","issuer":"https://issuer.test","clientId":"public","scopes":[]}`},
		"credentials in origin":   {"HEX_PUBLIC_URL": "https://user:secret@hex.test"},
	} {
		t.Run(name, func(t *testing.T) {
			settings["HEX_SITES_PROVIDER"] = "none"
			settings["HEX_FILES_PROVIDER"] = "none"
			settings["HEX_DATABASE_PROVIDER"] = "none"
			settings["HEX_REALTIME_PROVIDER"] = "none"
			if _, _, err := configure(context.Background(), func(key string) string { return settings[key] }); err == nil {
				t.Fatal("invalid connection accepted at startup")
			}
		})
	}
}

func TestLogoutConfiguration(t *testing.T) {
	for _, test := range []struct{ identity, configured, want string }{
		{"easyauth", "", "/.auth/logout"},
		{"easyauth", "/custom-logout", "/custom-logout"},
		{"static", "", ""},
		{"static", "https://identity.test/logout", "https://identity.test/logout"},
	} {
		settings := map[string]string{"HEX_SITES_PROVIDER": "none", "HEX_FILES_PROVIDER": "none", "HEX_DATABASE_PROVIDER": "none", "HEX_REALTIME_PROVIDER": "none", "HEX_IDENTITY_PROVIDER": test.identity, "HEX_LOGOUT_URL": test.configured}
		config, close, err := configure(context.Background(), func(key string) string { return settings[key] })
		if err != nil {
			t.Fatal(err)
		}
		close()
		if config.LogoutURL != test.want {
			t.Fatalf("logout for %s = %q, want %q", test.identity, config.LogoutURL, test.want)
		}
	}
}

func TestStaticPathComparisonConfiguration(t *testing.T) {
	for _, test := range []struct {
		name, share, override string
		want                  bool
	}{
		{"local", "", "", false},
		{"azure read-only", "https://account.file.core.windows.net/sites", "", true},
		{"explicit exact", "https://account.file.core.windows.net/sites", "false", false},
		{"explicit insensitive", "", "true", true},
	} {
		t.Run(test.name, func(t *testing.T) {
			values := map[string]string{
				"HEX_SITES_PROVIDER": "filesystem", "HEX_PUBLISHER_PROVIDER": "none", "HEX_SITES_DIR": t.TempDir(),
				"HEX_FILES_PROVIDER": "none", "HEX_DATABASE_PROVIDER": "none", "HEX_ANALYTICS_PROVIDER": "none", "HEX_REALTIME_PROVIDER": "none",
				"AZURE_FILES_SHARE_URL": test.share, "HEX_SITE_PATH_CASE_INSENSITIVE": test.override,
			}
			config, close, err := configure(context.Background(), func(key string) string { return values[key] })
			if err != nil {
				t.Fatal(err)
			}
			defer close()
			if config.PathCaseInsensitive != test.want {
				t.Fatalf("case mode=%v, want %v", config.PathCaseInsensitive, test.want)
			}
		})
	}
	if _, _, err := configure(context.Background(), func(key string) string {
		if key == "HEX_SITE_PATH_CASE_INSENSITIVE" {
			return "invalid"
		}
		return ""
	}); err == nil {
		t.Fatal("invalid path mode accepted")
	}
}

func TestEasyAuthCompositionCanonicalizesOnlyEntraGUIDs(t *testing.T) {
	const upper = "ABCDEF01-2345-6789-ABCD-EF0123456789"
	const lower = "abcdef01-2345-6789-abcd-ef0123456789"
	values := map[string]string{
		"HEX_SITES_PROVIDER": "none", "HEX_PUBLISHER_PROVIDER": "none", "HEX_FILES_PROVIDER": "none",
		"HEX_DATABASE_PROVIDER": "none", "HEX_ANALYTICS_PROVIDER": "none", "HEX_REALTIME_PROVIDER": "none",
		"HEX_IDENTITY_PROVIDER": "easyauth", "HEX_ADMIN_GROUPS": "group:" + upper + ",role:Ops.Admin",
		"HEX_PUBLISHER_GROUPS": "user:" + upper + ",role:Publish", "HEX_GROUPS": "Team=" + upper,
	}
	config, close, err := configure(context.Background(), func(key string) string { return values[key] })
	if err != nil {
		t.Fatal(err)
	}
	defer close()
	if len(config.AdminGroups) != 2 || config.AdminGroups[0] != "group:"+lower || config.AdminGroups[1] != "role:Ops.Admin" ||
		len(config.PublisherGroups) != 2 || config.PublisherGroups[0] != "user:"+lower || config.PublisherGroups[1] != "role:Publish" ||
		len(config.Groups) != 1 || config.Groups[0].ID != lower {
		t.Fatalf("incorrect principal canonicalization: %+v", config)
	}
}

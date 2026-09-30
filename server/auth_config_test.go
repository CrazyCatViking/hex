package hex_test

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"regexp"
	"testing"

	hex "github.com/crazycatviking/hex/server"
)

func TestConnectionAndInstallerCarryProviderIndependentAuth(t *testing.T) {
	auth := &hex.AuthConfig{Type: "oidc", Issuer: "https://identity.example/realm", ClientID: "hex-cli", Scopes: []string{"openid", "profile", "offline_access", "hex-api"}}
	server := hex.New(hex.Config{SiteBaseURL: "https://hex.example", Connection: &hex.ConnectionConfig{Name: "OIDC platform", Server: "https://hex.example", Auth: auth}})
	configuration := request(t, server, http.MethodGet, "/api/hex/config", nil, http.StatusOK)
	var document struct {
		Auth hex.AuthConfig `json:"auth"`
	}
	if err := json.Unmarshal(configuration.Body.Bytes(), &document); err != nil {
		t.Fatal(err)
	}
	if document.Auth.ClientID != "hex-cli" || document.Auth.Issuer != auth.Issuer {
		t.Fatalf("auth not advertised: %+v", document.Auth)
	}
	installer := request(t, server, http.MethodGet, "/api/hex/install/linux", nil, http.StatusOK)
	match := regexp.MustCompile(`printf '%s' '([^']+)' \| decode >`).FindStringSubmatch(installer.Body.String())
	if len(match) != 2 {
		t.Fatal("installer has no connection document")
	}
	embedded, err := base64.StdEncoding.DecodeString(match[1])
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(embedded, &document); err != nil {
		t.Fatal(err)
	}
	if document.Auth.Type != "oidc" || document.Auth.ClientID != "hex-cli" {
		t.Fatalf("installer lost auth settings: %+v", document.Auth)
	}
}

func TestAuthConfigRejectsIncompleteOrUnsafeSettings(t *testing.T) {
	for _, value := range []string{
		`{"type":"unknown"}`, `{"type":"none","clientId":"hex"}`,
		`{"type":"oidc","issuer":"http://remote.example","clientId":"hex","scopes":["openid"]}`,
		`{"type":"oidc","issuer":"https://user:secret@identity.example","clientId":"hex","scopes":["openid"]}`,
		`{"type":"oidc","issuer":"https://identity.example","scopes":["openid"]}`,
		`{"type":"oidc","issuer":"https://identity.example","clientId":"hex","scopes":["profile"]}`,
		`{"type":"none","secret":"not-allowed"}`, `null`, `{"type":"none"} {}`,
	} {
		if _, err := hex.ParseAuthConfig(value); err == nil {
			t.Errorf("accepted invalid auth config: %s", value)
		}
	}
	if _, err := hex.ParseAuthConfig(`{"type":"oidc","issuer":"http://localhost:9090","clientId":"hex-cli","scopes":["openid"]}`); err != nil {
		t.Fatal(err)
	}
}

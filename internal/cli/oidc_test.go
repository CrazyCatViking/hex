package cli

import (
	"encoding/json"
	"testing"

	hex "github.com/crazycatviking/hex/server"
)

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

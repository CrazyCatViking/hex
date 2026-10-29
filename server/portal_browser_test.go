package hex_test

import (
	"context"
	"encoding/json"
	"net"
	"net/http/httptest"
	"os"
	"os/exec"
	"testing"

	hex "github.com/crazycatviking/hex/server"
	"github.com/crazycatviking/hex/server/providers/memory"
)

func TestAdminAccessBrowser(t *testing.T) {
	if os.Getenv("HEX_PORTAL_BROWSER") != "1" {
		t.Skip("set HEX_PORTAL_BROWSER=1 to run Playwright")
	}
	config, _, access := discoveryFixture(t, 156)
	if err := access.PutSiteAccess(context.Background(), "app-0155", hex.SiteAccess{Version: 2, Owners: []string{"user:owner"}, Admission: hex.Subjects{Owners: true}}); err != nil {
		t.Fatal(err)
	}
	config.AdminGroups = []string{"admins"}
	config.Analytics = memory.NewAnalytics()
	server := httptest.NewUnstartedServer(nil)
	_, port, err := net.SplitHostPort(server.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	config.SiteBaseURL = "http://localhost:" + port
	server.Config.Handler = hex.New(config)
	server.Start()
	defer server.Close()
	command := exec.Command("node", "../scripts/test-admin-access.mjs")
	headers, err := json.Marshal(principalHeaders("operator", "admins"))
	if err != nil {
		t.Fatal(err)
	}
	command.Env = append(os.Environ(), "HEX_PORTAL_URL="+config.SiteBaseURL, "HEX_PORTAL_HEADERS="+string(headers))
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("admin browser tests: %v\n%s", err, output)
	}
	t.Log(string(output))
}

// Opt-in browser tests run against the real Go handlers and embedded assets,
// not the separately published design prototypes.
func TestPortalBrowser(t *testing.T) {
	if os.Getenv("HEX_PORTAL_BROWSER") != "1" {
		t.Skip("set HEX_PORTAL_BROWSER=1 to run Playwright")
	}
	config, _, _ := discoveryFixture(t, 156)
	server := httptest.NewUnstartedServer(nil)
	_, port, err := net.SplitHostPort(server.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	config.SiteBaseURL = "http://localhost:" + port
	config.Analytics = memory.NewAnalytics()
	server.Config.Handler = hex.New(config)
	server.Start()
	defer server.Close()
	command := exec.Command("node", "../scripts/test-portal-discovery.mjs")
	headers, err := json.Marshal(principalHeaders("owner"))
	if err != nil {
		t.Fatal(err)
	}
	command.Env = append(os.Environ(), "HEX_PORTAL_URL="+config.SiteBaseURL, "HEX_PORTAL_HEADERS="+string(headers))
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("browser tests: %v\n%s", err, output)
	}
	t.Log(string(output))
}

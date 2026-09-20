package hex_test

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	hex "github.com/crazycatviking/hex/server"
)

type portalDirectory struct {
	metadata map[string]*hex.SiteMetadata
}

func (d portalDirectory) ListSites(context.Context) ([]string, error) {
	names := make([]string, 0, len(d.metadata))
	for name := range d.metadata {
		names = append(names, name)
	}
	return names, nil
}

func (d portalDirectory) ReadSiteMetadata(_ context.Context, name string) (*hex.SiteMetadata, error) {
	return d.metadata[name], nil
}

func TestPortalVisibilityStatisticsAndHTMX(t *testing.T) {
	hidden := false
	server := hex.New(hex.Config{
		SiteBaseURL: "https://hex.smartdok.dev",
		Connection:  &hex.ConnectionConfig{Name: "SmartDok Hex", Server: "https://hex.smartdok.dev"},
		Sites: portalDirectory{metadata: map[string]*hex.SiteMetadata{
			"dashboard": {Title: "Dashboard", Description: "Reports", Author: "Alex", PublishedAt: time.Now().Add(-time.Hour)},
			"notes":     {Title: "Notes <script>alert(1)</script>", Author: "alex", PublishedAt: time.Now().AddDate(0, 0, -40)},
			"draft":     {Title: "Secret draft", Author: "Hidden author", Discoverable: &hidden},
			"legacy":    nil,
		}},
	})
	page := request(t, server, http.MethodGet, "https://hex.smartdok.dev/", nil, http.StatusOK)
	for _, expected := range []string{"SmartDok Hex", "Dashboard", "Notes &lt;script&gt;", "hx-get=", "/api/hex/htmx.min.js", "Download installer"} {
		if !strings.Contains(page.Body.String(), expected) {
			t.Fatalf("missing %q from landing page", expected)
		}
	}
	for _, forbidden := range []string{"Secret draft", "Hidden author", "<script>alert(1)</script>"} {
		if strings.Contains(page.Body.String(), forbidden) {
			t.Fatalf("landing page exposed %q", forbidden)
		}
	}
	request(t, server, http.MethodGet, "https://dashboard.hex.smartdok.dev/", nil, http.StatusNotFound)
	request(t, server, http.MethodGet, "https://evil.example/", nil, http.StatusNotFound)
	fragment := request(t, server, http.MethodGet, "/api/hex/catalog?search=reports&sort=name", nil, http.StatusOK)
	if !strings.HasPrefix(strings.TrimSpace(fragment.Body.String()), `<div id="catalog">`) || !strings.Contains(fragment.Body.String(), "Dashboard") || strings.Contains(fragment.Body.String(), "Notes &lt;") {
		t.Fatal("HTMX search did not return the filtered catalog fragment")
	}
	overview := request(t, server, http.MethodGet, "/api/hex/overview", nil, http.StatusOK)
	var result struct {
		Sites               []hex.Site                                    `json:"sites"`
		Statistics          struct{ Sites, Authors, UpdatedRecently int } `json:"statistics"`
		InstallersAvailable bool                                          `json:"installersAvailable"`
	}
	if err := json.Unmarshal(overview.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Sites) != 3 || result.Statistics.Sites != 3 || result.Statistics.Authors != 1 || result.Statistics.UpdatedRecently != 1 || !result.InstallersAvailable {
		t.Fatalf("incorrect overview: %+v", result)
	}
	discovery := request(t, server, http.MethodGet, "/api/sites", nil, http.StatusOK)
	if strings.Contains(discovery.Body.String(), "draft") {
		t.Fatal("hidden site was exposed by discovery")
	}
	asset := request(t, server, http.MethodGet, "/api/hex/htmx.min.js", nil, http.StatusOK)
	if !strings.Contains(asset.Body.String(), "htmx.org 2.0.10") {
		t.Fatal("HTMX is not served locally")
	}
}

func TestEmptyPortalWithoutConnection(t *testing.T) {
	server := hex.New(hex.Config{SiteBaseURL: "https://hex.example.com"})
	page := request(t, server, http.MethodGet, "https://hex.example.com/", nil, http.StatusOK)
	if !strings.Contains(page.Body.String(), "No apps are listed yet") || strings.Contains(page.Body.String(), `id="installer-controls"`) {
		t.Fatal("empty or unconfigured portal is incorrect")
	}
	request(t, server, http.MethodGet, "/api/hex/install/linux", nil, http.StatusNotFound)
}

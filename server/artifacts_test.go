package hex_test

import (
	"encoding/json"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	hex "github.com/crazycatviking/hex/server"
	"github.com/crazycatviking/hex/server/providers/easyauth"
	"github.com/crazycatviking/hex/server/providers/local"
	"github.com/crazycatviking/hex/server/providers/memory"
)

type artifact struct {
	Name    string   `json:"name"`
	URL     string   `json:"url"`
	Title   string   `json:"title"`
	Viewers []string `json:"viewers"`
}

func TestArtifactsArePrivateUntilShared(t *testing.T) {
	store, err := local.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	server := hex.New(hex.Config{
		Sites:       store,
		Publisher:   store,
		SiteBaseURL: "https://hex.example.com",
		Identity:    easyauth.Resolver{},
		Access:      memory.NewAccessStore(),
		// Publisher groups limit app names, not artifacts.
		PublisherGroups: []string{"publishers"},
	})
	creator := principalHeaders("creator-id")
	colleague := principalHeaders("colleague-id", "sales")

	response := requestAs(t, server, creator, "POST", "/api/hex/artifacts", []byte(`{"title":"Q3 report"}`), 201)
	var created artifact
	if err := json.Unmarshal(response.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if !regexp.MustCompile(`^[a-z][a-z0-9]{9}$`).MatchString(created.Name) || created.URL != "https://"+created.Name+".hex.example.com/" {
		t.Fatalf("unexpected artifact: %+v", created)
	}

	publishSite(t, server, creator, created.Name, map[string]string{"index.html": "report"}, "")

	authz := func(headers map[string][]string) int {
		r := httptest.NewRequest("GET", "/api/hex/authz", nil)
		for name, values := range headers {
			r.Header[name] = values
		}
		r.Header.Set("X-Hex-Site", created.Name)
		w := httptest.NewRecorder()
		server.ServeHTTP(w, r)
		return w.Code
	}
	if authz(creator) != 204 || authz(colleague) != 403 {
		t.Fatal("a new artifact must be visible to its creator only")
	}

	// Artifacts never appear in the catalogue, only in their owner's list.
	if listing := requestAs(t, server, creator, "GET", "/api/sites", nil, 200).Body.String(); strings.Contains(listing, created.Name) {
		t.Fatalf("artifact listed in discovery: %s", listing)
	}
	var mine []struct {
		Name  string `json:"name"`
		Title string `json:"title"`
		Kind  string `json:"kind"`
	}
	if err := json.Unmarshal(requestAs(t, server, creator, "GET", "/api/hex/my-sites", nil, 200).Body.Bytes(), &mine); err != nil {
		t.Fatal(err)
	}
	// The publication's title ("Demo", from the test helper) replaces the
	// one given at creation.
	if len(mine) != 1 || mine[0].Name != created.Name || mine[0].Title != "Demo" || mine[0].Kind != "Artifact" {
		t.Fatalf("unexpected site list: %+v", mine)
	}
	if others := requestAs(t, server, colleague, "GET", "/api/hex/my-sites", nil, 200).Body.String(); strings.Contains(others, created.Name) {
		t.Fatalf("another user's artifact was listed: %s", others)
	}

	// Sharing adds viewers; the artifact stays an artifact when republished.
	putPolicy(t, server, creator, created.Name, `{"viewers":["user:creator-id","group:sales"]}`)
	if authz(colleague) != 204 {
		t.Fatal("sharing with a group did not grant access")
	}
	publishSite(t, server, creator, created.Name, map[string]string{"index.html": "report v2"}, "")
	data, _ := readSiteFile(t, store, created.Name, ".hex-site.json")
	if !strings.Contains(data, `"kind": "artifact"`) || !strings.Contains(data, `"title": "Demo"`) {
		t.Fatalf("republishing lost the artifact kind: %s", data)
	}

	// Updating without a title keeps it.
	untitled := publication{Files: manifest(map[string]string{"index.html": "report v3"})}
	requestAs(t, server, creator, "POST", "/api/hex/sites/"+created.Name+"/publish", encode(t, untitled), 200)
	requestAs(t, server, creator, "PUT", "/api/hex/sites/"+created.Name+"/publish/files/index.html", []byte("report v3"), 204)
	requestAs(t, server, creator, "POST", "/api/hex/sites/"+created.Name+"/publish/complete", encode(t, untitled), 200)
	if data, _ := readSiteFile(t, store, created.Name, ".hex-site.json"); !strings.Contains(data, `"title": "Demo"`) {
		t.Fatalf("an untitled update removed the title: %s", data)
	}

	// Colleagues cannot take it over or publish to it.
	body := encode(t, publication{Files: manifest(map[string]string{"index.html": "x"})})
	requestAs(t, server, colleague, "POST", "/api/hex/sites/"+created.Name+"/publish", body, 403)
	requestAs(t, server, nil, "POST", "/api/hex/artifacts", nil, 401)
}

package hex_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	hex "github.com/crazycatviking/hex/server"
	"github.com/crazycatviking/hex/server/providers/easyauth"
	"github.com/crazycatviking/hex/server/providers/memory"
)

type checkedDirectPublisher struct {
	hex.SitePublisher
	check func()
}

func (p checkedDirectPublisher) UploadTargets(context.Context, string, []hex.SiteFile) ([]hex.UploadTarget, error) {
	p.check()
	return []hex.UploadTarget{}, nil
}

func TestPublicationRestrictionsPrecedeUploads(t *testing.T) {
	for _, existing := range []bool{false, true} {
		name := "new-private-site"
		if existing {
			name = "restricted-path-update"
		}
		t.Run(name, func(t *testing.T) {
			_, store := setupWithAccess(t)
			config := hex.Config{Publisher: store, Sites: store, Identity: easyauth.Resolver{}, Access: memory.NewAccessStore()}
			server, replica := hex.New(config), hex.New(config)
			owner := principalHeaders("owner")
			path := "/index.html"
			policy := `{"viewers":["user:owner"]}`
			files := map[string]string{"index.html": "secret"}
			if existing {
				publishSite(t, server, owner, "demo", map[string]string{"index.html": "public"}, "")
				path = "/admin/index.html"
				policy = `{"paths":[{"prefix":"/admin/","viewers":"owners"}]}`
				files = map[string]string{"index.html": "public", "admin/index.html": "secret"}
			}
			outsider := principalHeaders("outsider")
			outsider.Set("X-Hex-Site", "demo")
			outsider.Set("X-Hex-Path", path)
			// Prime the second instance before the policy changes.
			requestAs(t, replica, outsider, "GET", "/api/hex/authz", nil, 204)
			checkRestricted := func() {
				requestAs(t, replica, outsider, "GET", "/api/hex/authz", nil, 403)
			}
			// Direct-upload credentials must not be issued before the restrictions
			// are visible to other replicas serving the same storage.
			config.Publisher = checkedDirectPublisher{SitePublisher: store, check: checkRestricted}
			server = hex.New(config)
			body := publication{Files: manifest(files), Access: json.RawMessage(policy)}
			requestAs(t, server, owner, "POST", "/api/hex/sites/demo/publish", encode(t, body), 200)
			for file, content := range files {
				requestAs(t, server, owner, "PUT", "/api/hex/sites/demo/publish/files/"+file, []byte(content), 204)
			}
			// An interrupted upload is already protected without /complete.
			checkRestricted()
			if content, ok := readSiteFile(t, store, "demo", path[1:]); !ok || content != "secret" {
				t.Fatal("expected private content in the served storage")
			}
			requestAs(t, server, owner, "POST", "/api/hex/sites/demo/publish/complete", encode(t, body), 200)
			checkRestricted()
		})
	}
}

func TestRejectedPublicationDoesNotClaimName(t *testing.T) {
	server, _ := setupWithAccess(t)
	owner := principalHeaders("owner")
	body := publication{Files: manifest(map[string]string{"index.html": "private"}), Access: json.RawMessage(`{"owners":["user:other"]}`)}
	requestAs(t, server, owner, "POST", "/api/hex/sites/demo/publish", encode(t, body), http.StatusBadRequest)
	putPolicy(t, server, principalHeaders("other"), "demo", `{"owners":["user:other"]}`)
}

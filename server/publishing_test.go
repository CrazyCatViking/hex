package hex_test

import (
	"context"
	"crypto/md5"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	hex "github.com/crazycatviking/hex/server"
	"github.com/crazycatviking/hex/server/providers/local"
)

type publication struct {
	Files    []hex.SiteFile    `json:"files"`
	Metadata *hex.SiteMetadata `json:"metadata,omitempty"`
	Access   json.RawMessage   `json:"access,omitempty"`
}

type publishPlan struct {
	Uploads   []hex.UploadTarget `json:"uploads"`
	Unchanged int                `json:"unchanged"`
}

func manifest(files map[string]string) []hex.SiteFile {
	entries := make([]hex.SiteFile, 0, len(files))
	for path, content := range files {
		digest := md5.Sum([]byte(content))
		entries = append(entries, hex.SiteFile{
			Path: path,
			Size: int64(len(content)),
			MD5:  base64.StdEncoding.EncodeToString(digest[:]),
		})
	}
	return entries
}

func encode(t *testing.T, value any) []byte {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// publishSite runs the whole publication protocol and returns the plan the
// server answered with.
func publishSite(t *testing.T, server http.Handler, headers http.Header, site string, files map[string]string, access string) publishPlan {
	t.Helper()
	body := publication{Files: manifest(files), Metadata: &hex.SiteMetadata{Title: "Demo"}}
	if access != "" {
		body.Access = json.RawMessage(access)
	}

	response := requestAs(t, server, headers, "POST", "/api/hex/sites/"+site+"/publish", encode(t, body), 200)
	var plan publishPlan
	if err := json.Unmarshal(response.Body.Bytes(), &plan); err != nil {
		t.Fatal(err)
	}
	for _, upload := range plan.Uploads {
		if upload.Protocol != "hex" {
			t.Fatalf("unexpected upload protocol %q", upload.Protocol)
		}
		requestAs(t, server, headers, "PUT", upload.URL, []byte(files[upload.Path]), 204)
	}
	requestAs(t, server, headers, "POST", "/api/hex/sites/"+site+"/publish/complete", encode(t, body), 200)
	return plan
}

func readSiteFile(t *testing.T, store *local.Store, site, path string) (string, bool) {
	t.Helper()
	reader, err := store.ReadSiteFile(context.Background(), site, path)
	if err != nil {
		return "", false
	}
	defer reader.Close()
	data, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	return string(data), true
}

func TestPublishingThroughTheServer(t *testing.T) {
	server, store := setupWithAccess(t)
	publisher := principalHeaders("publisher-id")

	first := publishSite(t, server, publisher, "demo", map[string]string{
		"index.html":       "<h1>v1</h1>",
		"assets/app.js":    "console.log(1)",
		"assets/old.css":   "body{}",
		"admin/index.html": "admin",
	}, "")
	if len(first.Uploads) != 4 || first.Uploads[len(first.Uploads)-1].Path != "index.html" {
		t.Fatalf("index.html must be uploaded last: %+v", first.Uploads)
	}

	// The publisher now owns the site.
	policy := requestAs(t, server, publisher, "GET", "/api/hex/sites/demo/access", nil, 200).Body.String()
	if !strings.Contains(policy, `"owners":["user:publisher-id"]`) {
		t.Fatalf("publisher did not claim the site: %s", policy)
	}

	// Republishing uploads only changed files and removes obsolete ones.
	second := publishSite(t, server, publisher, "demo", map[string]string{
		"index.html":       "<h1>v2</h1>",
		"assets/app.js":    "console.log(1)",
		"admin/index.html": "admin",
	}, `{"paths":[{"prefix":"/admin/","viewers":"owners"}]}`)
	if len(second.Uploads) != 1 || second.Unchanged != 2 {
		t.Fatalf("expected one changed file: %+v", second)
	}
	if content, _ := readSiteFile(t, store, "demo", "index.html"); content != "<h1>v2</h1>" {
		t.Fatalf("index.html was not replaced: %q", content)
	}
	if _, exists := readSiteFile(t, store, "demo", "assets/old.css"); exists {
		t.Fatal("obsolete file survived the publication")
	}
	metadata, _ := readSiteFile(t, store, "demo", ".hex-site.json")
	if !strings.Contains(metadata, `"title": "Demo"`) || !strings.Contains(metadata, "publishedAt") {
		t.Fatalf("metadata was not recorded: %s", metadata)
	}
	policy = requestAs(t, server, publisher, "GET", "/api/hex/sites/demo/access", nil, 200).Body.String()
	if !strings.Contains(policy, `/admin/`) || !strings.Contains(policy, `user:publisher-id`) {
		t.Fatalf("hex.json access policy was not applied: %s", policy)
	}

	// Discovery sees the published site.
	listing := requestAs(t, server, publisher, "GET", "/api/sites", nil, 200).Body.String()
	if !strings.Contains(listing, `"name":"demo"`) {
		t.Fatalf("published site is not discoverable: %s", listing)
	}

	// Unpublishing removes the files and keeps the name reserved.
	requestAs(t, server, publisher, "DELETE", "/api/hex/sites/demo", nil, 204)
	if _, exists := readSiteFile(t, store, "demo", "index.html"); exists {
		t.Fatal("unpublished site still has files")
	}
	body := encode(t, publication{Files: manifest(map[string]string{"index.html": "x"})})
	requestAs(t, server, principalHeaders("squatter"), "POST", "/api/hex/sites/demo/publish", body, 403)
}

func TestOnlyOwnersPublish(t *testing.T) {
	server, _ := setupWithAccess(t)
	owner := principalHeaders("owner-id")
	coOwner := principalHeaders("co-owner", "team")
	other := principalHeaders("other-id")
	admin := principalHeaders("admin-id", "admin-group")

	publishSite(t, server, owner, "demo", map[string]string{"index.html": "v1"}, `{"owners":["user:owner-id","group:team"]}`)

	body := encode(t, publication{Files: manifest(map[string]string{"index.html": "hijacked"})})
	requestAs(t, server, other, "POST", "/api/hex/sites/demo/publish", body, 403)
	requestAs(t, server, other, "PUT", "/api/hex/sites/demo/publish/files/index.html", []byte("hijacked"), 403)
	requestAs(t, server, other, "POST", "/api/hex/sites/demo/publish/complete", body, 403)
	requestAs(t, server, other, "DELETE", "/api/hex/sites/demo", nil, 403)
	requestAs(t, server, nil, "POST", "/api/hex/sites/demo/publish", body, 401)

	publishSite(t, server, coOwner, "demo", map[string]string{"index.html": "v2"}, "")
	publishSite(t, server, admin, "demo", map[string]string{"index.html": "v3"}, "")

	// Uploads and completion never claim a name.
	requestAs(t, server, other, "PUT", "/api/hex/sites/fresh/publish/files/index.html", []byte("x"), 403)
}

func TestPublicationValidation(t *testing.T) {
	server, _ := setupWithAccess(t)
	publisher := principalHeaders("publisher-id")

	invalid := []publication{
		{Files: manifest(map[string]string{"app.js": "no index"})},
		{Files: manifest(map[string]string{"index.html": "x", ".env": "secret"})},
		{Files: manifest(map[string]string{"index.html": "x", "a/../../escape": "x"})},
		{Files: []hex.SiteFile{{Path: "index.html", Size: 1, MD5: "not-a-digest"}}},
		{Files: []hex.SiteFile{{Path: "index.html", Size: 1 << 40, MD5: base64.StdEncoding.EncodeToString(make([]byte, 16))}}},
	}
	for _, body := range invalid {
		requestAs(t, server, publisher, "POST", "/api/hex/sites/demo/publish", encode(t, body), 400)
	}
	requestAs(t, server, publisher, "POST", "/api/hex/sites/Not_A_Label/publish", encode(t, publication{}), 400)

	// A policy that would lock the publisher out is refused before any upload.
	lockout := publication{Files: manifest(map[string]string{"index.html": "x"}), Access: json.RawMessage(`{"owners":["user:someone-else"]}`)}
	requestAs(t, server, publisher, "POST", "/api/hex/sites/locked/publish", encode(t, lockout), 400)

	// Completion fails while files are missing, without changing the site.
	body := encode(t, publication{Files: manifest(map[string]string{"index.html": "x", "app.js": "y"})})
	requestAs(t, server, publisher, "POST", "/api/hex/sites/demo/publish", body, 200)
	requestAs(t, server, publisher, "PUT", "/api/hex/sites/demo/publish/files/index.html", []byte("x"), 204)
	response := requestAs(t, server, publisher, "POST", "/api/hex/sites/demo/publish/complete", body, 409)
	if !strings.Contains(response.Body.String(), "app.js") {
		t.Fatalf("missing files are not reported: %s", response.Body.String())
	}

	// A declared size that does not match the upload is rejected.
	requestAs(t, server, publisher, "PUT", "/api/hex/sites/demo/publish/files/app.js", nil, 204)
	requestAs(t, server, publisher, "POST", "/api/hex/sites/demo/publish/complete", body, 409)
}

func TestLocalPublisherKeepsFilesInsideTheSite(t *testing.T) {
	directory := t.TempDir()
	store, err := local.New(directory)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()

	if err := store.WriteSiteFile(ctx, "demo", "../other/index.html", 1, strings.NewReader("x")); err == nil {
		t.Fatal("wrote outside the site directory")
	}
	if err := store.WriteSiteFile(ctx, "demo", "index.html", 5, strings.NewReader("short")); err != nil {
		t.Fatal(err)
	}
	if err := store.WriteSiteFile(ctx, "demo", "big.bin", 10, strings.NewReader("short")); err == nil {
		t.Fatal("accepted a truncated upload")
	}
	if _, err := os.Stat(filepath.Join(directory, "public/sites/demo/big.bin")); !os.IsNotExist(err) {
		t.Fatal("truncated upload was left behind")
	}

	files, err := store.ListSiteFiles(ctx, "demo")
	if err != nil || len(files) != 1 || files[0].Path != "index.html" || files[0].Size != 5 {
		t.Fatalf("unexpected listing: %+v %v", files, err)
	}
	if files, err := store.ListSiteFiles(ctx, "missing"); err != nil || len(files) != 0 {
		t.Fatalf("missing sites have no files: %+v %v", files, err)
	}
}

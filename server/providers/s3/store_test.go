package s3

import (
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sort"
	"strings"
	"sync"
	"testing"

	hex "github.com/crazycatviking/hex/server"
)

// A deliberately small S3 wire server forces every listing through two pages.
func testStore(t *testing.T) (*Store, map[string]string, *sync.Mutex) {
	t.Helper()
	objects := map[string]string{}
	mu := &sync.Mutex{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if r.URL.Query().Get("list-type") == "2" {
			keys := []string{}
			for key := range objects {
				if strings.HasPrefix(key, r.URL.Query().Get("prefix")) {
					keys = append(keys, key)
				}
			}
			sort.Strings(keys)
			start := 0
			if r.URL.Query().Get("continuation-token") != "" {
				start = 1
			}
			end := len(keys)
			if start == 0 && end > 1 {
				end = 1
			}
			fmt.Fprint(w, `<ListBucketResult xmlns="http://s3.amazonaws.com/doc/2006-03-01/">`)
			if end < len(keys) {
				fmt.Fprint(w, `<IsTruncated>true</IsTruncated><NextContinuationToken>next</NextContinuationToken>`)
			} else {
				fmt.Fprint(w, `<IsTruncated>false</IsTruncated>`)
			}
			for _, key := range keys[start:end] {
				fmt.Fprint(w, "<Contents><Key>")
				if err := xml.EscapeText(w, []byte(key)); err != nil {
					t.Errorf("encode key: %v", err)
				}
				fmt.Fprintf(w, "</Key><Size>%d</Size></Contents>", len(objects[key]))
			}
			fmt.Fprint(w, "</ListBucketResult>")
			return
		}
		key := strings.TrimPrefix(r.URL.Path, "/bucket/")
		switch r.Method {
		case http.MethodPut:
			data, err := io.ReadAll(r.Body)
			if err != nil {
				t.Errorf("read upload: %v", err)
				w.WriteHeader(500)
				return
			}
			objects[key] = string(data)
			w.Header().Set("ETag", `"fake"`)
		case http.MethodGet, http.MethodHead:
			data, ok := objects[key]
			if !ok {
				w.WriteHeader(404)
				fmt.Fprint(w, "<Error><Code>NoSuchKey</Code></Error>")
				return
			}
			w.Header().Set("Content-Length", fmt.Sprint(len(data)))
			if r.Method == http.MethodGet {
				fmt.Fprint(w, data)
			}
		case http.MethodDelete:
			delete(objects, key)
			w.WriteHeader(204)
		default:
			t.Errorf("unexpected method %s", r.Method)
			w.WriteHeader(500)
		}
	}))
	t.Cleanup(server.Close)
	store, err := New(context.Background(), Config{Bucket: "bucket", Endpoint: server.URL, Region: "us-east-1", Prefix: "tenant", UsePathStyle: true, AccessKeyID: "test", SecretAccessKey: "secret", HTTPClient: server.Client()})
	if err != nil {
		t.Fatal(err)
	}
	return store, objects, mu
}

func TestObjectsAndSites(t *testing.T) {
	store, objects, mu := testStore(t)
	ctx := context.Background()
	for key, data := range map[string]string{"a": "one", "b": "two", "public/sites/demo/index.html": "hello", "public/sites/demo/.hex-site.json": `{"title":"Demo"}`, "public/sites/other/asset": "asset"} {
		if err := store.Put(ctx, key, strings.NewReader(data)); err != nil {
			t.Fatal(err)
		}
	}
	mu.Lock()
	objects["tenant-other/secret"] = "not ours"
	mu.Unlock()
	listed, err := store.List(ctx, "")
	if err != nil || len(listed) != 5 {
		t.Fatalf("list: %v %v", listed, err)
	}
	sites, err := store.ListSites(ctx)
	if err != nil || len(sites) != 1 || sites[0] != "demo" {
		t.Fatalf("sites: %v %v", sites, err)
	}
	metadata, err := store.ReadSiteMetadata(ctx, "demo")
	if err != nil || metadata.Title != "Demo" {
		t.Fatalf("metadata: %v %v", metadata, err)
	}
	missing, err := store.ReadSiteMetadata(ctx, "unknown")
	if err != nil || missing != nil {
		t.Fatalf("missing metadata: %v %v", missing, err)
	}
	if err := store.WriteSiteFile(ctx, "demo", "index.html", 2, strings.NewReader("bad")); err == nil {
		t.Fatal("accepted long body")
	}
	if err := store.WriteSiteFile(ctx, "demo", "index.html", 8, strings.NewReader("bad")); err == nil {
		t.Fatal("accepted short body")
	}
	body, err := store.ReadSiteFile(ctx, "demo", "index.html")
	if err != nil {
		t.Fatal(err)
	}
	data, readErr := io.ReadAll(body)
	if err := errors.Join(readErr, body.Close()); err != nil {
		t.Fatal(err)
	}
	if string(data) != "hello" {
		t.Fatalf("failed validation replaced object: %q", data)
	}
	if err := store.DeleteSite(ctx, "demo"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Open(ctx, "public/sites/demo/index.html"); !errors.Is(err, hex.ErrNotFound) {
		t.Fatalf("missing open: %v", err)
	}
	if err := store.Delete(ctx, "missing"); !errors.Is(err, hex.ErrNotFound) {
		t.Fatalf("missing delete: %v", err)
	}
	if err := store.DeleteSite(ctx, "absent"); err != nil {
		t.Fatal(err)
	}
}

func TestScopeAndCancellation(t *testing.T) {
	store, _, _ := testStore(t)
	ctx := context.Background()
	for _, key := range []string{"", ".", "..", "a/../b", "/absolute", "a//b", "a\\b", "a\x00b"} {
		if err := store.Put(ctx, key, strings.NewReader("x")); !errors.Is(err, fs.ErrInvalid) {
			t.Errorf("key %q: %v", key, err)
		}
		if _, err := store.Open(ctx, key); !errors.Is(err, fs.ErrInvalid) {
			t.Errorf("open %q: %v", key, err)
		}
	}
	for _, site := range []string{"", ".", "..", "a/b", "a\\b"} {
		if _, err := store.ListSiteFiles(ctx, site); !errors.Is(err, fs.ErrInvalid) {
			t.Errorf("site %q: %v", site, err)
		}
	}
	if _, err := store.List(ctx, "../"); !errors.Is(err, fs.ErrInvalid) {
		t.Fatal(err)
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if err := store.Put(canceled, "key", strings.NewReader("x")); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if _, err := store.List(canceled, ""); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if _, err := store.Open(canceled, "key"); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func TestUploadTargets(t *testing.T) {
	store, _, _ := testStore(t)
	files := []hex.SiteFile{{Path: "assets/a b.txt", Size: 3, MD5: "kAFQmDzST7DWlj99KOF/cg=="}, {Path: "empty", Size: 0}}
	targets, err := store.UploadTargets(context.Background(), "demo", files)
	if err != nil {
		t.Fatal(err)
	}
	for i, target := range targets {
		u, err := url.Parse(target.URL)
		if err != nil {
			t.Fatal(err)
		}
		if target.Protocol != "http" || u.Path != "/bucket/tenant/public/sites/demo/"+files[i].Path {
			t.Fatalf("target: %#v", target)
		}
		signed := u.Query().Get("X-Amz-SignedHeaders")
		if files[i].Size > 0 && !strings.Contains(signed, "content-length") {
			t.Fatalf("length not signed: %s", target.URL)
		}
		if i == 0 && (!strings.Contains(signed, "content-md5") || target.Headers["Content-Md5"] != files[i].MD5) {
			t.Fatalf("MD5 not signed: %#v", target)
		}
		if u.Query().Get("X-Amz-Expires") != "900" || u.Query().Get("X-Amz-Signature") == "" {
			t.Fatalf("unsigned target: %s", target.URL)
		}
	}
	for _, file := range []hex.SiteFile{{Path: "../escape"}, {Path: "x", Size: -1}, {Path: "x", MD5: "bad"}} {
		if _, err := store.UploadTargets(context.Background(), "demo", []hex.SiteFile{file}); err == nil {
			t.Fatalf("accepted %#v", file)
		}
	}
}

type trackedBody struct {
	io.Reader
	closed bool
}

func (b *trackedBody) Close() error {
	b.closed = true
	return nil
}

type responseClient struct {
	body   *trackedBody
	status int
}

func (c responseClient) Do(r *http.Request) (*http.Response, error) {
	return &http.Response{StatusCode: c.status, Header: http.Header{}, Body: c.body, Request: r}, nil
}

func TestStreamingOwnershipAndErrors(t *testing.T) {
	for _, data := range []string{`{"title":"Demo"}`, "invalid", strings.Repeat("x", 64*1024+1)} {
		body := &trackedBody{Reader: strings.NewReader(data)}
		store, err := New(context.Background(), Config{Bucket: "b", AccessKeyID: "test", SecretAccessKey: "secret", HTTPClient: responseClient{body: body, status: 200}})
		if err != nil {
			t.Fatal(err)
		}
		_, _ = store.ReadSiteMetadata(context.Background(), "demo")
		if !body.closed {
			t.Fatal("metadata body not closed")
		}
	}
	body := &trackedBody{Reader: strings.NewReader("object")}
	store, err := New(context.Background(), Config{Bucket: "b", AccessKeyID: "test", SecretAccessKey: "secret", HTTPClient: responseClient{body: body, status: 200}})
	if err != nil {
		t.Fatal(err)
	}
	injected, err := New(context.Background(), Config{Bucket: "b", Client: store.client})
	if err != nil {
		t.Fatal(err)
	}
	reader, err := injected.Open(context.Background(), "object")
	if err != nil {
		t.Fatal(err)
	}
	if body.closed {
		t.Fatal("Open prematurely closed body")
	}
	if err := reader.Close(); err != nil {
		t.Fatal(err)
	}
	if !body.closed {
		t.Fatal("caller cannot close body")
	}
	denied := &trackedBody{Reader: strings.NewReader("<Error><Code>AccessDenied</Code></Error>")}
	store, err = New(context.Background(), Config{Bucket: "b", AccessKeyID: "test", SecretAccessKey: "secret", HTTPClient: responseClient{body: denied, status: 403}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Open(context.Background(), "object"); err == nil || errors.Is(err, hex.ErrNotFound) {
		t.Fatalf("access error incorrectly mapped: %v", err)
	}
}

func TestMetadataBoundsAndConfiguration(t *testing.T) {
	store, _, _ := testStore(t)
	for _, data := range []string{"not JSON", strings.Repeat("x", 64*1024+1)} {
		if err := store.Put(context.Background(), "public/sites/demo/.hex-site.json", strings.NewReader(data)); err != nil {
			t.Fatal(err)
		}
		if _, err := store.ReadSiteMetadata(context.Background(), "demo"); err == nil {
			t.Fatal("accepted invalid metadata")
		}
	}
	for _, cfg := range []Config{{}, {Bucket: "b", Prefix: "../root"}, {Bucket: "b", Endpoint: "ftp://host"}, {Bucket: "b", AccessKeyID: "only"}, {Bucket: "b", PresignExpires: -1}} {
		if _, err := New(context.Background(), cfg); err == nil {
			t.Fatalf("accepted %#v", cfg)
		}
	}
}

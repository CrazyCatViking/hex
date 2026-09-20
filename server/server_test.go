package hex_test

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	hex "github.com/hex-platform/hex/server"
	"github.com/hex-platform/hex/server/providers/local"
	"github.com/hex-platform/hex/server/providers/memory"
)

func setup(t *testing.T) (*hex.Server, *local.Store) {
	t.Helper()
	store, err := local.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	return hex.New(hex.Config{Files: store, Sites: store, Database: memory.NewDatabase(), Realtime: memory.NewRealtime(), MaxUploadBytes: 4096}), store
}

func request(t *testing.T, handler http.Handler, method, path string, data []byte, want int) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(method, path, bytes.NewReader(data))
	r.Header.Set("X-Hex-Request", "1")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	if w.Code != want {
		t.Fatalf("%s %s: got %d, want %d: %s", method, path, w.Code, want, w.Body.String())
	}
	return w
}

func TestFileRoundtripAndBoundaries(t *testing.T) {
	s, _ := setup(t)
	path := "/api/sites/demo/files/reports/a.txt"
	request(t, s, "PUT", path, []byte("hello"), 200)
	if got := request(t, s, "GET", path, nil, 200).Body.String(); got != "hello" {
		t.Fatal(got)
	}
	request(t, s, "GET", "/api/sites/other/files/reports/a.txt", nil, 404)
	list := request(t, s, "GET", "/api/sites/demo/files", nil, 200)
	if !strings.Contains(list.Body.String(), `"key":"reports/a.txt"`) {
		t.Fatal(list.Body.String())
	}
	request(t, s, "PUT", path, bytes.Repeat([]byte("x"), 4097), 413)
	if got := request(t, s, "GET", path, nil, 200).Body.String(); got != "hello" {
		t.Fatal("oversize upload replaced file")
	}
	request(t, s, "GET", "/api/sites/demo/files/%2e%2e/secret", nil, 400)
	request(t, s, "DELETE", path, nil, 204)
	request(t, s, "GET", path, nil, 404)
}

func TestDatabaseCRUDAndPagination(t *testing.T) {
	s, _ := setup(t)
	base := "/api/sites/demo/db/tasks"
	request(t, s, "PUT", base+"/a", []byte(`{"title":"A"}`), 200)
	request(t, s, "PUT", base+"/b", []byte(`{"title":"B"}`), 200)
	request(t, s, "PUT", base+"/a", []byte(`{"done":true}`), 200)
	got := request(t, s, "GET", base+"/a", nil, 200)
	if strings.Contains(got.Body.String(), "title") {
		t.Fatal("set did not replace document")
	}
	page := request(t, s, "GET", base+"?limit=1&after=a", nil, 200)
	var docs []hex.Document
	if err := json.Unmarshal(page.Body.Bytes(), &docs); err != nil {
		t.Fatal(err)
	}
	if len(docs) != 1 || docs[0].ID != "b" {
		t.Fatal(docs)
	}
	request(t, s, "GET", "/api/sites/other/db/tasks/a", nil, 404)
	request(t, s, "POST", base, []byte(`[]`), 400)
	request(t, s, "POST", base, []byte(`{"ok":true} trailing`), 400)
	created := request(t, s, "POST", base, []byte(`{"ok":true}`), 201)
	var doc hex.Document
	json.Unmarshal(created.Body.Bytes(), &doc)
	request(t, s, "GET", base+"/"+doc.ID, nil, 200)
	request(t, s, "DELETE", base+"/a", nil, 204)
	request(t, s, "GET", base+"/a", nil, 404)
	request(t, s, "GET", base+"?limit=101", nil, 400)
}

func zipFiles(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var data bytes.Buffer
	w := zip.NewWriter(&data)
	for name, content := range files {
		f, err := w.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := io.WriteString(f, content); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return data.Bytes()
}

func storedFile(t *testing.T, store hex.ObjectStore, key string) string {
	t.Helper()
	f, err := store.Open(context.Background(), key)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	data, err := io.ReadAll(f)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func missingFile(t *testing.T, store hex.ObjectStore, key string) {
	t.Helper()
	f, err := store.Open(context.Background(), key)
	if err == nil {
		f.Close()
		t.Fatalf("unexpected file %s", key)
	}
	if !errors.Is(err, hex.ErrNotFound) {
		t.Fatal(err)
	}
}

func TestPublishingWritesPublicFolderAndRejectsInvalidArchives(t *testing.T) {
	s, store := setup(t)
	deploy := "/api/sites/demo/deploy"
	request(t, s, "POST", deploy, zipFiles(t, map[string]string{"index.html": "first", "old.js": "old"}), 201)
	request(t, s, "POST", deploy, zipFiles(t, map[string]string{"index.html": "bad", "../outside": "bad"}), 400)
	request(t, s, "POST", deploy, zipFiles(t, map[string]string{"index.html": strings.Repeat("x", 4097)}), 413)
	request(t, s, "POST", deploy, zipFiles(t, map[string]string{"missing.html": "bad"}), 400)
	request(t, s, "POST", deploy, zipFiles(t, map[string]string{"index.html": "bad", "a": "file", "a/b": "conflict"}), 400)
	if got := storedFile(t, store, "public/sites/demo/index.html"); got != "first" {
		t.Fatal(got)
	}
	request(t, s, "POST", deploy, zipFiles(t, map[string]string{"index.html": "second"}), 201)
	missingFile(t, store, "public/sites/demo/old.js")
	if got := storedFile(t, store, "public/sites/demo/index.html"); got != "second" {
		t.Fatal(got)
	}
	request(t, s, "GET", "/api/sites", nil, 200)
	request(t, s, "DELETE", "/api/sites/demo", nil, 204)
	missingFile(t, store, "public/sites/demo/index.html")
	request(t, s, "GET", "/sites/demo/", nil, 404)
}

func TestRepublishingHandlesFileDirectoryTransitions(t *testing.T) {
	s, store := setup(t)
	deploy := "/api/sites/demo/deploy"
	request(t, s, "POST", deploy, zipFiles(t, map[string]string{"index.html": "one", "asset": "file"}), 201)
	request(t, s, "POST", deploy, zipFiles(t, map[string]string{"index.html": "two", "asset/nested.js": "nested"}), 201)
	if got := storedFile(t, store, "public/sites/demo/asset/nested.js"); got != "nested" {
		t.Fatal(got)
	}
	request(t, s, "POST", deploy, zipFiles(t, map[string]string{"index.html": "three", "asset": "file again"}), 201)
	if got := storedFile(t, store, "public/sites/demo/asset"); got != "file again" {
		t.Fatal(got)
	}
}

type failingStore struct{ hex.ObjectStore }

func (s failingStore) Put(ctx context.Context, key string, reader io.Reader) error {
	if strings.HasSuffix(key, "broken.js") {
		return errors.New("storage unavailable")
	}
	return s.ObjectStore.Put(ctx, key, reader)
}

func TestStagingFailureDoesNotChangePublicFolder(t *testing.T) {
	s, store := setup(t)
	request(t, s, "POST", "/api/sites/demo/deploy", zipFiles(t, map[string]string{"index.html": "working"}), 201)
	failing := hex.New(hex.Config{Sites: failingStore{store}})
	request(t, failing, "POST", "/api/sites/demo/deploy", zipFiles(t, map[string]string{"index.html": "broken", "broken.js": "broken"}), 500)
	if got := storedFile(t, store, "public/sites/demo/index.html"); got != "working" {
		t.Fatal(got)
	}
}

func TestGoDoesNotServePublishedSites(t *testing.T) {
	s, _ := setup(t)
	request(t, s, "POST", "/api/sites/demo/deploy", zipFiles(t, map[string]string{"index.html": "hello"}), 201)
	request(t, s, "GET", "/sites/demo/", nil, 404)
	request(t, s, "GET", "/sites/demo/index.html", nil, 404)
}

func TestDisabledCapabilitiesAndCrossOriginProtection(t *testing.T) {
	s := hex.New(hex.Config{})
	request(t, s, "GET", "/api/sites/demo/files", nil, 404)
	w := request(t, s, "GET", "/api/hex/capabilities", nil, 200)
	if !strings.Contains(w.Body.String(), `"files":false`) {
		t.Fatal(w.Body.String())
	}
	r := httptest.NewRequest("GET", "/api/hex/capabilities", nil)
	r.Header.Set("Origin", "https://evil.example")
	w = httptest.NewRecorder()
	s.ServeHTTP(w, r)
	if w.Code != 403 {
		t.Fatal(w.Code)
	}
	r = httptest.NewRequest("POST", "/api/sites/demo/deploy", nil)
	w = httptest.NewRecorder()
	s.ServeHTTP(w, r)
	if w.Code != 403 {
		t.Fatal(w.Code)
	}
}

func TestRealtimeBroadcastAndRoomIsolation(t *testing.T) {
	s, _ := setup(t)
	httpServer := httptest.NewServer(s)
	defer httpServer.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	url := "ws" + strings.TrimPrefix(httpServer.URL, "http") + "/api/sites/demo/realtime/room"
	a, _, err := websocket.Dial(ctx, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer a.CloseNow()
	b, _, err := websocket.Dial(ctx, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer b.CloseNow()
	other, _, err := websocket.Dial(ctx, strings.Replace(url, "/demo/", "/other/", 1), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer other.CloseNow()
	if err := a.Write(ctx, websocket.MessageText, []byte(`{"message":"hello"}`)); err != nil {
		t.Fatal(err)
	}
	for _, conn := range []*websocket.Conn{a, b} {
		_, data, err := conn.Read(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if string(data) != `{"message":"hello"}` {
			t.Fatal(string(data))
		}
	}
	short, stop := context.WithTimeout(ctx, 100*time.Millisecond)
	defer stop()
	if _, _, err := other.Read(short); err == nil {
		t.Fatal("message leaked across sites")
	}
}

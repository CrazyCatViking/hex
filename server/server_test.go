package hex_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	hex "github.com/crazycatviking/hex/server"
	"github.com/crazycatviking/hex/server/providers/local"
	"github.com/crazycatviking/hex/server/providers/memory"
)

func setup(t *testing.T) (*hex.Server, *local.Store) {
	t.Helper()
	store, err := local.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Errorf("close test store: %v", err)
		}
	})

	server := hex.New(hex.Config{
		Files:          store,
		Sites:          store,
		Database:       memory.NewDatabase(),
		Realtime:       memory.NewRealtime(),
		MaxUploadBytes: 4096,
	})
	return server, store
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
	server, _ := setup(t)
	path := "/api/sites/demo/files/reports/a.txt"
	request(t, server, "PUT", path, []byte("hello"), 200)
	if got := request(t, server, "GET", path, nil, 200).Body.String(); got != "hello" {
		t.Fatal(got)
	}
	request(t, server, "GET", "/api/sites/other/files/reports/a.txt", nil, 404)
	list := request(t, server, "GET", "/api/sites/demo/files", nil, 200)
	if !strings.Contains(list.Body.String(), `"key":"reports/a.txt"`) {
		t.Fatal(list.Body.String())
	}

	request(t, server, "PUT", path, bytes.Repeat([]byte("x"), 4097), 413)
	if got := request(t, server, "GET", path, nil, 200).Body.String(); got != "hello" {
		t.Fatal("oversize upload replaced file")
	}
	request(t, server, "GET", "/api/sites/demo/files/%2e%2e/secret", nil, 400)
	request(t, server, "DELETE", path, nil, 204)
	request(t, server, "GET", path, nil, 404)
}

func TestDatabaseCRUDAndPagination(t *testing.T) {
	server, _ := setup(t)
	base := "/api/sites/demo/db/tasks"
	request(t, server, "PUT", base+"/a", []byte(`{"title":"A"}`), 200)
	request(t, server, "PUT", base+"/b", []byte(`{"title":"B"}`), 200)
	request(t, server, "PUT", base+"/a", []byte(`{"done":true}`), 200)
	got := request(t, server, "GET", base+"/a", nil, 200)
	if strings.Contains(got.Body.String(), "title") {
		t.Fatal("set did not replace document")
	}

	page := request(t, server, "GET", base+"?limit=1&after=a", nil, 200)
	var documents []hex.Document
	if err := json.Unmarshal(page.Body.Bytes(), &documents); err != nil {
		t.Fatal(err)
	}
	if len(documents) != 1 || documents[0].ID != "b" {
		t.Fatal(documents)
	}
	request(t, server, "GET", "/api/sites/other/db/tasks/a", nil, 404)
	request(t, server, "POST", base, []byte(`[]`), 400)
	request(t, server, "POST", base, []byte(`{"ok":true} trailing`), 400)

	created := request(t, server, "POST", base, []byte(`{"ok":true}`), 201)
	var document hex.Document
	if err := json.Unmarshal(created.Body.Bytes(), &document); err != nil {
		t.Fatal(err)
	}
	request(t, server, "GET", base+"/"+document.ID, nil, 200)
	request(t, server, "DELETE", base+"/a", nil, 204)
	request(t, server, "GET", base+"/a", nil, 404)
	request(t, server, "GET", base+"?limit=101", nil, 400)
}

func TestSiteDiscoveryUsesFilesWithoutMetadata(t *testing.T) {
	server, store := setup(t)
	ctx := context.Background()
	if body := request(t, server, "GET", "/api/sites", nil, 200).Body.String(); body != "[]\n" {
		t.Fatal(body)
	}

	for _, key := range []string{
		"public/sites/bravo/index.html",
		"public/sites/alpha/index.html",
		"public/sites/incomplete/nested/index.html",
		"public/sites/.hidden/index.html",
		"sites/ghost.json",
		"releases/old/index.html",
	} {
		if err := store.Put(ctx, key, strings.NewReader("not metadata")); err != nil {
			t.Fatal(err)
		}
	}

	response := request(t, server, "GET", "/api/sites", nil, 200)
	var sites []hex.Site
	if err := json.Unmarshal(response.Body.Bytes(), &sites); err != nil {
		t.Fatal(err)
	}
	if len(sites) != 2 || sites[0].Name != "alpha" || sites[1].URL != "http://bravo.localhost:8080/" {
		t.Fatalf("unexpected directory listing: %+v", sites)
	}
	if strings.Contains(response.Body.String(), "release") || strings.Contains(response.Body.String(), "publishedAt") {
		t.Fatal("site listing contains metadata")
	}

	if err := store.Delete(ctx, "public/sites/alpha/index.html"); err != nil {
		t.Fatal(err)
	}
	response = request(t, server, "GET", "/api/sites", nil, 200)
	if err := json.Unmarshal(response.Body.Bytes(), &sites); err != nil {
		t.Fatal(err)
	}
	if len(sites) != 1 || sites[0].Name != "bravo" {
		t.Fatal("listing did not reflect a direct filesystem deletion")
	}
}

func TestServerCannotPublishUnpublishOrServeSites(t *testing.T) {
	server, store := setup(t)
	key := "public/sites/demo/index.html"
	if err := store.Put(context.Background(), key, strings.NewReader("published directly")); err != nil {
		t.Fatal(err)
	}

	request(t, server, "POST", "/api/sites/demo/deploy", []byte("anything"), 404)
	request(t, server, "DELETE", "/api/sites/demo", nil, 404)
	request(t, server, "GET", "/sites/demo/", nil, 404)

	reader, err := store.Open(context.Background(), key)
	if err != nil {
		t.Fatal(err)
	}
	data, readError := io.ReadAll(reader)
	closeError := reader.Close()
	if readError != nil || closeError != nil || string(data) != "published directly" {
		t.Fatalf("unexpected stored file: %s, read: %v, close: %v", data, readError, closeError)
	}
}

func TestDisabledCapabilitiesAndCrossOriginProtection(t *testing.T) {
	server := hex.New(hex.Config{})
	request(t, server, "GET", "/api/sites/demo/files", nil, 404)
	request(t, server, "GET", "/api/sites", nil, 404)
	response := request(t, server, "GET", "/api/hex/capabilities", nil, 200)
	if !strings.Contains(response.Body.String(), `"files":false`) {
		t.Fatal(response.Body.String())
	}

	r := httptest.NewRequest("GET", "/api/hex/capabilities", nil)
	r.Header.Set("Origin", "https://evil.example")
	w := httptest.NewRecorder()
	server.ServeHTTP(w, r)
	if w.Code != http.StatusForbidden {
		t.Fatal(w.Code)
	}

	r = httptest.NewRequest("PUT", "/api/sites/demo/files/test", nil)
	w = httptest.NewRecorder()
	server.ServeHTTP(w, r)
	if w.Code != http.StatusForbidden {
		t.Fatal(w.Code)
	}
}

func TestRealtimeBroadcastAndRoomIsolation(t *testing.T) {
	server, _ := setup(t)
	httpServer := httptest.NewServer(server)
	defer httpServer.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	url := "ws" + strings.TrimPrefix(httpServer.URL, "http") + "/api/sites/demo/realtime/room"
	sender := dialWebSocket(t, ctx, url)
	subscriber := dialWebSocket(t, ctx, url)
	other := dialWebSocket(t, ctx, strings.Replace(url, "/demo/", "/other/", 1))

	if err := sender.Write(ctx, websocket.MessageText, []byte(`{"message":"hello"}`)); err != nil {
		t.Fatal(err)
	}
	for _, connection := range []*websocket.Conn{sender, subscriber} {
		_, data, err := connection.Read(ctx)
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

func dialWebSocket(t *testing.T, ctx context.Context, url string) *websocket.Conn {
	t.Helper()
	connection, _, err := websocket.Dial(ctx, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := connection.CloseNow(); err != nil {
			t.Logf("test WebSocket already closed: %v", err)
		}
	})
	return connection
}

package cli

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type cliRoundTripFunc func(*http.Request) (*http.Response, error)

func (f cliRoundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

type countedResponseBody struct {
	io.Reader
	read int
}

func (b *countedResponseBody) Read(data []byte) (int, error) {
	n, err := b.Reader.Read(data)
	b.read += n
	return n, err
}

func (b *countedResponseBody) Close() error { return nil }

func TestAPIResponseSizeBoundary(t *testing.T) {
	t.Setenv("HEX_TOKEN", "")
	for _, test := range []struct {
		name      string
		body      string
		oversized bool
	}{
		{"exact limit", `{}` + strings.Repeat(" ", maxAPIResponseBytes-2), false},
		{"valid prefix overflow", `{}` + strings.Repeat(" ", maxAPIResponseBytes), true},
		{"JSON value overflow", `"` + strings.Repeat("a", maxAPIResponseBytes) + `"`, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			body := &countedResponseBody{Reader: strings.NewReader(test.body)}
			app, _ := testApp(t, t.TempDir())
			app.HTTP.Transport = cliRoundTripFunc(func(request *http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"application/json"}}, Body: body}, nil
			})
			data, err := app.apiRequest(context.Background(), Project{Server: "https://hex.example.com"}, "/api/sites")
			if test.oversized {
				if !errors.Is(err, errAPIResponseTooLarge) || data != nil {
					t.Fatalf("overflow was not explicit: %d bytes, %v", len(data), err)
				}
			} else if err != nil || string(data) != test.body {
				t.Fatalf("exact limit rejected: %v", err)
			}
			if body.read > maxAPIResponseBytes+1 {
				t.Fatalf("unbounded response read: %d bytes", body.read)
			}
		})
	}
}

func TestDataListOversizedResponseGuidance(t *testing.T) {
	directory := t.TempDir()
	saveReadProfile(t, directory, "https://hex.example.com")
	app, _ := testApp(t, directory)
	app.HTTP.Transport = cliRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		body := io.NopCloser(strings.NewReader(`[]` + strings.Repeat(" ", maxAPIResponseBytes)))
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"application/json"}}, Body: body}, nil
	})
	err := app.Execute(context.Background(), []string{"data", "list", "--site", "demo", "--collection", "tasks"}, "test")
	if !errors.Is(err, errAPIResponseTooLarge) || !strings.Contains(err.Error(), "reduce --limit") || !strings.Contains(err.Error(), "--after") {
		t.Fatalf("missing pagination guidance: %v", err)
	}
}

func TestCapabilitiesServerOverrideBypassesCache(t *testing.T) {
	directory := t.TempDir()
	requests := 0
	live := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.URL.Path != "/api/hex/capabilities" {
			t.Errorf("unexpected request: %s", r.URL)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"version":1,"sites":false,"files":false,"database":true,"realtime":false,"maxUploadBytes":4096}`)
	}))
	defer live.Close()
	saveReadProfile(t, directory, "https://cached.example.com")
	if got := run(t, directory, "capabilities"); !strings.Contains(got, `"sites": true`) {
		t.Fatal(got)
	}
	if got := run(t, directory, "capabilities", "--server", live.URL); !strings.Contains(got, `"sites": false`) || !strings.Contains(got, `"database": true`) {
		t.Fatal(got)
	}
	if requests != 1 {
		t.Fatalf("expected one live request, got %d", requests)
	}
}

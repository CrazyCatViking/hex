package hex_test

import (
	"context"
	"encoding/base64"
	"net/http"
	"strings"
	"testing"

	hex "github.com/crazycatviking/hex/server"
	"github.com/crazycatviking/hex/server/providers/local"
)

const testAppIcon = `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 32 32"><path id="app-logo" d="M0 0h32v32H0z" fill="red"/></svg>`

func TestPortalDiscoversPublishedFavicons(t *testing.T) {
	png, err := base64.StdEncoding.DecodeString("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+jD1cAAAAASUVORK5CYII=")
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name        string
		index       string
		file        string
		data        string
		contentType string
	}{
		{"declared SVG", `<head><link rel="icon" href="assets/logo.svg?v=1"></head>`, "assets/logo.svg", testAppIcon, "image/svg+xml"},
		{"shortcut icon", `<head><link rel="SHORTCUT ICON" href="/assets/logo.svg"></head>`, "assets/logo.svg", testAppIcon, "image/svg+xml"},
		{"base URL", `<head><base href="/assets/"><link rel="icon" href="logo.svg"></head>`, "assets/logo.svg", testAppIcon, "image/svg+xml"},
		{"same-origin absolute", `<head><link rel="icon" href="http://demo.example.com/assets/logo.svg"></head>`, "assets/logo.svg", testAppIcon, "image/svg+xml"},
		{"touch icon", `<head><link rel="apple-touch-icon" href="/touch.png"></head>`, "touch.png", string(png), "image/png"},
		{"conventional SVG", `<head><title>App</title></head>`, "favicon.svg", testAppIcon, "image/svg+xml"},
		{"conventional ICO", `<head></head>`, "favicon.ico", "\x00\x00\x01\x00\x01\x00", "image/x-icon"},
		{"missing declaration falls back", `<head><link rel="icon" href="/missing.svg"></head>`, "favicon.png", string(png), "image/png"},
		{"external declaration falls back", `<head><link rel="icon" href="https://outside.example/icon.svg"></head>`, "favicon.svg", testAppIcon, "image/svg+xml"},
		{"unsafe declaration falls back", `<head><link rel="icon" href="data:image/svg+xml,unsafe"></head>`, "favicon.svg", testAppIcon, "image/svg+xml"},
		{"hidden file falls back", `<head><link rel="icon" href="/.private.svg"></head>`, "favicon.svg", testAppIcon, "image/svg+xml"},
	} {
		t.Run(test.name, func(t *testing.T) {
			store, err := local.New(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := store.Close(); err != nil {
					t.Error(err)
				}
			})
			for file, data := range map[string]string{"index.html": test.index, test.file: test.data} {
				if err := store.WriteSiteFile(context.Background(), "demo", file, int64(len(data)), strings.NewReader(data)); err != nil {
					t.Fatal(err)
				}
			}
			// Read-only directory compositions work without a configured publisher.
			server := hex.New(hex.Config{Sites: store, SiteBaseURL: "http://example.com"})
			page := request(t, server, "GET", "http://example.com/", nil, 200)
			if !strings.Contains(page.Body.String(), `src="/api/hex/sites/demo/icon"`) {
				t.Fatal("portal did not render an app icon")
			}
			icon := request(t, server, "GET", "http://example.com/api/hex/sites/demo/icon", nil, 200)
			if icon.Header().Get("Content-Type") != test.contentType || icon.Body.String() != test.data {
				t.Fatalf("wrong icon: %s %s", icon.Header().Get("Content-Type"), icon.Body.String())
			}
			if icon.Header().Get("Cache-Control") != "no-store" || !strings.Contains(icon.Header().Get("Content-Security-Policy"), "sandbox") {
				t.Fatal("icon preview must preserve session isolation")
			}
		})
	}
}

func TestPortalIconAuthorizationAndFallback(t *testing.T) {
	server, store, _ := analyticsFixture(t)
	owner := principalHeaders("owner")
	viewer := principalHeaders("viewer")
	publishSite(t, server, owner, "demo", map[string]string{"index.html": `<head><link rel="icon" href="/private/icon.svg"></head>`, "private/icon.svg": testAppIcon}, `{"viewers":["user:owner","user:viewer"],"paths":[{"prefix":"/private/","viewers":"owners"}]}`)
	path := "/api/hex/sites/demo/icon"
	requestAs(t, server, nil, "GET", path, nil, http.StatusUnauthorized)
	requestAs(t, server, principalHeaders("stranger"), "GET", path, nil, http.StatusForbidden)
	icon := requestAs(t, server, owner, "GET", path, nil, 200)
	if icon.Body.String() != testAppIcon {
		t.Fatal("owner could not view their icon")
	}
	icon = requestAs(t, server, viewer, "GET", path, nil, 200)
	if strings.Contains(icon.Body.String(), "app-logo") || !strings.Contains(icon.Body.String(), "<text") {
		t.Fatal("path-restricted icon was exposed instead of initials")
	}
	requestAs(t, server, owner, "GET", "http://demo.example.com"+path, nil, 403)
	for _, page := range []string{"/manage", "/manage/demo", "/"} {
		if !strings.Contains(requestAs(t, server, owner, "GET", page, nil, 200).Body.String(), `src="/api/hex/sites/demo/icon"`) {
			t.Fatalf("icon missing on %s", page)
		}
	}
	// Non-images and oversized images fall back without serving app markup.
	for _, data := range []string{`<html><script>bad()</script></html>`, strings.Repeat("x", 257<<10)} {
		if err := store.WriteSiteFile(context.Background(), "demo", "private/icon.svg", int64(len(data)), strings.NewReader(data)); err != nil {
			t.Fatal(err)
		}
		icon := requestAs(t, server, owner, "GET", path, nil, 200)
		if !strings.Contains(icon.Body.String(), "<text") || strings.Contains(icon.Body.String(), "<script>") {
			t.Fatal("invalid image was not replaced by initials")
		}
	}
}

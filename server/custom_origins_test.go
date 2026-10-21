package hex

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestExplicitHostingOrigins(t *testing.T) {
	s := New(Config{ResolveOrigin: func(r *http.Request) (RequestOrigin, error) {
		switch r.Host {
		case "hex.customer.com":
			return RequestOrigin{}, nil
		case "reports.hex.customer.com":
			return RequestOrigin{Site: "reports"}, nil
		default:
			return RequestOrigin{}, errors.New("unknown origin")
		}
	}})
	for _, tc := range []struct {
		host, path string
		denied     bool
	}{
		{"reports.hex.customer.com", "/api/hex/capabilities", false},
		{"reports.hex.customer.com", "/api/sites/reports/db/tasks", false},
		{"reports.hex.customer.com", "/api/sites/other/db/tasks", true},
		{"reports.hex.customer.com", "/api/hex/config", true},
		{"reports.hex.customer.com", "/api/hex/sites/reports/access", true},
		{"reports.hex.customer.com", "/", true},
		{"hex.customer.com", "/api/hex/capabilities", false},
		{"unregistered.customer.com", "/api/hex/capabilities", true},
		{"reports.localhost", "/api/hex/capabilities", true},
	} {
		t.Run(tc.host+tc.path, func(t *testing.T) {
			r := httptest.NewRequest("GET", "https://"+tc.host+tc.path, nil)
			w := httptest.NewRecorder()
			s.ServeHTTP(w, r)
			if (w.Code == http.StatusForbidden) != tc.denied {
				t.Fatalf("status %d: %s", w.Code, w.Body.String())
			}
		})
	}
}

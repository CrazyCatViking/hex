package hex_test

import (
	"net/http"
	"testing"
)

func TestAppOriginAPIBoundary(t *testing.T) {
	server, _ := setupWithAccess(t)
	owner := principalHeaders("victim")
	putPolicy(t, server, owner, "private", `{"owners":["user:victim"],"viewers":["user:victim"]}`)
	requestAs(t, server, owner, "PUT", "/api/sites/private/db/tasks/a", []byte(`{"secret":true}`), 200)

	for _, host := range []string{"untrusted.localhost:8080", "UNTRUSTED.LOCALHOST:8080", "untrusted.localhost.:8080"} {
		t.Run(host, func(t *testing.T) {
			headers := owner.Clone()
			headers.Set("Origin", "https://"+host)
			headers.Set("Sec-Fetch-Site", "same-origin")
			base := "https://" + host
			requestAs(t, server, headers, "PUT", base+"/api/hex/sites/private/access", []byte(`{"owners":["user:victim","user:attacker"]}`), 403)
			for _, path := range []string{
				"/api/sites/private/db/tasks/a", "/api/sites/private/files", "/api/sites/private/realtime/updates",
				"/api/hex/sites/private/permissions", "/api/hex/manage/sites/private/collections",
				"/api/hex/my-sites", "/api/sites", "/api/hex/directory",
				"/api/hex/sites/untrusted/access",
			} {
				requestAs(t, server, headers, "GET", base+path, nil, http.StatusForbidden)
			}
			requestAs(t, server, headers, "GET", base+"/api/hex/me", nil, 200)
			requestAs(t, server, headers, "GET", base+"/api/hex/capabilities", nil, 200)
			requestAs(t, server, headers, "GET", base+"/api/hex/sites/untrusted/permissions", nil, 200)
			requestAs(t, server, headers, "PUT", base+"/api/sites/untrusted/db/tasks/a", []byte(`{"ok":true}`), 200)
			requestAs(t, server, headers, "GET", base+"/api/sites/untrusted/db/tasks/a", nil, 200)
		})
	}
	requestAs(t, server, principalHeaders("attacker"), "GET", "/api/hex/sites/private/access", nil, 403)
	requestAs(t, server, owner, "GET", "https://localhost:8080/api/sites/private/db/tasks/a", nil, 200)
}

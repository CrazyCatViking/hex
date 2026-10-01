package hex_test

import (
	"bytes"
	"net/http/httptest"
	"sync"
	"testing"

	hex "github.com/crazycatviking/hex/server"
	"github.com/crazycatviking/hex/server/providers/easyauth"
	"github.com/crazycatviking/hex/server/providers/memory"
)

func TestPolicyMutationsIgnoreStaleAuthorization(t *testing.T) {
	store := memory.NewAccessStore()
	config := hex.Config{Identity: easyauth.Resolver{}, Access: store}
	first, second := hex.New(config), hex.New(config)
	victim, attacker := principalHeaders("victim"), principalHeaders("attacker")
	requestAs(t, second, attacker, "GET", "/api/hex/sites/newsite/permissions", nil, 200)
	putPolicy(t, first, victim, "newsite", `{"owners":["user:victim"]}`)
	requestAs(t, second, attacker, "PUT", "/api/hex/sites/newsite/access", []byte(`{"owners":["user:attacker"]}`), 403)

	// An owner revoked through another instance cannot restore or delete a policy.
	putPolicy(t, first, victim, "newsite", `{"owners":["user:victim","user:attacker"]}`)
	third := hex.New(config)
	requestAs(t, third, attacker, "GET", "/api/hex/sites/newsite/access", nil, 200)
	putPolicy(t, first, victim, "newsite", `{"owners":["user:victim"]}`)
	requestAs(t, third, attacker, "PUT", "/api/hex/sites/newsite/access", []byte(`{"owners":["user:attacker"]}`), 403)
	requestAs(t, third, attacker, "DELETE", "/api/hex/sites/newsite/access", nil, 403)
}

func TestConcurrentSiteClaimsHaveOneWinner(t *testing.T) {
	store := memory.NewAccessStore()
	config := hex.Config{Identity: easyauth.Resolver{}, Access: store}
	start := make(chan struct{})
	results := make(chan int, 2)
	var workers sync.WaitGroup
	for _, id := range []string{"first", "second"} {
		server := hex.New(config)
		workers.Add(1)
		go func() {
			defer workers.Done()
			r := httptest.NewRequest("PUT", "/api/hex/sites/newsite/access", bytes.NewBufferString(`{}`))
			r.Header = principalHeaders(id)
			r.Header.Set("X-Hex-Request", "1")
			w := httptest.NewRecorder()
			<-start
			server.ServeHTTP(w, r)
			results <- w.Code
		}()
	}
	close(start)
	workers.Wait()
	close(results)
	counts := map[int]int{}
	for code := range results {
		counts[code]++
	}
	if counts[200] != 1 || counts[403] != 1 {
		t.Fatalf("expected one owner and one rejected claim, got %v", counts)
	}
}

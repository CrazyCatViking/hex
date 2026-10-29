package hex_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	hex "github.com/crazycatviking/hex/server"
	"github.com/crazycatviking/hex/server/providers/easyauth"
)

func TestSiteCallsAuthorizationIdempotencyAndIsolation(t *testing.T) {
	ctx := context.Background()
	database, policies, _ := durableTestStores(t)
	registry := &hex.ActionRegistry{}
	var calls atomic.Int32
	err := registry.Register("target", hex.Action{Definition: hex.ActionDefinition{Name: "submit", Description: "Submit a request", Version: 1, Exported: true, InputSchema: json.RawMessage(`{"type":"object","additionalProperties":false}`), OutputSchema: json.RawMessage(`{"type":"object"}`)}, Handler: func(ctx context.Context, caller hex.ActionContext, input json.RawMessage) (any, error) {
		calls.Add(1)
		if caller.SourceSite != "source" || caller.Identity.ID != "alice" {
			t.Errorf("wrong caller: %+v", caller)
		}
		return map[string]string{"ok": "yes"}, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	source := hex.SiteAccess{Version: 2, Admission: hex.Subjects{SignedIn: true}, Grants: map[string]hex.PermissionGrant{"out": {Kind: "site-action", Resource: "target.submit", Operations: []string{"run"}, Subjects: hex.Subjects{SignedIn: true}}}}
	if err := policies.PutSiteAccess(ctx, "source", source); err != nil {
		t.Fatal(err)
	}
	config := hex.Config{Identity: easyauth.Resolver{}, Database: database, Access: policies, Actions: registry, SiteOperationsPerMinute: 2}
	server := hex.New(config)
	headers := principalHeaders("alice")
	identityResponse := requestAs(t, server, headers, "GET", "/api/sites/source/site-api/identity", nil, 200)
	var identity hex.Identity
	if err := json.Unmarshal(identityResponse.Body.Bytes(), &identity); err != nil {
		t.Fatal(err)
	}
	target := hex.SiteAccess{Version: 2, Grants: map[string]hex.PermissionGrant{
		"consumer": {Kind: "action-consumer", Resource: "submit", Operations: []string{"run"}, Subjects: hex.Subjects{Principals: []string{identity.ID}}},
		"run":      {Kind: "action", Resource: "submit", Operations: []string{"run"}, Subjects: hex.Subjects{Principals: []string{"user:alice"}}},
	}}
	if err := policies.PutSiteAccess(ctx, "target", target); err != nil {
		t.Fatal(err)
	}
	body := []byte(`{"target":"target","action":"submit","version":1,"input":{},"idempotencyKey":"one"}`)
	requestAs(t, server, headers, "POST", "/api/sites/source/site-api/call", body, 200)
	server = hex.New(config) // completed results survive server replacement
	requestAs(t, server, headers, "POST", "/api/sites/source/site-api/call", body, 200)
	if calls.Load() != 1 {
		t.Fatal("mutation repeated")
	}
	requestAs(t, server, headers, "POST", "/api/sites/source/site-api/call", []byte(strings.Replace(string(body), `"one"`, `"two"`, 1)), 429)
	requestAs(t, server, principalHeaders("bob"), "POST", "/api/sites/source/site-api/call", body, 403)
	requestAs(t, server, headers, "POST", "/api/sites/source/site-api/call", []byte(strings.Replace(string(body), `"version":1`, `"version":2`, 1)), 403)
	requestAs(t, server, headers, "POST", "/api/sites/source/site-api/call", []byte(strings.Replace(string(body), `"input":{}`, `"input":{"unexpected":true}`, 1)), 400)
	req := httptest.NewRequest("POST", "http://other.localhost:8080/api/sites/source/site-api/call", strings.NewReader(string(body)))
	req.Header = headers
	response := httptest.NewRecorder()
	server.ServeHTTP(response, req)
	if response.Code != 403 {
		t.Fatal("source spoof accepted", response.Code)
	}
	delete(target.Grants, "consumer")
	if err := policies.PutSiteAccess(ctx, "target", target); err != nil {
		t.Fatal(err)
	}
	requestAs(t, server, headers, "POST", "/api/sites/source/site-api/call", body, 403)
	requestAs(t, server, headers, "GET", fmt.Sprintf("/api/sites/source/db/identities/%s", identity.ID), nil, 400)
}

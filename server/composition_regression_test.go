package hex_test

import (
	"context"
	"encoding/json"
	"errors"
	"html"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	hex "github.com/crazycatviking/hex/server"
	"github.com/crazycatviking/hex/server/providers/local"
	"github.com/crazycatviking/hex/server/providers/memory"
	"golang.org/x/oauth2"
)

func TestAutomationAIDryRunCannotWrite(t *testing.T) {
	registry := new(hex.IntegrationRegistry)
	var writes atomic.Int32
	if err := registry.Register(hex.Integration{Name: "chat", Title: "Chat", Endpoints: []hex.IntegrationEndpoint{{
		Name: "post", Description: "Post a message", Write: true, InputSchema: objectSchema,
		OutputSchema: json.RawMessage(`{"type":"object","required":["posted"],"properties":{"posted":{"type":"boolean"}}}`),
		Handler: func(context.Context, hex.IntegrationCall, json.RawMessage) (any, error) {
			writes.Add(1)
			return map[string]bool{"posted": true}, nil
		},
	}}}); err != nil {
		t.Fatal(err)
	}
	access := memory.NewAccessStore()
	if err := access.PutSiteAccess(context.Background(), "demo", hex.SiteAccess{Owners: []string{"user:owner"}}); err != nil {
		t.Fatal(err)
	}
	provider := &scriptedProvider{turns: [][]hex.AIEvent{
		assistantTurn(hex.StopToolUse, hex.AIContent{Type: hex.ContentToolCall, ToolCallID: "call", Name: "chat__post", Input: json.RawMessage(`{}`)}),
		assistantTurn(hex.StopEndTurn, hex.AIContent{Type: hex.ContentText, Text: "preview"}),
	}}
	server := hex.New(hex.Config{
		Identity: hex.StaticIdentity{Identity: hex.Identity{ID: "owner"}}, Access: access,
		Integrations: registry, Automations: memory.NewAutomationStore(), AI: &hex.AIConfig{Provider: provider},
		IntegrationGrants: []hex.IntegrationGrant{{Principal: "site:demo", Permissions: []string{"ai", "chat.post"}}},
	})
	response := requestAs(t, server, roleHeaders("owner"), "POST", "/api/hex/sites/demo/automations/test",
		[]byte(`{"name":"preview","steps":[{"id":"ai","ai":{"model":"general","prompt":"Post","tools":["chat.post"]}}]}`), 202)
	var started hex.AutomationRun
	if err := json.Unmarshal(response.Body.Bytes(), &started); err != nil {
		t.Fatal(err)
	}
	run := waitForRun(t, server, "/api/hex/sites/demo/automation-runs/"+started.ID)
	if run.Status != hex.RunSucceeded || !run.DryRun || writes.Load() != 0 {
		t.Fatalf("dry-run write escaped: run=%+v writes=%d", run, writes.Load())
	}
	provider.mu.Lock()
	defer provider.mu.Unlock()
	if len(provider.requests) != 2 || !strings.Contains(string(provider.requests[1].Messages[2].Content[0].Input), "wouldCall") &&
		!strings.Contains(provider.requests[1].Messages[2].Content[0].Text, "wouldCall") {
		t.Fatal("AI did not receive a simulated tool result")
	}
}

type unavailableEventStore struct{ *memory.Analytics }

func (*unavailableEventStore) RecordSiteEvents(context.Context, []hex.SiteEvent) error {
	return errors.New("analytics unavailable")
}

func TestUnpublishSharesCleanupAndHistoryRecovery(t *testing.T) {
	for _, portal := range []bool{false, true} {
		for _, unavailable := range []bool{false, true} {
			name := "api"
			if portal {
				name = "portal"
			}
			if unavailable {
				name += "/recovery-failure"
			}
			t.Run(name, func(t *testing.T) {
				ctx := context.Background()
				files, err := local.New(t.TempDir())
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() {
					if err := files.Close(); err != nil {
						t.Error(err)
					}
				})
				at := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
				metadata := hex.SiteMetadata{Title: "Demo", CreatedAt: at, PublishedAt: at.Add(2 * time.Hour)}
				history := []hex.Publication{{PublishedAt: at.Add(time.Hour)}, {PublishedAt: metadata.PublishedAt}}
				for path, body := range map[string][]byte{"index.html": []byte("hello"), ".hex-site.json": encode(t, metadata), ".hex-history.json": encode(t, history)} {
					if err := files.WriteSiteFile(ctx, "demo", path, int64(len(body)), strings.NewReader(string(body))); err != nil {
						t.Fatal(err)
					}
				}
				access := memory.NewAccessStore()
				if err := access.PutSiteAccess(ctx, "demo", hex.SiteAccess{Owners: []string{"user:owner"}}); err != nil {
					t.Fatal(err)
				}
				automations := memory.NewAutomationStore()
				if err := automations.ReplaceSiteAutomations(ctx, "demo", []hex.ScheduledAutomation{{Site: "demo", Automation: hex.Automation{Name: "scheduled", Schedule: "* * * * *"}, NextRun: time.Now()}}); err != nil {
					t.Fatal(err)
				}
				analytics := memory.NewAnalytics()
				var events hex.AnalyticsStore = analytics
				if unavailable {
					events = &unavailableEventStore{Analytics: analytics}
				}
				server := hex.New(hex.Config{Sites: files, Publisher: files, Access: access, Automations: automations, Analytics: events,
					Identity: hex.StaticIdentity{Identity: hex.Identity{ID: "owner"}}})
				method, path, body := "DELETE", "/api/hex/sites/demo", ""
				if portal {
					method, path, body = "POST", "/api/hex/manage/sites/demo/unpublish", "confirm=demo"
				}
				request := httptest.NewRequest(method, path, strings.NewReader(body))
				request.Header.Set("X-Hex-Request", "1")
				request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
				response := httptest.NewRecorder()
				server.ServeHTTP(response, request)
				want := http.StatusNoContent
				if unavailable {
					want = http.StatusInternalServerError
				}
				if response.Code != want {
					t.Fatalf("status=%d: %s", response.Code, response.Body.String())
				}
				_, present := readSiteFile(t, files, "demo", "index.html")
				remaining, err := automations.ListSiteAutomations(ctx, "demo")
				if err != nil {
					t.Fatal(err)
				}
				if unavailable {
					if !present || len(remaining) != 1 {
						t.Fatal("failed history recovery destroyed site or schedule")
					}
					return
				}
				if present || len(remaining) != 0 {
					t.Fatal("unpublishing left assets or scheduled work")
				}
				report, err := analytics.QueryAnalytics(ctx, hex.AnalyticsQuery{From: at, Until: time.Now().Add(24 * time.Hour), Site: "demo"})
				if err != nil {
					t.Fatal(err)
				}
				if report.Created != 1 || report.Publications != 2 || report.Unpublished != 1 {
					t.Fatalf("lost history: %+v", report)
				}
			})
		}
	}
}

func TestConnectorOnlyCompositionRegistersAPI(t *testing.T) {
	registry := new(hex.IntegrationRegistry)
	if err := registry.RegisterConnector(hex.Connector{Name: "docs", Title: "Docs", OAuth2: oauth2.Config{
		ClientID: "client", Endpoint: oauth2.Endpoint{AuthURL: "https://identity.test/authorize", TokenURL: "https://identity.test/token"},
	}}); err != nil {
		t.Fatal(err)
	}
	server := hex.New(hex.Config{Identity: hex.StaticIdentity{Identity: hex.Identity{ID: "owner"}}, Integrations: registry,
		IntegrationStore: memory.NewIntegrationStore(), CredentialKey: make([]byte, 32)})
	response := requestAs(t, server, roleHeaders("owner"), "GET", "/api/hex/capabilities", nil, 200)
	if !strings.Contains(response.Body.String(), `"connections":true`) {
		t.Fatal(response.Body.String())
	}
	requestAs(t, server, roleHeaders("owner"), "GET", "/api/hex/connections", nil, 200)
	requestAs(t, server, roleHeaders("owner"), "GET", "/api/hex/connections/docs/start", nil, 302)
}

func TestPortalUsesHostLogoutAndCapabilityControls(t *testing.T) {
	identity := hex.StaticIdentity{Identity: hex.Identity{ID: "owner"}}
	server := hex.New(hex.Config{Identity: identity, SiteBaseURL: "http://example.com"})
	page := requestAs(t, server, roleHeaders("owner"), "GET", "/", nil, 200).Body.String()
	if strings.Contains(page, `href="/manage"`) || strings.Contains(page, "/.auth/logout") || strings.Contains(page, "Sign out") {
		t.Fatal("disabled management or host logout appeared")
	}
	server = hex.New(hex.Config{Identity: identity, SiteBaseURL: "http://example.com", LogoutURL: "https://identity.test/signout"})
	page = requestAs(t, server, roleHeaders("owner"), "GET", "/", nil, 200).Body.String()
	if !strings.Contains(page, `href="https://identity.test/signout"`) {
		t.Fatal("host logout missing")
	}
	files, err := local.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := files.Close(); err != nil {
			t.Error(err)
		}
	})
	access := memory.NewAccessStore()
	if err := access.PutSiteAccess(context.Background(), "demo", hex.SiteAccess{Owners: []string{"user:owner"}}); err != nil {
		t.Fatal(err)
	}
	server = hex.New(hex.Config{Identity: identity, Access: access, Sites: files, SiteBaseURL: "http://example.com"})
	page = requestAs(t, server, roleHeaders("owner"), "GET", "/manage/demo?tab=settings", nil, 200).Body.String()
	if strings.Contains(page, `hx-post="/api/hex/manage/sites/demo/unpublish"`) || !strings.Contains(page, "Publishing unavailable") {
		t.Fatal("read-only management offered unpublishing")
	}
}

func TestOpaquePortalMutationURLsAndOwnerLabels(t *testing.T) {
	ctx := context.Background()
	registry := new(hex.IntegrationRegistry)
	if err := registry.RegisterConnector(hex.Connector{Name: "docs", Title: "Docs", OAuth2: oauth2.Config{
		ClientID: "client", Endpoint: oauth2.Endpoint{AuthURL: "https://identity.test/authorize", TokenURL: "https://identity.test/token"},
	}}); err != nil {
		t.Fatal(err)
	}
	connections := memory.NewIntegrationStore()
	people := memory.NewPeopleStore()
	const opaque = "subject/part?tenant=A+B&other=C#fragment"
	owners := map[string]string{"Subject:AbC": "Upper case person", "subject:abc": "Lower case person", "subject": "Prefix person", opaque: "Opaque person"}
	for id, name := range owners {
		if err := people.RememberPerson(ctx, hex.Person{ID: id, Name: name}); err != nil {
			t.Fatal(err)
		}
		if err := connections.PutCredential(ctx, hex.CredentialRecord{Owner: id, Connector: "docs", Sealed: []byte{1}, ConnectedAt: time.Now(), LastUsedAt: time.Now()}); err != nil {
			t.Fatal(err)
		}
	}
	server := hex.New(hex.Config{Identity: hex.StaticIdentity{Identity: hex.Identity{ID: "admin"}}, AdminGroups: []string{"user:admin"}, SiteBaseURL: "http://example.com",
		Integrations: registry, IntegrationStore: connections, People: people, CredentialKey: make([]byte, 32)})
	page := html.UnescapeString(requestAs(t, server, roleHeaders("admin"), "GET", "/connections", nil, 200).Body.String())
	for id, name := range owners {
		remove := "/api/hex/manage/connections/docs/" + url.PathEscape(id)
		if !strings.Contains(page, name) || !strings.Contains(page, `hx-delete="`+remove+`"`) {
			t.Fatalf("owner %q not rendered with an exact label and encoded URL", id)
		}
	}
	requestAs(t, server, roleHeaders("admin"), "DELETE", "/api/hex/manage/connections/docs/"+url.PathEscape(opaque), nil, 200)
	for id := range owners {
		_, err := connections.GetCredential(ctx, id, "docs")
		if id == opaque {
			if !errors.Is(err, hex.ErrNotFound) {
				t.Fatalf("opaque connection survived: %v", err)
			}
		} else if err != nil {
			t.Fatalf("removal affected %q: %v", id, err)
		}
	}
	for id := range owners {
		found, err := people.GetPeople(ctx, []string{id})
		if err != nil || len(found) != 1 || found[0].ID != id {
			t.Fatalf("inexact person lookup %q: %+v %v", id, found, err)
		}
	}

	aiServer, usage := setupAIPortal(t)
	const subject = "user:subject+tag&foo=bar#anchor"
	limit := int64(10)
	for _, principal := range []string{subject, "user:subject"} {
		if err := usage.PutAIBudget(ctx, hex.AIBudget{Scope: hex.BudgetPerson, Subject: principal, LimitMicros: &limit}); err != nil {
			t.Fatal(err)
		}
	}
	admin := principalHeaders("admin", "admin-group")
	page = html.UnescapeString(requestAs(t, aiServer, admin, "GET", "/admin/ai", nil, 200).Body.String())
	month := time.Now().UTC().Format("2006-01")
	remove := "/api/hex/manage/ai/overrides?" + url.Values{"subject": {subject}, "month": {month}}.Encode()
	if !strings.Contains(page, `hx-delete="`+remove+`"`) {
		t.Fatal("AI override URL is not encoded")
	}
	requestAs(t, aiServer, admin, "DELETE", remove, nil, 200)
	budgets, err := usage.ListAIBudgets(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var prefixFound bool
	for _, budget := range budgets {
		if budget.Subject == subject {
			t.Fatal("opaque override survived")
		}
		if budget.Subject == "user:subject" {
			prefixFound = true
		}
	}
	if !prefixFound {
		t.Fatal("removal affected a different override")
	}
}

type signedPublisher struct{ *local.Store }

func (*signedPublisher) UploadTargets(_ context.Context, _ string, files []hex.SiteFile) ([]hex.UploadTarget, error) {
	targets := make([]hex.UploadTarget, 0, len(files))
	for _, file := range files {
		targets = append(targets, hex.UploadTarget{Path: file.Path, Protocol: "http", URL: "https://storage.test/" + file.Path,
			Headers: map[string]string{"Content-Type": "text/html"}})
	}
	return targets, nil
}

func TestSignedUploadNegotiationAndFallback(t *testing.T) {
	for _, negotiated := range []bool{false, true} {
		t.Run(map[bool]string{false: "legacy", true: "negotiated"}[negotiated], func(t *testing.T) {
			files, err := local.New(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := files.Close(); err != nil {
					t.Error(err)
				}
			})
			server := hex.New(hex.Config{Publisher: &signedPublisher{Store: files}})
			body := map[string]any{"files": manifest(map[string]string{"index.html": "hello"})}
			if negotiated {
				body["supportedUploadProtocols"] = []string{"hex", "http"}
			}
			response := requestAs(t, server, roleHeaders("owner"), "POST", "/api/hex/sites/demo/publish", encode(t, body), 200)
			var plan publishPlan
			if err := json.Unmarshal(response.Body.Bytes(), &plan); err != nil {
				t.Fatal(err)
			}
			if len(plan.Uploads) != 1 {
				t.Fatal(plan)
			}
			upload := plan.Uploads[0]
			if negotiated {
				if upload.Protocol != "http" || upload.Headers["Content-Type"] != "text/html" {
					t.Fatal(upload)
				}
				return
			}
			if upload.Protocol != "hex" || len(upload.Headers) != 0 {
				t.Fatal(upload)
			}
			requestAs(t, server, roleHeaders("owner"), "PUT", upload.URL, []byte("hello"), 204)
			requestAs(t, server, roleHeaders("owner"), "POST", "/api/hex/sites/demo/publish/complete", encode(t, body), 200)
		})
	}
}

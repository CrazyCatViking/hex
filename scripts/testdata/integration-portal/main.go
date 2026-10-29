// Loopback-only fixture for the real integration catalog and OAuth entry point.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	hex "github.com/crazycatviking/hex/server"
	"github.com/crazycatviking/hex/server/providers/memory"
	"golang.org/x/oauth2"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func feature(name, description string, write bool) hex.IntegrationEndpoint {
	return hex.IntegrationEndpoint{
		Name: name, Description: description, Write: write,
		InputSchema:  json.RawMessage(`{"type":"object","properties":{"query":{"type":"string","description":"Search query"}},"additionalProperties":false}`),
		OutputSchema: json.RawMessage(`{"type":"object","properties":{"items":{"type":"array","items":{"type":"string"}}},"required":["items"]}`),
		Handler: func(context.Context, hex.IntegrationCall, json.RawMessage) (any, error) {
			return nil, errors.New("browser discovery must not invoke integrations")
		},
	}
}

func run() error {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	registry := new(hex.IntegrationRegistry)
	for _, connector := range []hex.Connector{
		{Name: "microsoft", Title: "Microsoft"},
		{Name: "google", Title: "Google"},
	} {
		connector.OAuth2 = oauth2.Config{ClientID: "browser-fixture", Endpoint: oauth2.Endpoint{AuthURL: "https://oauth.example.test/authorize", TokenURL: "https://oauth.example.test/token"}}
		if err := registry.RegisterConnector(connector); err != nil {
			return err
		}
	}
	for _, integration := range []hex.Integration{
		{Name: "slack", Title: "Slack", Description: "Bring conversations, channel updates and notifications into your workflows.", Endpoints: []hex.IntegrationEndpoint{feature("channels", "Discover workspace channels.", false), feature("users", "Find people in your workspace.", false), feature("post-message", "Post a message to a channel.", true)}},
		{Name: "github", Title: "GitHub", Description: "Track repositories, review open issues and create work for your team.", Endpoints: []hex.IntegrationEndpoint{feature("repositories", "Discover repositories available to the platform.", false), feature("issues", "Read and filter issues in a repository.", false), feature("create-issue", "Open an issue with a title and description.", true)}},
		{Name: "azuredevops", Title: "Azure DevOps", Description: "Explore projects and work items using your own Azure DevOps account.", Connector: "microsoft", Endpoints: []hex.IntegrationEndpoint{feature("projects", "See projects in your linked account.", false), feature("work-items", "Read bugs, tasks and stories.", false), feature("update-work-item", "Change a work item's title or state.", true)}},
		{Name: "hubspot", Title: "HubSpot", Description: "Work with customer records, sales pipelines and deal activity.", RequiresApproval: true, Endpoints: []hex.IntegrationEndpoint{feature("contacts", "Read contacts.", false), feature("deals", "Read deals.", false), feature("update-deal", "Update a deal.", true)}},
		{Name: "sentry", Title: "Sentry", Description: "Understand application errors, spot trends and triage issues.", RequiresApproval: true, Audit: true, Endpoints: []hex.IntegrationEndpoint{feature("issues", "Read recent errors.", false), feature("issue-counts", "Summarize error totals.", false), feature("resolve-issue", "Resolve an issue.", true)}},
		{Name: "google-tasks", Title: "Google Tasks", Description: "Read and manage personal tasks with your linked Google account.", Connector: "google", Endpoints: []hex.IntegrationEndpoint{feature("lists", "Browse your personal task lists.", false), feature("tasks", "Read tasks from a list.", false), feature("create-task", "Add a personal task.", true)}},
	} {
		if err := registry.Register(integration); err != nil {
			return err
		}
	}
	store := memory.NewIntegrationStore()
	if err := store.PutCredential(ctx, hex.CredentialRecord{Owner: "owner", Connector: "google", Account: "Jamie <img src=x onerror=alert(1)>", Sealed: []byte("fixture-only"), ConnectedAt: time.Now(), LastUsedAt: time.Now()}); err != nil {
		return err
	}
	if err := store.PutIntegrationApproval(ctx, hex.IntegrationApproval{Site: "demo", Integration: "sentry", Status: hex.ApprovalRequested, Reason: "Daily error reporting"}); err != nil {
		return err
	}
	// Synthetic key and record are only for exercising discovery, never upstream calls.
	sealer, err := hex.NewKeySealer(make([]byte, 32))
	if err != nil {
		return err
	}
	access := memory.NewAccessStore()
	if err := access.PutSiteAccess(ctx, "demo", hex.SiteAccess{Owners: []string{"user:owner"}}); err != nil {
		return err
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	baseURL := fmt.Sprintf("http://localhost:%d", listener.Addr().(*net.TCPAddr).Port)
	platform := hex.New(hex.Config{
		Identity: hex.StaticIdentity{Identity: hex.Identity{ID: "owner", Name: "Jamie Demo", Groups: []string{"admins"}}}, AdminGroups: []string{"admins"},
		Access: access, SiteBaseURL: baseURL, IntegrationStore: store, CredentialSealer: sealer, Integrations: registry, IntegrationAudit: memory.NewIntegrationAuditStore(),
		IntegrationGrants: []hex.IntegrationGrant{{Principal: "user:owner", Permissions: []string{"slack.*", "github.repositories", "github.issues", "azuredevops.projects", "azuredevops.work-items", "sentry.*", "google-tasks.*"}}},
	})
	server := &http.Server{Handler: platform, ReadHeaderTimeout: 5 * time.Second}
	errorsCh := make(chan error, 1)
	go func() { errorsCh <- server.Serve(listener) }()
	fmt.Println(baseURL)
	select {
	case err := <-errorsCh:
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
	case <-ctx.Done():
		shutdown, stop := context.WithTimeout(context.Background(), 5*time.Second)
		defer stop()
		return server.Shutdown(shutdown)
	}
	return nil
}

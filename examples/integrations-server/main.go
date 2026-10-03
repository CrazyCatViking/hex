package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	hex "github.com/crazycatviking/hex/server"
	"github.com/crazycatviking/hex/server/dev"
	"github.com/crazycatviking/hex/server/providers/memory"
	"github.com/crazycatviking/hex/server/providers/postgres"
)

func main() {
	if err := run(); err != nil {
		slog.Error("integration platform stopped", "error", err)
		os.Exit(1)
	}
}

func run() error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	environment, err := dev.Open(ctx)
	if err != nil {
		return err
	}
	defer func() {
		if err := environment.Close(); err != nil {
			slog.Error("close providers", "error", err)
		}
	}()
	registry, err := exampleIntegrations()
	if err != nil {
		return err
	}
	var state hex.IntegrationStateStore = memory.NewIntegrationState()
	if database, ok := environment.Config.Database.(*postgres.Database); ok {
		state = database
	}
	runtime, err := hex.NewIntegrationRuntime(registry, state, hex.IntegrationBudget{}, []string{"user:local-dev", "role:integration-auditor"})
	if err != nil {
		return err
	}
	environment.Config.Integrations = runtime
	// Optional local demonstration only. Production uses the authenticated
	// gateway's actual access-token audience/scope claims, not static identity.
	if resource := os.Getenv("HEX_MCP_RESOURCE_URL"); resource != "" {
		metadata := &hex.IntegrationMCPConfig{ResourceURL: resource, Audience: "hex-example", RequiredScopes: []string{"tools.read"}, AuthorizationServers: []string{"https://identity.example.com"}}
		if err := metadata.Validate(); err != nil {
			return err
		}
		environment.Config.IntegrationMCP = metadata
		if identity, ok := environment.Config.Identity.(hex.StaticIdentity); ok {
			identity.Identity.Audiences = []string{"hex-example"}
			identity.Identity.Scopes = []string{"tools.read"}
			environment.Config.Identity = identity
		}
	}
	server := &http.Server{Addr: environment.Address, Handler: hex.New(environment.Config), ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 90 * time.Second}
	go func() {
		<-ctx.Done()
		timeout, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := server.Shutdown(timeout); err != nil {
			slog.Error("shutdown integration server", "error", err)
		}
	}()
	if err := server.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

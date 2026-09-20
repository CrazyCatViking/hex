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

	hex "github.com/hex-platform/hex/server"
	"github.com/hex-platform/hex/server/dev"
)

func main() {
	if err := run(); err != nil {
		slog.Error("server stopped", "error", err)
		os.Exit(1)
	}
}

func run() error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	config, closeProviders, err := configureServer(ctx)
	if err != nil {
		return err
	}
	defer closeProviders()

	server := &http.Server{
		Addr:              environmentValue(os.Getenv, "HEX_ADDR", "127.0.0.1:8080"),
		Handler:           hex.New(config),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       90 * time.Second,
	}
	go func() {
		<-ctx.Done()
		shutdownContext, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		if err := server.Shutdown(shutdownContext); err != nil {
			slog.Error("shut down HTTP server", "error", err)
		}
	}()

	slog.Info("Hex listening", "address", server.Addr)
	err = server.ListenAndServe()
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

func configureServer(ctx context.Context) (hex.Config, func(), error) {
	if os.Getenv("HEX_DEV") != "1" {
		return configure(ctx, os.Getenv)
	}

	environment, err := dev.Open(ctx)
	if err != nil {
		return hex.Config{}, nil, err
	}
	closeProviders := func() {
		if err := environment.Close(); err != nil {
			slog.Error("close development providers", "error", err)
		}
	}
	return environment.Config, closeProviders, nil
}

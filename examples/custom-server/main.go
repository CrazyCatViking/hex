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
		slog.Error("platform stopped", "error", err)
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
			slog.Error("close development providers", "error", err)
		}
	}()

	mux := http.NewServeMux()
	mux.Handle("/", hex.New(environment.Config))
	mux.HandleFunc("GET /api/platform", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if _, err := w.Write([]byte(`{"name":"custom-server"}`)); err != nil {
			slog.Error("write platform response", "error", err)
		}
	})

	server := &http.Server{
		Addr:              environment.Address,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       90 * time.Second,
	}
	serveErrors := make(chan error, 1)
	go func() {
		serveErrors <- server.ListenAndServe()
	}()

	select {
	case err := <-serveErrors:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return server.Shutdown(shutdown)
	}
}

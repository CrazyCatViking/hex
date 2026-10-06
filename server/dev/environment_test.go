package dev

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"strings"
	"testing"

	hex "github.com/crazycatviking/hex/server"
	"github.com/crazycatviking/hex/server/providers/postgres"
)

func localSettings(t *testing.T) {
	t.Helper()
	for name, value := range map[string]string{
		"HEX_ADDR":              "127.0.0.1:8081",
		"HEX_DEV_DATA_DIR":      t.TempDir(),
		"HEX_SITES_DIR":         "",
		"HEX_FILES_DIR":         "",
		"HEX_SITES_PROVIDER":    "filesystem",
		"HEX_FILES_PROVIDER":    "",
		"HEX_DATABASE_PROVIDER": "",
		"HEX_REALTIME_PROVIDER": "",
		"HEX_SITE_BASE_URL":     "http://localhost:8080",
	} {
		t.Setenv(name, value)
	}
}

func TestLocalDefaultsDoNotDependOnAModeFlag(t *testing.T) {
	for _, flag := range []string{"", "1", "production"} {
		t.Run("flag="+flag, func(t *testing.T) {
			localSettings(t)
			t.Setenv("HEX_DEV", flag)
			t.Setenv("HEX_ADDR", "")
			environment, err := Open(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := environment.Close(); err != nil {
					t.Error(err)
				}
			})
			if environment.Address != "127.0.0.1:8081" {
				t.Fatalf("unexpected default address: %s", environment.Address)
			}
			if environment.Config.Sites == nil || environment.Config.Files == nil || environment.Config.Database == nil || environment.Config.Realtime == nil {
				t.Fatal("local defaults were not configured")
			}
		})
	}
}

func TestCallerCanChooseTheListenerAddress(t *testing.T) {
	localSettings(t)
	t.Setenv("HEX_ADDR", "0.0.0.0:9000")
	environment, err := Open(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if environment.Address != "0.0.0.0:9000" {
		t.Fatalf("caller address was changed: %s", environment.Address)
	}
	if err := environment.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestFilesystemPersistsAndMemoryDatabaseResets(t *testing.T) {
	localSettings(t)
	t.Setenv("HEX_FILES_PROVIDER", "filesystem")
	ctx := context.Background()
	environment, err := Open(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := environment.Config.Files.Put(ctx, "demo/hello.txt", strings.NewReader("hello")); err != nil {
		t.Fatal(err)
	}
	if _, err := environment.Config.Database.Put(ctx, "demo", "notes", "one", json.RawMessage(`{"title":"temporary"}`), hex.WriteOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := environment.Close(); err != nil {
		t.Fatal(err)
	}

	restarted, err := Open(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := restarted.Close(); err != nil {
			t.Error(err)
		}
	})
	reader, err := restarted.Config.Files.Open(ctx, "demo/hello.txt")
	if err != nil {
		t.Fatal(err)
	}
	content, readError := io.ReadAll(reader)
	closeError := reader.Close()
	if readError != nil || closeError != nil || string(content) != "hello" {
		t.Fatalf("unexpected stored file: %s; read %v; close %v", content, readError, closeError)
	}
	documents, err := restarted.Config.Database.List(ctx, "demo", "notes", hex.ListOptions{Limit: 100})
	if err != nil || len(documents) != 0 {
		t.Fatalf("memory database should reset: %v, %v", documents, err)
	}
}

func TestDefaultLocalDataResetsOnRestart(t *testing.T) {
	localSettings(t)
	t.Setenv("DATABASE_URL", "ambient-connection-must-not-be-used")
	t.Setenv("AZURE_BLOB_CONNECTION_STRING", "ambient-connection-must-not-be-used")
	ctx := context.Background()
	environment, err := Open(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if environment.Config.Realtime == nil {
		t.Fatal("default realtime provider is missing")
	}
	if err := environment.Config.Files.Put(ctx, "demo/file", strings.NewReader("temporary")); err != nil {
		t.Fatal(err)
	}
	if _, err := environment.Config.Database.Put(ctx, "demo", "notes", "one", json.RawMessage(`{"value":1}`), hex.WriteOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := environment.Close(); err != nil {
		t.Fatal(err)
	}

	restarted, err := Open(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := restarted.Close(); err != nil {
			t.Error(err)
		}
	})
	if _, err := restarted.Config.Files.Open(ctx, "demo/file"); !errors.Is(err, hex.ErrNotFound) {
		t.Fatalf("default uploads must reset: %v", err)
	}
	if _, err := restarted.Config.Database.Get(ctx, "demo", "notes", "one"); !errors.Is(err, hex.ErrNotFound) {
		t.Fatalf("default documents must reset: %v", err)
	}
}

func TestDisabledServicesDoNotUseAmbientConnections(t *testing.T) {
	localSettings(t)
	for _, name := range []string{"HEX_SITES_PROVIDER", "HEX_FILES_PROVIDER", "HEX_DATABASE_PROVIDER", "HEX_REALTIME_PROVIDER"} {
		t.Setenv(name, "none")
	}
	t.Setenv("DATABASE_URL", "invalid-remote-connection")
	t.Setenv("AZURE_BLOB_CONNECTION_STRING", "invalid-remote-connection")
	environment, err := Open(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if environment.Config.Sites != nil || environment.Config.Files != nil || environment.Config.Database != nil || environment.Config.Realtime != nil {
		t.Fatal("disabled providers were enabled")
	}
	if err := environment.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestAnalyticsDatabaseBacksDurablePlatformStores(t *testing.T) {
	connection := os.Getenv("HEX_TEST_POSTGRES_URL")
	if connection == "" {
		t.Skip("set HEX_TEST_POSTGRES_URL")
	}
	localSettings(t)
	t.Setenv("HEX_DATABASE_PROVIDER", "memory")
	t.Setenv("HEX_ANALYTICS_PROVIDER", "postgres")
	t.Setenv("HEX_ANALYTICS_DATABASE_URL", connection)
	environment, err := Open(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := environment.Close(); err != nil {
			t.Error(err)
		}
	})
	durable, ok := environment.Config.Analytics.(*postgres.Database)
	if !ok {
		t.Fatal("durable analytics was not selected")
	}
	if environment.Config.IntegrationStore != durable || environment.Config.IntegrationAudit != durable || environment.Config.Automations != durable || environment.Config.AIUsage != durable || environment.Config.Access != durable || environment.Config.People != durable {
		t.Fatal("ephemeral documents must not force platform stores into memory")
	}
}

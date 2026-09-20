package main

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azidentity"
	hex "github.com/hex-platform/hex/server"
	"github.com/hex-platform/hex/server/providers/azureblob"
	"github.com/hex-platform/hex/server/providers/local"
	"github.com/hex-platform/hex/server/providers/memory"
	"github.com/hex-platform/hex/server/providers/postgres"
)

func configure(ctx context.Context, getenv func(string) string) (hex.Config, func(), error) {
	var config hex.Config
	var closers []func()
	closeProviders := func() {
		for i := len(closers) - 1; i >= 0; i-- {
			closers[i]()
		}
	}
	ok := false
	defer func() {
		if !ok {
			closeProviders()
		}
	}()
	value := func(key, fallback string) string {
		if v := getenv(key); v != "" {
			return v
		}
		return fallback
	}
	filesDefault, databaseDefault := "filesystem", "memory"
	if getenv("AZURE_BLOB_ENDPOINT") != "" {
		filesDefault = "azureblob"
	}
	if getenv("DATABASE_URL") != "" {
		databaseDefault = "postgres"
	}
	modes := map[string]string{
		"HEX_SITES_PROVIDER":    value("HEX_SITES_PROVIDER", "filesystem"),
		"HEX_FILES_PROVIDER":    value("HEX_FILES_PROVIDER", filesDefault),
		"HEX_DATABASE_PROVIDER": value("HEX_DATABASE_PROVIDER", databaseDefault),
		"HEX_REALTIME_PROVIDER": value("HEX_REALTIME_PROVIDER", "memory"),
	}
	allowed := map[string][]string{
		"HEX_SITES_PROVIDER":    {"none", "filesystem"},
		"HEX_FILES_PROVIDER":    {"none", "filesystem", "azureblob"},
		"HEX_DATABASE_PROVIDER": {"none", "memory", "postgres"},
		"HEX_REALTIME_PROVIDER": {"none", "memory"},
	}
	for key, mode := range modes {
		if !slices.Contains(allowed[key], mode) {
			return config, nil, fmt.Errorf("%s: unsupported provider %q", key, mode)
		}
	}
	if modes["HEX_FILES_PROVIDER"] == "azureblob" && getenv("AZURE_BLOB_ENDPOINT") == "" {
		return config, nil, fmt.Errorf("AZURE_BLOB_ENDPOINT is required for the azureblob files provider")
	}
	if modes["HEX_DATABASE_PROVIDER"] == "postgres" && getenv("DATABASE_URL") == "" {
		return config, nil, fmt.Errorf("DATABASE_URL is required for the postgres database provider")
	}
	filesystem := func(directory string) (*local.Store, error) {
		store, err := local.New(directory)
		if err == nil {
			closers = append(closers, func() { store.Close() })
		}
		return store, err
	}
	if modes["HEX_SITES_PROVIDER"] == "filesystem" {
		store, err := filesystem(value("HEX_SITES_DIR", ".hex-data/sites"))
		if err != nil {
			return config, nil, err
		}
		config.Sites = store
	}
	switch modes["HEX_FILES_PROVIDER"] {
	case "filesystem":
		store, err := filesystem(value("HEX_FILES_DIR", ".hex-data/files"))
		if err != nil {
			return config, nil, err
		}
		config.Files = store
	case "azureblob":
		credential, err := azidentity.NewDefaultAzureCredential(nil)
		if err != nil {
			return config, nil, err
		}
		store, err := azureblob.New(getenv("AZURE_BLOB_ENDPOINT"), value("AZURE_BLOB_CONTAINER", "uploads"), credential)
		if err != nil {
			return config, nil, err
		}
		config.Files = store
	}
	switch modes["HEX_DATABASE_PROVIDER"] {
	case "postgres":
		startup, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		db, err := postgres.New(startup, getenv("DATABASE_URL"))
		if err != nil {
			return config, nil, err
		}
		closers = append(closers, db.Close)
		if err := db.Migrate(startup); err != nil {
			return config, nil, err
		}
		config.Database = db
	case "memory":
		slog.Warn("using ephemeral in-memory database")
		config.Database = memory.NewDatabase()
	}
	if modes["HEX_REALTIME_PROVIDER"] == "memory" {
		config.Realtime = memory.NewRealtime()
	}
	ok = true
	return config, closeProviders, nil
}

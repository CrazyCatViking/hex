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

type providerSelection struct {
	sites    string
	files    string
	database string
	realtime string
}

func readProviderSelection(getenv func(string) string) (providerSelection, error) {
	filesDefault := "filesystem"
	if getenv("AZURE_BLOB_ENDPOINT") != "" {
		filesDefault = "azureblob"
	}

	databaseDefault := "memory"
	if getenv("DATABASE_URL") != "" {
		databaseDefault = "postgres"
	}

	selection := providerSelection{
		sites:    environmentValue(getenv, "HEX_SITES_PROVIDER", "filesystem"),
		files:    environmentValue(getenv, "HEX_FILES_PROVIDER", filesDefault),
		database: environmentValue(getenv, "HEX_DATABASE_PROVIDER", databaseDefault),
		realtime: environmentValue(getenv, "HEX_REALTIME_PROVIDER", "memory"),
	}
	settings := []struct {
		name    string
		value   string
		allowed []string
	}{
		{"HEX_SITES_PROVIDER", selection.sites, []string{"none", "filesystem"}},
		{"HEX_FILES_PROVIDER", selection.files, []string{"none", "filesystem", "azureblob"}},
		{"HEX_DATABASE_PROVIDER", selection.database, []string{"none", "memory", "postgres"}},
		{"HEX_REALTIME_PROVIDER", selection.realtime, []string{"none", "memory"}},
	}

	for _, setting := range settings {
		if !slices.Contains(setting.allowed, setting.value) {
			return selection, fmt.Errorf("%s: unsupported provider %q", setting.name, setting.value)
		}
	}

	if selection.files == "azureblob" && getenv("AZURE_BLOB_ENDPOINT") == "" {
		return selection, fmt.Errorf("AZURE_BLOB_ENDPOINT is required for the azureblob files provider")
	}
	if selection.database == "postgres" && getenv("DATABASE_URL") == "" {
		return selection, fmt.Errorf("DATABASE_URL is required for the postgres database provider")
	}

	return selection, nil
}

func configure(ctx context.Context, getenv func(string) string) (hex.Config, func(), error) {
	config := hex.Config{
		SiteBaseURL: environmentValue(getenv, "HEX_SITE_BASE_URL", "http://localhost:8080"),
	}
	selection, err := readProviderSelection(getenv)
	if err != nil {
		return config, nil, err
	}

	var closers []func()
	closeProviders := func() {
		for i := len(closers) - 1; i >= 0; i-- {
			closers[i]()
		}
	}
	configured := false
	defer func() {
		if !configured {
			closeProviders()
		}
	}()

	openFilesystem := func(directory string) (*local.Store, error) {
		store, err := local.New(directory)
		if err != nil {
			return nil, err
		}

		closers = append(closers, func() {
			if err := store.Close(); err != nil {
				slog.Error("close filesystem provider", "directory", directory, "error", err)
			}
		})
		return store, nil
	}

	if selection.sites == "filesystem" {
		directory := environmentValue(getenv, "HEX_SITES_DIR", ".hex-data/sites")
		store, err := openFilesystem(directory)
		if err != nil {
			return config, nil, fmt.Errorf("configure site storage: %w", err)
		}
		config.Sites = store
	}

	switch selection.files {
	case "filesystem":
		directory := environmentValue(getenv, "HEX_FILES_DIR", ".hex-data/files")
		store, err := openFilesystem(directory)
		if err != nil {
			return config, nil, fmt.Errorf("configure file storage: %w", err)
		}
		config.Files = store

	case "azureblob":
		store, err := openBlobStorage(getenv)
		if err != nil {
			return config, nil, fmt.Errorf("configure Blob Storage: %w", err)
		}
		config.Files = store
	}

	switch selection.database {
	case "postgres":
		database, err := openPostgres(ctx, getenv("DATABASE_URL"))
		if err != nil {
			return config, nil, fmt.Errorf("configure document database: %w", err)
		}
		closers = append(closers, database.Close)
		config.Database = database

	case "memory":
		slog.Warn("using ephemeral in-memory database")
		config.Database = memory.NewDatabase()
	}

	if selection.realtime == "memory" {
		config.Realtime = memory.NewRealtime()
	}

	configured = true
	return config, closeProviders, nil
}

func openBlobStorage(getenv func(string) string) (*azureblob.Store, error) {
	credential, err := azidentity.NewDefaultAzureCredential(nil)
	if err != nil {
		return nil, fmt.Errorf("create Azure credential: %w", err)
	}

	endpoint := getenv("AZURE_BLOB_ENDPOINT")
	container := environmentValue(getenv, "AZURE_BLOB_CONTAINER", "uploads")
	return azureblob.New(endpoint, container, credential)
}

func openPostgres(ctx context.Context, connectionString string) (*postgres.Database, error) {
	startupContext, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	database, err := postgres.New(startupContext, connectionString)
	if err != nil {
		return nil, err
	}

	if err := database.Migrate(startupContext); err != nil {
		database.Close()
		return nil, err
	}

	return database, nil
}

func environmentValue(getenv func(string) string, key, fallback string) string {
	if value := getenv(key); value != "" {
		return value
	}

	return fallback
}

package main

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azidentity"
	hex "github.com/crazycatviking/hex/server"
	"github.com/crazycatviking/hex/server/providers/azureblob"
	"github.com/crazycatviking/hex/server/providers/azurefiles"
	"github.com/crazycatviking/hex/server/providers/easyauth"
	"github.com/crazycatviking/hex/server/providers/local"
	"github.com/crazycatviking/hex/server/providers/memory"
	"github.com/crazycatviking/hex/server/providers/postgres"
)

type providerSelection struct {
	sites     string
	publisher string
	files     string
	database  string
	realtime  string
	identity  string
	analytics string
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

	sites := environmentValue(getenv, "HEX_SITES_PROVIDER", "filesystem")
	publisherDefault := "none"
	if getenv("AZURE_FILES_SHARE_URL") != "" {
		publisherDefault = "azurefiles"
	} else if sites == "filesystem" {
		publisherDefault = "filesystem"
	}

	selection := providerSelection{
		sites:     sites,
		publisher: environmentValue(getenv, "HEX_PUBLISHER_PROVIDER", publisherDefault),
		files:     environmentValue(getenv, "HEX_FILES_PROVIDER", filesDefault),
		database:  environmentValue(getenv, "HEX_DATABASE_PROVIDER", databaseDefault),
		realtime:  environmentValue(getenv, "HEX_REALTIME_PROVIDER", "memory"),
		identity:  environmentValue(getenv, "HEX_IDENTITY_PROVIDER", "none"),
	}
	analyticsDefault := selection.database
	if analyticsDefault != "postgres" && analyticsDefault != "none" {
		analyticsDefault = "memory"
	}
	selection.analytics = environmentValue(getenv, "HEX_ANALYTICS_PROVIDER", analyticsDefault)
	settings := []struct {
		name    string
		value   string
		allowed []string
	}{
		{"HEX_SITES_PROVIDER", selection.sites, []string{"none", "filesystem"}},
		{"HEX_PUBLISHER_PROVIDER", selection.publisher, []string{"none", "filesystem", "azurefiles"}},
		{"HEX_FILES_PROVIDER", selection.files, []string{"none", "memory", "filesystem", "azureblob"}},
		{"HEX_DATABASE_PROVIDER", selection.database, []string{"none", "memory", "postgres"}},
		{"HEX_REALTIME_PROVIDER", selection.realtime, []string{"none", "memory"}},
		{"HEX_IDENTITY_PROVIDER", selection.identity, []string{"none", "easyauth", "static"}},
		{"HEX_ANALYTICS_PROVIDER", selection.analytics, []string{"none", "memory", "postgres"}},
	}

	for _, setting := range settings {
		if !slices.Contains(setting.allowed, setting.value) {
			return selection, fmt.Errorf("%s: unsupported provider %q", setting.name, setting.value)
		}
	}

	if selection.publisher == "filesystem" && selection.sites != "filesystem" {
		return selection, fmt.Errorf("the filesystem publisher requires the filesystem sites provider")
	}
	if selection.publisher == "azurefiles" && getenv("AZURE_FILES_SHARE_URL") == "" {
		return selection, fmt.Errorf("AZURE_FILES_SHARE_URL is required for the azurefiles publisher")
	}
	if selection.files == "azureblob" && getenv("AZURE_BLOB_ENDPOINT") == "" {
		return selection, fmt.Errorf("AZURE_BLOB_ENDPOINT is required for the azureblob files provider")
	}
	if selection.database == "postgres" && getenv("DATABASE_URL") == "" {
		return selection, fmt.Errorf("DATABASE_URL is required for the postgres database provider")
	}
	if selection.analytics == "postgres" && environmentValue(getenv, "HEX_ANALYTICS_DATABASE_URL", getenv("DATABASE_URL")) == "" {
		return selection, fmt.Errorf("HEX_ANALYTICS_DATABASE_URL or DATABASE_URL is required for postgres analytics")
	}

	return selection, nil
}

func configure(ctx context.Context, getenv func(string) string) (hex.Config, func(), error) {
	config := hex.Config{
		SiteBaseURL:   environmentValue(getenv, "HEX_SITE_BASE_URL", "http://localhost:8080"),
		CLIReleaseURL: getenv("HEX_CLI_RELEASE_URL"),
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
		if selection.publisher == "filesystem" {
			config.Publisher = store
		}
	}
	if selection.publisher == "azurefiles" {
		publisher, err := openAzureFiles(getenv)
		if err != nil {
			return config, nil, fmt.Errorf("configure Azure Files publishing: %w", err)
		}
		config.Publisher = publisher
	}
	config.PublisherGroups = splitList(getenv("HEX_PUBLISHER_GROUPS"))

	switch selection.files {
	case "memory":
		config.Files = memory.NewStore()

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
	switch selection.analytics {
	case "memory":
		config.Analytics = memory.NewAnalytics()
		slog.Warn("using ephemeral in-memory analytics")
	case "postgres":
		connection := environmentValue(getenv, "HEX_ANALYTICS_DATABASE_URL", getenv("DATABASE_URL"))
		if database, ok := config.Database.(*postgres.Database); ok && connection == getenv("DATABASE_URL") {
			config.Analytics = database
		} else {
			database, err := openPostgres(ctx, connection)
			if err != nil {
				return config, nil, fmt.Errorf("configure analytics database: %w", err)
			}
			closers = append(closers, database.Close)
			config.Analytics = database
		}
	}
	if address := getenv("HEX_ANALYTICS_ADDR"); address != "" {
		collector, err := hex.StartTrafficCollector(ctx, address, config.Analytics)
		if err != nil {
			return config, nil, err
		}
		config.TrafficCollector = collector
		closers = append(closers, func() {
			if err := collector.Close(); err != nil {
				slog.Error("close traffic collector", "error", err)
			}
		})
	}

	configureIdentity(&config, selection, getenv)
	config.Groups = hex.ParseGroups(getenv("HEX_GROUPS"))

	if serverURL := getenv("HEX_PUBLIC_URL"); serverURL != "" {
		auth, err := hex.ParseAuthConfig(getenv("HEX_AUTH_CONFIG"))
		if err != nil {
			return config, nil, fmt.Errorf("HEX_AUTH_CONFIG: %w", err)
		}
		config.Connection = &hex.ConnectionConfig{
			Name:     environmentValue(getenv, "HEX_PLATFORM_NAME", "Hex"),
			Server:   serverURL,
			Resource: getenv("HEX_API_RESOURCE"),
			ClientID: getenv("HEX_CLI_CLIENT_ID"),
			TenantID: getenv("HEX_CLI_TENANT_ID"),
			Auth:     auth,
		}
	}

	configured = true
	return config, closeProviders, nil
}

// configureIdentity wires the identity resolver, admin groups and the access
// store. Access policies need durable storage, so site access control only
// activates alongside a PostgreSQL document or analytics provider. The static
// setup accepts the in-memory store for local experimentation.
func configureIdentity(config *hex.Config, selection providerSelection, getenv func(string) string) {
	switch selection.identity {
	case "none":
		return

	case "easyauth":
		config.Identity = easyauth.Resolver{}

	case "static":
		config.Identity = hex.StaticIdentity{Identity: hex.Identity{
			Provider: "static",
			ID:       environmentValue(getenv, "HEX_IDENTITY_ID", "local-dev"),
			Name:     environmentValue(getenv, "HEX_IDENTITY_NAME", "Local Developer"),
			Groups:   splitList(getenv("HEX_IDENTITY_GROUPS")),
		}}
	}
	config.AdminGroups = splitList(getenv("HEX_ADMIN_GROUPS"))
	// Keep existing policies in their original database when analytics has a
	// separate connection. Moving analytics must not move access control.
	if database, ok := config.Database.(*postgres.Database); ok {
		config.Access, config.People = database, database
		return
	}
	if database, ok := config.Analytics.(*postgres.Database); ok {
		config.Access, config.People = database, database
		return
	}

	switch config.Database.(type) {
	case *memory.Database:
		if selection.identity == "static" {
			config.Access = memory.NewAccessStore()
			config.People = memory.NewPeopleStore()
			slog.Warn("using ephemeral in-memory site access entries")
			return
		}
		slog.Warn("site access control disabled: the postgres database provider is required to store access entries")

	default:
		slog.Warn("site access control disabled: the postgres database provider is required to store access entries")
	}
}

func splitList(value string) []string {
	var values []string
	for _, entry := range strings.Split(value, ",") {
		if trimmed := strings.TrimSpace(entry); trimmed != "" {
			values = append(values, trimmed)
		}
	}
	return values
}

func openAzureFiles(getenv func(string) string) (*azurefiles.Publisher, error) {
	credential, err := azidentity.NewDefaultAzureCredential(nil)
	if err != nil {
		return nil, fmt.Errorf("create Azure credential: %w", err)
	}
	return azurefiles.New(getenv("AZURE_FILES_SHARE_URL"), credential)
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

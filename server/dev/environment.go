package dev

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	hex "github.com/crazycatviking/hex/server"
	"github.com/crazycatviking/hex/server/providers/azureblob"
	"github.com/crazycatviking/hex/server/providers/local"
	"github.com/crazycatviking/hex/server/providers/memory"
	"github.com/crazycatviking/hex/server/providers/postgres"
)

type Environment struct {
	Config  hex.Config
	Address string
	closers []func() error
}

func Open(ctx context.Context) (*Environment, error) {
	settings, err := readSettings()
	if err != nil {
		return nil, err
	}

	environment := &Environment{
		Address: settings.address,
		Config: hex.Config{
			SiteBaseURL:   value("HEX_SITE_BASE_URL", "http://localhost:8080"),
			CLIReleaseURL: os.Getenv("HEX_CLI_RELEASE_URL"),
		},
	}
	if err := environment.openProviders(ctx, settings); err != nil {
		return nil, errors.Join(err, environment.Close())
	}
	if err := environment.configureConnection(settings); err != nil {
		return nil, errors.Join(err, environment.Close())
	}

	return environment, nil
}

func (e *Environment) configureConnection(settings settings) error {
	serverURL := os.Getenv("HEX_PUBLIC_URL")
	if serverURL == "" {
		_, port, err := net.SplitHostPort(e.Address)
		if err != nil {
			return fmt.Errorf("derive local connection URL: %w", err)
		}
		serverURL = "http://localhost:" + port
	}
	e.Config.Connection = &hex.ConnectionConfig{
		Name:     value("HEX_PLATFORM_NAME", "Local Hex"),
		Server:   serverURL,
		Resource: os.Getenv("HEX_API_RESOURCE"),
		ClientID: os.Getenv("HEX_CLI_CLIENT_ID"),
		TenantID: os.Getenv("HEX_CLI_TENANT_ID"),
	}
	auth, err := hex.ParseAuthConfig(os.Getenv("HEX_AUTH_CONFIG"))
	if err != nil {
		return fmt.Errorf("HEX_AUTH_CONFIG: %w", err)
	}
	e.Config.Connection.Auth = auth
	return nil
}

func (e *Environment) Close() error {
	var failures []error
	for i := len(e.closers) - 1; i >= 0; i-- {
		if err := e.closers[i](); err != nil {
			failures = append(failures, err)
		}
	}
	e.closers = nil

	return errors.Join(failures...)
}

type settings struct {
	address   string
	dataDir   string
	sites     string
	files     string
	database  string
	realtime  string
	identity  string
	analytics string
}

func readSettings() (settings, error) {
	settings := settings{
		address:  value("HEX_ADDR", "127.0.0.1:8081"),
		dataDir:  value("HEX_DEV_DATA_DIR", ".hex-dev"),
		sites:    value("HEX_SITES_PROVIDER", "filesystem"),
		files:    value("HEX_FILES_PROVIDER", "memory"),
		database: value("HEX_DATABASE_PROVIDER", "memory"),
		realtime: value("HEX_REALTIME_PROVIDER", "memory"),
		identity: value("HEX_IDENTITY_PROVIDER", "static"),
	}
	analyticsDefault := "memory"
	if settings.database == "postgres" {
		analyticsDefault = "postgres"
	}
	settings.analytics = value("HEX_ANALYTICS_PROVIDER", analyticsDefault)
	selections := []struct {
		name    string
		value   string
		allowed []string
	}{
		{"HEX_SITES_PROVIDER", settings.sites, []string{"none", "filesystem"}},
		{"HEX_FILES_PROVIDER", settings.files, []string{"none", "memory", "filesystem", "azureblob"}},
		{"HEX_DATABASE_PROVIDER", settings.database, []string{"none", "memory", "postgres"}},
		{"HEX_REALTIME_PROVIDER", settings.realtime, []string{"none", "memory"}},
		{"HEX_IDENTITY_PROVIDER", settings.identity, []string{"none", "static"}},
		{"HEX_ANALYTICS_PROVIDER", settings.analytics, []string{"none", "memory", "postgres"}},
	}
	for _, selection := range selections {
		if !slices.Contains(selection.allowed, selection.value) {
			return settings, fmt.Errorf("%s: unsupported local provider %q", selection.name, selection.value)
		}
	}
	if settings.files == "azureblob" && os.Getenv("AZURE_BLOB_CONNECTION_STRING") == "" {
		return settings, errors.New("local Azure Blob storage requires AZURE_BLOB_CONNECTION_STRING")
	}
	if settings.database == "postgres" && os.Getenv("DATABASE_URL") == "" {
		return settings, errors.New("local PostgreSQL requires DATABASE_URL")
	}
	if settings.analytics == "postgres" && value("HEX_ANALYTICS_DATABASE_URL", os.Getenv("DATABASE_URL")) == "" {
		return settings, errors.New("local analytics requires HEX_ANALYTICS_DATABASE_URL or DATABASE_URL")
	}

	return settings, nil
}

func (e *Environment) openProviders(ctx context.Context, settings settings) error {
	if settings.sites == "filesystem" {
		directory := value("HEX_SITES_DIR", filepath.Join(settings.dataDir, "sites"))
		store, err := e.openFilesystem(directory)
		if err != nil {
			return fmt.Errorf("open local site directory: %w", err)
		}
		e.Config.Sites = store
		e.Config.Publisher = store
	}

	switch settings.files {
	case "memory":
		e.Config.Files = memory.NewStore()

	case "filesystem":
		directory := value("HEX_FILES_DIR", filepath.Join(settings.dataDir, "files"))
		store, err := e.openFilesystem(directory)
		if err != nil {
			return fmt.Errorf("open local file storage: %w", err)
		}
		e.Config.Files = store

	case "azureblob":
		store, err := azureblob.NewFromConnectionString(
			os.Getenv("AZURE_BLOB_CONNECTION_STRING"),
			value("AZURE_BLOB_CONTAINER", "uploads"),
		)
		if err != nil {
			return err
		}
		startup, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		if err := store.EnsureContainer(startup); err != nil {
			return fmt.Errorf("initialize local blob storage: %w", err)
		}
		e.Config.Files = store
	}

	switch settings.database {
	case "memory":
		e.Config.Database = memory.NewDatabase()

	case "postgres":
		startup, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		database, err := postgres.New(startup, os.Getenv("DATABASE_URL"))
		if err != nil {
			return fmt.Errorf("connect to local PostgreSQL: %w", err)
		}
		e.closers = append(e.closers, func() error {
			database.Close()
			return nil
		})
		if err := database.Migrate(startup); err != nil {
			return fmt.Errorf("initialize local PostgreSQL: %w", err)
		}
		e.Config.Database = database
	}

	if settings.realtime == "memory" {
		e.Config.Realtime = memory.NewRealtime()
	}

	switch settings.analytics {
	case "memory":
		e.Config.Analytics = memory.NewAnalytics()
	case "postgres":
		connection := value("HEX_ANALYTICS_DATABASE_URL", os.Getenv("DATABASE_URL"))
		if database, ok := e.Config.Database.(*postgres.Database); ok && connection == os.Getenv("DATABASE_URL") {
			e.Config.Analytics = database
		} else {
			startup, cancel := context.WithTimeout(ctx, 30*time.Second)
			defer cancel()
			database, err := postgres.New(startup, connection)
			if err != nil {
				return fmt.Errorf("open local analytics: %w", err)
			}
			e.closers = append(e.closers, func() error { database.Close(); return nil })
			if err := database.Migrate(startup); err != nil {
				return err
			}
			e.Config.Analytics = database
		}
	}
	if address := os.Getenv("HEX_ANALYTICS_ADDR"); address != "" {
		collector, err := hex.StartTrafficCollector(ctx, address, e.Config.Analytics)
		if err != nil {
			return err
		}
		e.Config.TrafficCollector = collector
		e.closers = append(e.closers, collector.Close)
	}
	e.configureIdentity(settings)
	return e.configurePlatformFeatures()
}

// configurePlatformFeatures prepares integrations, connected accounts and
// automations. The consuming server registers its integrations in
// Config.Integrations and may configure Config.AI. Locally every grant is
// open unless HEX_INTEGRATION_GRANTS says otherwise, and without
// HEX_CREDENTIAL_KEY connected accounts last until the server restarts.
func (e *Environment) configurePlatformFeatures() error {
	e.Config.Integrations = new(hex.IntegrationRegistry)
	grants, err := hex.ParseIntegrationGrants(value("HEX_INTEGRATION_GRANTS", `{"*":["*"]}`))
	if err != nil {
		return fmt.Errorf("HEX_INTEGRATION_GRANTS: %w", err)
	}
	e.Config.IntegrationGrants = grants

	if configured := os.Getenv("HEX_CREDENTIAL_KEY"); configured != "" {
		key, err := hex.ParseCredentialKey(configured)
		if err != nil {
			return fmt.Errorf("HEX_CREDENTIAL_KEY: %w", err)
		}
		e.Config.CredentialKey = key
	} else {
		key := make([]byte, 32)
		if _, err := rand.Read(key); err != nil {
			return fmt.Errorf("generate a local credential key: %w", err)
		}
		e.Config.CredentialKey = key
	}

	if database, ok := e.Config.Database.(*postgres.Database); ok {
		e.Config.IntegrationStore = database
		e.Config.Automations = database
		return nil
	}
	e.Config.IntegrationStore = memory.NewIntegrationStore()
	e.Config.Automations = memory.NewAutomationStore()
	return nil
}

// configureIdentity gives local development a fixed identity so apps can use
// the identity and access APIs without an authenticating gateway. The local
// developer administers access entries by default; entries live in the
// database provider (PostgreSQL) or an in-memory store otherwise.
func (e *Environment) configureIdentity(settings settings) {
	if settings.identity != "static" {
		return
	}

	identity := hex.Identity{
		Provider: "static",
		ID:       value("HEX_IDENTITY_ID", "local-dev"),
		Name:     value("HEX_IDENTITY_NAME", "Local Developer"),
		Groups:   splitList(os.Getenv("HEX_IDENTITY_GROUPS")),
	}
	e.Config.Identity = hex.StaticIdentity{Identity: identity}
	e.Config.AdminGroups = splitList(value("HEX_ADMIN_GROUPS", identity.ID))

	if database, ok := e.Config.Database.(*postgres.Database); ok {
		e.Config.Access = database
		e.Config.People = database
	} else if database, ok := e.Config.Analytics.(*postgres.Database); ok {
		e.Config.Access = database
		e.Config.People = database
	} else {
		e.Config.Access = memory.NewAccessStore()
		e.Config.People = memory.NewPeopleStore()
	}
	e.Config.Groups = hex.ParseGroups(os.Getenv("HEX_GROUPS"))
}

func splitList(list string) []string {
	var values []string
	for _, entry := range strings.Split(list, ",") {
		if trimmed := strings.TrimSpace(entry); trimmed != "" {
			values = append(values, trimmed)
		}
	}
	return values
}

func (e *Environment) openFilesystem(directory string) (*local.Store, error) {
	store, err := local.New(directory)
	if err != nil {
		return nil, err
	}
	e.closers = append(e.closers, store.Close)
	return store, nil
}

func value(name, fallback string) string {
	if configured := os.Getenv(name); configured != "" {
		return configured
	}
	return fallback
}

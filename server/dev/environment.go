package dev

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"slices"
	"time"

	hex "github.com/hex-platform/hex/server"
	"github.com/hex-platform/hex/server/providers/azureblob"
	"github.com/hex-platform/hex/server/providers/local"
	"github.com/hex-platform/hex/server/providers/memory"
	"github.com/hex-platform/hex/server/providers/postgres"
)

type Environment struct {
	Config  hex.Config
	Address string
	closers []func() error
}

func Open(ctx context.Context) (*Environment, error) {
	if os.Getenv("HEX_DEV") != "1" {
		return nil, errors.New("local providers require HEX_DEV=1; use hex dev to run locally")
	}

	settings, err := readSettings()
	if err != nil {
		return nil, err
	}

	environment := &Environment{
		Address: settings.address,
		Config: hex.Config{
			SiteBaseURL: value("HEX_SITE_BASE_URL", "http://localhost:8080"),
		},
	}
	if err := environment.openProviders(ctx, settings); err != nil {
		return nil, errors.Join(err, environment.Close())
	}

	return environment, nil
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
	address  string
	dataDir  string
	sites    string
	files    string
	database string
	realtime string
}

func readSettings() (settings, error) {
	settings := settings{
		address:  value("HEX_ADDR", "127.0.0.1:8081"),
		dataDir:  value("HEX_DEV_DATA_DIR", ".hex-dev"),
		sites:    value("HEX_SITES_PROVIDER", "filesystem"),
		files:    value("HEX_FILES_PROVIDER", "memory"),
		database: value("HEX_DATABASE_PROVIDER", "memory"),
		realtime: value("HEX_REALTIME_PROVIDER", "memory"),
	}
	host, _, err := net.SplitHostPort(settings.address)
	if err != nil || !net.ParseIP(host).IsLoopback() {
		return settings, errors.New("the unauthenticated development server must bind to a loopback IP")
	}

	selections := []struct {
		name    string
		value   string
		allowed []string
	}{
		{"HEX_SITES_PROVIDER", settings.sites, []string{"none", "filesystem"}},
		{"HEX_FILES_PROVIDER", settings.files, []string{"none", "memory", "filesystem", "azureblob"}},
		{"HEX_DATABASE_PROVIDER", settings.database, []string{"none", "memory", "postgres"}},
		{"HEX_REALTIME_PROVIDER", settings.realtime, []string{"none", "memory"}},
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
	return nil
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

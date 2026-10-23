package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"

	"github.com/gofrs/flock"
)

var stableCLIVersion = regexp.MustCompile(`^v?[0-9]+\.[0-9]+\.[0-9]+$`)

type automaticUpdateState struct {
	CheckedAt time.Time `json:"checkedAt"`
	Error     string    `json:"error,omitempty"`
}

func (a *App) refreshInstalledGuidance(ctx context.Context, executable string) error {
	command := exec.CommandContext(ctx, executable, "agents", "refresh")
	command.Stdout = io.Discard
	command.Stderr = a.Err
	if err := command.Run(); err != nil {
		return fmt.Errorf("refresh installed Hex guidance: %w", err)
	}
	return nil
}

// Automatic updates replace the executable on disk, never the running MCP
// process. That keeps existing calls and sessions intact until the next launch.
func (a *App) automaticUpdate(ctx context.Context, executable, version string) error {
	if os.Getenv("HEX_AUTO_UPDATE") == "0" || !stableCLIVersion.MatchString(version) {
		return nil
	}
	if _, err := cliArtifact(runtime.GOOS, runtime.GOARCH); err != nil {
		return nil // No distributed executable for this platform.
	}
	directory, err := profileDirectory()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(directory, 0700); err != nil {
		return err
	}
	lock := flock.New(filepath.Join(directory, "mcp-update.lock"))
	locked, err := lock.TryLock()
	if err != nil {
		return err
	}
	if !locked {
		return nil
	}
	defer func() {
		if err := lock.Unlock(); err != nil {
			fmt.Fprintf(a.Err, "Release automatic-update lock: %v\n", err)
		}
	}()
	path := filepath.Join(directory, "mcp-update.json")
	data, err := readOptionalFile(path)
	if err != nil {
		return err
	}
	var state automaticUpdateState
	if len(data) > 0 {
		if err := json.Unmarshal(data, &state); err != nil {
			return fmt.Errorf("read automatic-update state: %w", err)
		}
	}
	interval := 24 * time.Hour
	if state.Error != "" {
		interval = time.Hour
	}
	if elapsed := time.Since(state.CheckedAt); elapsed >= 0 && elapsed < interval {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	// A separate App routes every updater message to stderr, not MCP stdout.
	updater, err := New(strings.NewReader(""), a.Err, a.Err)
	if err != nil {
		return err
	}
	updater.HTTP = a.HTTP
	address := os.Getenv("HEX_CLI_RELEASE_URL")
	if address == "" {
		connection, _, err := loadProfile("", true)
		if err != nil {
			return err
		}
		if connection != nil {
			address = connection.CLIReleaseURL
		}
	}
	if address == "" {
		address = defaultCLIReleaseURL
	}
	updateError := updater.updateExecutable(ctx, executable, address)
	if updateError == nil {
		updateError = updater.refreshInstalledGuidance(ctx, executable)
	}
	if ctx.Err() != nil {
		return updateError // An interrupted attempt can retry on the next launch.
	}
	state = automaticUpdateState{CheckedAt: time.Now()}
	if updateError != nil {
		state.Error = updateError.Error()
	}
	return errors.Join(updateError, writeJSONFile(path, state))
}

func (a *App) startAutomaticUpdate(ctx context.Context, version string) func() {
	ctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		executable, err := os.Executable()
		if err == nil {
			err = a.automaticUpdate(ctx, executable, version)
		}
		if err != nil && ctx.Err() == nil {
			fmt.Fprintf(a.Err, "Automatic Hex update deferred; continuing with the installed version: %v\n", err)
		}
	}()
	return func() {
		cancel()
		<-done
	}
}

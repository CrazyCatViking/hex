package cli

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

const defaultCLIReleaseURL = "https://github.com/crazycatviking/hex/releases/latest/download"

func cliArtifact(osName, architecture string) (string, error) {
	switch osName + "-" + architecture {
	case "linux-amd64", "darwin-amd64", "darwin-arm64":
		return "hex-" + osName + "-" + architecture, nil
	case "windows-amd64":
		return "hex-windows-amd64.exe", nil
	default:
		return "", fmt.Errorf("no prebuilt Hex CLI for %s/%s", osName, architecture)
	}
}

func releaseChecksum(data []byte, artifact string) (string, error) {
	var checksum string
	scanner := bufio.NewScanner(bytes.NewReader(data))
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) != 2 || strings.TrimPrefix(fields[1], "*") != artifact {
			continue
		}
		decoded, err := hex.DecodeString(fields[0])
		if err != nil || len(decoded) != sha256.Size || checksum != "" {
			return "", fmt.Errorf("release has an invalid or duplicate checksum for %s", artifact)
		}
		checksum = strings.ToLower(fields[0])
	}
	if err := scanner.Err(); err != nil {
		return "", err
	}
	if checksum == "" {
		return "", fmt.Errorf("release has no checksum for %s", artifact)
	}
	return checksum, nil
}

func binaryChecksum(path string) (checksum string, result error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer func() { result = errors.Join(result, file.Close()) }()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func validateReleaseDirectory(value string) error {
	if err := validateDownloadURL(value); err != nil {
		return err
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.RawQuery != "" {
		return errors.New("CLI release directory must not contain query parameters")
	}
	return nil
}

func (a *App) updateCommand() *cobra.Command {
	var releaseURL string
	command := &cobra.Command{
		Use:   "update",
		Short: "Install the latest checksum-verified prebuilt Hex CLI",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if releaseURL == "" {
				releaseURL = os.Getenv("HEX_CLI_RELEASE_URL")
			}
			if releaseURL == "" {
				connection, _, err := loadProfile("", true)
				if err != nil {
					return err
				}
				if connection != nil {
					releaseURL = connection.CLIReleaseURL
				}
			}
			if releaseURL == "" {
				releaseURL = defaultCLIReleaseURL
			}
			executable, err := os.Executable()
			if err != nil {
				return fmt.Errorf("locate the installed CLI: %w", err)
			}
			return a.updateExecutable(cmd.Context(), executable, releaseURL)
		},
	}
	command.Flags().StringVar(&releaseURL, "release-url", "", "Override the CLI release download directory")
	return command
}

func (a *App) updateExecutable(ctx context.Context, executable, releaseURL string) error {
	if err := validateReleaseDirectory(releaseURL); err != nil {
		return err
	}
	artifact, err := cliArtifact(runtime.GOOS, runtime.GOARCH)
	if err != nil {
		return err
	}
	executable, err = filepath.EvalSymlinks(executable)
	if err != nil {
		return fmt.Errorf("resolve the installed CLI: %w", err)
	}
	info, err := os.Stat(executable)
	if err != nil {
		return fmt.Errorf("inspect installed CLI: %w", err)
	}
	if !info.Mode().IsRegular() {
		return errors.New("the installed CLI must be a regular executable file")
	}
	releaseURL = strings.TrimRight(releaseURL, "/")
	fmt.Fprintln(a.Err, "Checking for the latest Hex CLI...")
	manifest, err := a.downloadBytes(ctx, releaseURL+"/SHA256SUMS", 64<<10)
	if err != nil {
		return err
	}
	expected, err := releaseChecksum(manifest, artifact)
	if err != nil {
		return err
	}
	current, err := binaryChecksum(executable)
	if err != nil {
		return fmt.Errorf("read installed CLI: %w", err)
	}
	if current == expected {
		fmt.Fprintln(a.Out, "Hex is already up to date.")
		return nil
	}
	temporary, err := os.MkdirTemp(filepath.Dir(executable), ".hex-update-*")
	if err != nil {
		return fmt.Errorf("cannot update %s; its installation directory must be writable by your user: %w", executable, err)
	}
	defer a.removeTemporary(temporary)
	downloaded := filepath.Join(temporary, artifact)
	if err := a.downloadBinary(ctx, releaseURL+"/"+artifact, downloaded, expected); err != nil {
		return err
	}
	if err := os.Chmod(downloaded, info.Mode().Perm()); err != nil {
		return err
	}
	checkContext, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	output, err := exec.CommandContext(checkContext, downloaded, "--version").CombinedOutput()
	if err != nil {
		return fmt.Errorf("verify downloaded CLI before replacing the installation: %w", err)
	}
	version := strings.TrimSpace(string(output))
	if !strings.HasPrefix(version, "hex version ") {
		return errors.New("downloaded executable did not identify itself as the Hex CLI")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if runtime.GOOS == "windows" {
		if err := a.replaceWindowsExecutable(downloaded, executable); err != nil {
			return err
		}
	} else if err := os.Rename(downloaded, executable); err != nil {
		return fmt.Errorf("replace installed CLI: %w", err)
	}
	fmt.Fprintf(a.Out, "Updated Hex to %s. Platform profiles and project files are unchanged.\n", strings.TrimPrefix(version, "hex version "))
	return nil
}

func (a *App) replaceWindowsExecutable(downloaded, executable string) error {
	previous := executable + ".previous"
	if err := os.Remove(previous); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove previous CLI update; close processes still using the previous Hex version and retry: %w", err)
	}
	if err := os.Rename(executable, previous); err != nil {
		return fmt.Errorf("move the previous CLI aside: %w", err)
	}
	if err := os.Rename(downloaded, executable); err != nil {
		rollbackError := os.Rename(previous, executable)
		return fmt.Errorf("replace installed CLI: %w", errors.Join(err, rollbackError))
	}
	if err := os.Remove(previous); err != nil && !os.IsNotExist(err) {
		fmt.Fprintln(a.Err, "The running previous executable is retained until the next update:", previous)
	}
	return nil
}

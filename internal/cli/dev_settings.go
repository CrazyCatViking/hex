package cli

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/hashicorp/go-envparse"
	"github.com/spf13/cobra"
)

type devSettings struct {
	Package         string            `json:"package"`
	Binary          string            `json:"binary"`
	Args            []string          `json:"args"`
	DataDirectory   string            `json:"dataDirectory"`
	EnvFile         string            `json:"envFile"`
	Services        []string          `json:"services"`
	Port            int               `json:"port"`
	APIPort         int               `json:"apiPort"`
	PostgresPort    int               `json:"postgresPort"`
	BlobPort        int               `json:"blobPort"`
	ServerDirectory string            `json:"-"`
	Environment     map[string]string `json:"-"`
}

func (a *App) devCommand() *cobra.Command {
	var flags devSettings
	var configFile string
	var stopServices bool
	command := &cobra.Command{
		Use:   "dev [server-directory]",
		Short: "Run your server and NGINX locally",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			directory := a.Dir
			if len(args) > 0 {
				directory = resolvePath(a.Dir, args[0])
			}
			settings, err := readDevSettings(directory, configFile, flags, cmd.Flags().Changed, stopServices)
			if err != nil {
				return err
			}
			if stopServices {
				temporary, err := os.MkdirTemp("", "hex-services-*")
				if err != nil {
					return err
				}
				defer a.removeTemporary(temporary)
				if err := a.manageServices(cmd.Context(), settings, temporary, "down"); err != nil {
					return err
				}
				fmt.Fprintln(a.Out, "Local service containers stopped; persistent volumes retained.")
				return nil
			}
			err = a.runDevelopment(cmd.Context(), settings)
			if errors.Is(err, context.Canceled) {
				return nil
			}
			return err
		},
	}
	f := command.Flags()
	f.StringVar(&configFile, "config", "", "Development settings file (default hex.dev.json)")
	f.StringVar(&flags.Package, "package", "", "Go package to build (default .)")
	f.StringVar(&flags.Binary, "binary", "", "Run a prebuilt executable without Go installed")
	f.StringArrayVar(&flags.Args, "arg", nil, "Server argument; may be repeated")
	f.StringVar(&flags.EnvFile, "env-file", "", "Server dotenv file")
	f.StringVar(&flags.DataDirectory, "data-dir", "", "Persistent local data directory")
	f.IntVar(&flags.Port, "port", 8080, "Gateway port")
	f.IntVar(&flags.APIPort, "api-port", 8081, "API port")
	f.IntVar(&flags.PostgresPort, "postgres-port", 54320, "Optional PostgreSQL port")
	f.IntVar(&flags.BlobPort, "blob-port", 10000, "Optional Azurite port")
	f.StringSliceVar(&flags.Services, "services", nil, "Optional postgres,azurite services; default none")
	f.BoolVar(&stopServices, "stop-services", false, "Stop service containers without removing volumes")
	return command
}

func readDevSettings(directory, configFile string, flags devSettings, changed func(string) bool, stopping bool) (devSettings, error) {
	settings := devSettings{
		Port:          8080,
		APIPort:       8081,
		PostgresPort:  54320,
		BlobPort:      10000,
		DataDirectory: ".hex-dev",
	}
	root, err := filepath.EvalSymlinks(directory)
	if err != nil {
		return settings, err
	}
	filename := configFile
	if filename == "" {
		filename = "hex.dev.json"
	}
	data, err := os.ReadFile(resolvePath(root, filename))
	if err != nil && !(configFile == "" && errors.Is(err, fs.ErrNotExist)) {
		return settings, fmt.Errorf("read development configuration: %w", err)
	}
	if err == nil {
		if strings.TrimSpace(string(data)) == "null" {
			return settings, errors.New("development configuration must be an object")
		}
		if err := json.Unmarshal(data, &settings); err != nil {
			return settings, fmt.Errorf("parse development configuration: %w", err)
		}
	}

	if changed("package") {
		settings.Package = flags.Package
	}
	if changed("binary") {
		settings.Binary = flags.Binary
	}
	if changed("arg") {
		settings.Args = flags.Args
	}
	if changed("data-dir") {
		settings.DataDirectory = flags.DataDirectory
	}
	if changed("env-file") {
		settings.EnvFile = flags.EnvFile
	}
	if changed("services") {
		settings.Services = flags.Services
	}
	if changed("port") {
		settings.Port = flags.Port
	}
	if changed("api-port") {
		settings.APIPort = flags.APIPort
	}
	if changed("postgres-port") {
		settings.PostgresPort = flags.PostgresPort
	}
	if changed("blob-port") {
		settings.BlobPort = flags.BlobPort
	}

	if settings.Binary != "" && settings.Package != "" {
		return settings, errors.New("choose a Go package or a prebuilt binary, not both")
	}
	if settings.Package == "" {
		settings.Package = "."
	}
	if strings.HasPrefix(settings.Package, "-") {
		return settings, errors.New("package must be a Go package path")
	}
	if slices.Equal(settings.Services, []string{"none"}) {
		settings.Services = nil
	}
	for _, service := range settings.Services {
		if service != "postgres" && service != "azurite" {
			return settings, errors.New("supported services are postgres and azurite, or none")
		}
	}
	ports := []int{settings.Port, settings.APIPort}
	if slices.Contains(settings.Services, "postgres") {
		ports = append(ports, settings.PostgresPort)
	}
	if slices.Contains(settings.Services, "azurite") {
		ports = append(ports, settings.BlobPort)
	}
	seen := make(map[int]bool)
	for _, port := range ports {
		if port < 1024 || port > 65535 {
			return settings, errors.New("ports must be between 1024 and 65535")
		}
		if seen[port] {
			return settings, errors.New("gateway, API and enabled service ports must be distinct")
		}
		seen[port] = true
	}
	settings.ServerDirectory = root
	settings.DataDirectory = resolvePath(root, settings.DataDirectory)
	if settings.Binary != "" {
		settings.Binary, err = filepath.EvalSymlinks(resolvePath(root, settings.Binary))
		if err != nil {
			return settings, err
		}
	}
	settings.Environment = make(map[string]string)
	if settings.EnvFile != "" && !stopping {
		file, err := os.Open(resolvePath(root, settings.EnvFile))
		if err != nil {
			return settings, fmt.Errorf("read environment file: %w", err)
		}
		settings.Environment, err = envparse.Parse(file)
		if err := errors.Join(err, file.Close()); err != nil {
			return settings, fmt.Errorf("parse environment file: %w", err)
		}
	}
	return settings, nil
}

func environmentMap() map[string]string {
	environment := make(map[string]string)
	for _, entry := range os.Environ() {
		name, value, _ := strings.Cut(entry, "=")
		environment[name] = value
	}
	return environment
}

func environmentList(environment map[string]string) []string {
	values := make([]string, 0, len(environment))
	for key, value := range environment {
		values = append(values, key+"="+value)
	}
	slices.Sort(values)
	return values
}

func serviceEnvironment(settings devSettings) map[string]string {
	environment := make(map[string]string)
	if slices.Contains(settings.Services, "postgres") {
		environment["HEX_DATABASE_PROVIDER"] = "postgres"
		environment["DATABASE_URL"] = fmt.Sprintf("postgres://hex:hex-local-only@127.0.0.1:%d/hex?sslmode=disable", settings.PostgresPort)
	}
	if slices.Contains(settings.Services, "azurite") {
		environment["HEX_FILES_PROVIDER"] = "azureblob"
		environment["AZURE_BLOB_CONTAINER"] = "uploads"
		environment["AZURE_BLOB_CONNECTION_STRING"] = fmt.Sprintf("DefaultEndpointsProtocol=http;AccountName=hexlocal;AccountKey=%s;BlobEndpoint=http://127.0.0.1:%d/hexlocal", localBlobKey(), settings.BlobPort)
	}
	return environment
}

func localBlobKey() string {
	return base64.StdEncoding.EncodeToString([]byte("hex-local-development-only"))
}

func serverEnvironment(settings devSettings) []string {
	environment := environmentMap()
	defaults := map[string]string{
		"DATABASE_URL":                 "",
		"AZURE_BLOB_ENDPOINT":          "",
		"AZURE_BLOB_CONNECTION_STRING": "",
		"HEX_SITES_PROVIDER":           "filesystem",
		"HEX_FILES_PROVIDER":           "memory",
		"HEX_DATABASE_PROVIDER":        "memory",
		"HEX_REALTIME_PROVIDER":        "memory",
	}
	for key, value := range defaults {
		environment[key] = value
	}
	for key, value := range settings.Environment {
		environment[key] = value
	}
	for key, value := range serviceEnvironment(settings) {
		environment[key] = value
	}
	environment["HEX_ADDR"] = fmt.Sprintf("127.0.0.1:%d", settings.APIPort)
	environment["HEX_DEV_DATA_DIR"] = settings.DataDirectory
	environment["HEX_SITES_DIR"] = filepath.Join(settings.DataDirectory, "sites")
	environment["HEX_FILES_DIR"] = filepath.Join(settings.DataDirectory, "files")
	environment["HEX_SITE_BASE_URL"] = fmt.Sprintf("http://localhost:%d", settings.Port)
	environment["HEX_PUBLIC_URL"] = environment["HEX_SITE_BASE_URL"]
	return environmentList(environment)
}

func composeArguments(settings devSettings, file, action string) []string {
	digest := sha256.Sum256([]byte(settings.DataDirectory))
	project := "hexdev-" + hex.EncodeToString(digest[:6])
	args := []string{"compose", "--file", file, "--project-name", project, "--profile", "postgres", "--profile", "azurite"}
	if action == "up" {
		args = append(args, "up", "--detach", "--wait", "--wait-timeout", "120")
		return append(args, settings.Services...)
	}
	return append(args, "down")
}

func composeEnvironment(settings devSettings) []string {
	environment := environmentMap()
	environment["HEX_LOCAL_POSTGRES_PORT"] = strconv.Itoa(settings.PostgresPort)
	environment["HEX_LOCAL_BLOB_PORT"] = strconv.Itoa(settings.BlobPort)
	environment["HEX_LOCAL_BLOB_KEY"] = localBlobKey()
	return environmentList(environment)
}

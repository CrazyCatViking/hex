# Develop your own Hex server locally

`hex dev` runs a server from **your repository**, not Hex's bundled reference executable. Your Go application imports Hex, chooses its providers, and adds its own routes. The CLI supplies a local hosting environment: native NGINX, process supervision, configuration, and optional Docker Compose services. No Aspire, .NET, Azure subscription or cloud credentials are required.

## Responsibilities

| Component | Responsibility |
| --- | --- |
| Your server | Creates the Hex handler, adds application behavior, and listens on `HEX_ADDR` |
| `server/dev` | Optional Go adapter that constructs local providers from environment variables |
| `hex dev` | Builds/runs your executable, starts NGINX, supplies settings and stops owned processes |
| Docker Compose | Optional PostgreSQL and Azurite containers with persistent volumes and health checks |
| `hex publish` | Synchronizes directly into the local site directory; does not call the API |

The development adapter neither starts a server nor provisions cloud infrastructure. Production continues to use your chosen providers and your infrastructure code. The core Hex HTTP handler does not depend on the local tooling.

## 1. Add Hex to your server

Install the Hex executable and native NGINX. Go 1.25+ is needed only when building your server or the CLI from source; `hex dev --binary` needs no Go installation. Node.js is not required by the CLI. Set `NGINX_BIN` if NGINX is not on PATH; set `NGINX_MIME_TYPES` if its MIME type file is in a nonstandard location. The local NGINX runner is tested on Linux; the CLI can also be built for macOS and Windows, but native NGINX behavior there needs platform validation (WSL is an option on Windows).

For development from a checkout, install the CLI from the Hex repository and ensure your Go bin directory is on PATH:

```sh
go install ./cmd/hex
```

Create your own Go module in a separate directory. Until Hex is published as a versioned module, use a local replacement (adjust the path):

```sh
go mod init example.com/my-hex-platform
go mod edit -require=github.com/crazycatviking/hex@v0.0.0
go mod edit -replace=github.com/crazycatviking/hex=../hex
```

A Go workspace is another option. Only your development checkout needs the replacement/workspace; a deployed server can use a versioned dependency.

Choose the convenience local configuration directly in your server:

```go
environment, err := dev.Open(ctx)
if err != nil {
    return err
}
defer func() {
    if err := environment.Close(); err != nil {
        slog.Error("close local providers", "error", err)
    }
}()

mux := http.NewServeMux()
mux.Handle("/", hex.New(environment.Config))
```

The imports are `github.com/crazycatviking/hex/server` (aliased to `hex`) and `github.com/crazycatviking/hex/server/dev`. They are packages in the same root Go module, not separate repositories. Listen on `environment.Address`. Calling `dev.Open` selects local defaults; it does not inspect a development-mode flag or detect how the application was launched. Environment variables can override its defaults but are not required. The address defaults to `127.0.0.1:8081`; choosing a different listener is the application's responsibility.

Copy [the complete custom-server example](../examples/custom-server/main.go) into your own repository, then run `go mod tidy`. It defines `/api/platform` to demonstrate that the executable is yours. This example explicitly chooses local defaults. For deployment, your application chooses hosted providers instead; Hex does not switch configurations based on a mode flag.

You may construct local providers yourself instead of importing `server/dev`; consume the environment contract below. The CLI only expects the resulting HTTP handler to expose `/api/hex/capabilities` for readiness.

## 2. Run it

You can run the Go server normally from your repository, with no environment flag:

```sh
go run .
```

Its APIs are available on `http://127.0.0.1:8081`. This starts only your executable, not NGINX. The same applies when launching from an IDE or running the compiled binary.

For the full local hosting arrangement, including NGINX and site subdomains, optionally use:

```sh
hex dev
```

For a different entry package, prebuilt executable, or another repository:

```sh
hex dev --package ./cmd/platform
hex dev --binary ./bin/platform
hex dev ../my-hex-platform --package ./cmd/platform
```

Use `--arg=value` for executable arguments, including arguments beginning with a dash. A server is built once per launch; automatic code watching/rebuilding is not included. Restart `hex dev` after Go changes.

The default layout is:

```text
http://localhost:8080/api/* → NGINX → your server on 127.0.0.1:8081
http://demo.localhost:8080/ → NGINX → .hex-dev/sites/public/sites/demo/
```

Both listeners are loopback-only. Local requests are unauthenticated. Ctrl+C stops NGINX and the server, and removes temporary build/config files. The server's working directory is your repository; persistent local files stay in `.hex-dev/` by default. Add `.hex-dev/` and your local environment files to that repository's `.gitignore`.

## 3. Publish a test app

The local helper exposes its connection settings automatically. From an app workspace:

```sh
hex setup http://localhost:8080 --name local
hex init demo
```

Run `hex publish` in the generated app directory and open `http://demo.localhost:8080/`. For custom gateway ports, give setup the matching URL. The launcher also prints the absolute publishing root for manual `hex init --server ... --publish-root ...` configuration. App code uses same-origin API requests. Modern browsers resolve `.localhost` subdomains to loopback; configure local DNS/hosts entries if your environment does not.

## 4. Choose your local services

### Lightweight mode

```sh
hex dev --services none
```

This is the default. It does not invoke Docker:

- Site discovery and NGINX use a shared local directory.
- Uploaded app files use an in-memory object store that resets on restart.
- Documents use an in-memory database that resets on restart.
- Realtime uses the same in-process broker as the initial production example.

Ordinary development needs no PostgreSQL, Azurite or other service containers. Published website assets remain on disk because the CLI and NGINX access them directly, independently of the API process. They survive server restarts. Database records and application uploads do not.

To persist application uploads locally without adding a service, explicitly set `HEX_FILES_PROVIDER=filesystem` in the development environment file. This is optional; the default is `memory`.

### Optional provider integration testing

Use service-backed mode only when you specifically want to test PostgreSQL or Azure Blob behavior. It is not required for app development.

Install Docker with Compose v2 and start the Docker engine:

```sh
hex dev --services postgres,azurite
```

Or select just one service:

```sh
hex dev --services postgres
hex dev --services azurite
```

The CLI uses the shipped Compose definition. PostgreSQL uses the same Go provider as a hosted PostgreSQL service. Azurite exercises the real Azure Blob SDK/provider through a connection string. The Go development adapter initializes the database table and upload container. Native or externally managed local services can instead be supplied through an environment file, without Docker.

Service ports default to PostgreSQL **54320** and Azurite Blob **10000**, published only on loopback. Override them with `--postgres-port` and `--blob-port`. Each data-directory path determines a separate Compose project name; use different host ports when running multiple platforms concurrently.

Containers and their named volumes remain after `hex dev` exits, avoiding service startup on every server restart. Stop/remove the containers explicitly with the same directory/configuration:

```sh
hex dev --stop-services
```

This retains volumes. The CLI does not automatically delete service data. Development database/storage credentials are deliberately local-only and must not be used in a remote deployment.

## Project configuration

Put optional defaults in `hex.dev.json` in your server repository:

```json
{
  "package": "./cmd/platform",
  "services": [],
  "envFile": ".env.local",
  "dataDirectory": ".hex-dev",
  "port": 8080,
  "apiPort": 8081,
  "postgresPort": 54320,
  "blobPort": 10000
}
```

Flags override corresponding configuration values. Paths are resolved from the consuming server repository. `--config` selects another JSON configuration file. Choose either `package` or `binary`; do not configure both.

The environment file uses ordinary dotenv syntax, with no variable expansion. For example:

```dotenv
HEX_REALTIME_PROVIDER=none
CUSTOM_INTEGRATION_MODE=stub
```

For an existing local PostgreSQL service:

```dotenv
HEX_DATABASE_PROVIDER=postgres
DATABASE_URL=postgres://user:password@127.0.0.1:5432/hex?sslmode=disable
```

For an existing Azurite service, select `HEX_FILES_PROVIDER=azureblob` and provide `AZURE_BLOB_CONNECTION_STRING`; `AZURE_BLOB_CONTAINER` defaults to `uploads`. No Azure token credential is used by this local adapter.

## Environment contract

The CLI preserves ordinary inherited environment variables but replaces ambient Hex provider choices and storage connection strings with lightweight local defaults. Explicit environment-file values override those defaults. Selected Compose services then supply their provider/connection settings. Finally, launcher-owned ports and paths are set so NGINX and your executable agree. The CLI neither sets nor requires a development-mode flag.

| Variable | Meaning |
| --- | --- |
| `HEX_ADDR` | Loopback API listener selected by `--api-port` |
| `HEX_DEV_DATA_DIR` | Persistent development data directory |
| `HEX_SITES_DIR` | Site filesystem root, shared with NGINX |
| `HEX_FILES_DIR` | Uploaded-file root when explicitly selecting the filesystem provider |
| `HEX_SITE_BASE_URL` | `http://localhost:<gateway-port>` |
| `HEX_PUBLIC_URL` | Gateway origin advertised by the connection-settings endpoint |
| `HEX_SITES_PROVIDER` | `filesystem` or `none` |
| `HEX_FILES_PROVIDER` | `memory` (default), `filesystem`, `azureblob` (connection-string mode), or `none` |
| `HEX_DATABASE_PROVIDER` | `memory`, `postgres`, or `none` |
| `HEX_REALTIME_PROVIDER` | `memory` or `none` |
| `DATABASE_URL` | PostgreSQL connection string when selected |
| `AZURE_BLOB_CONNECTION_STRING` | Azurite/service connection string when selected |
| `AZURE_BLOB_CONTAINER` | Upload container to initialize when selected |

Explicit `none` disables a capability. Missing connection settings for a selected service are errors; they do not silently enable a different provider. The optional adapter's `Config` is an ordinary `hex.Config`, so your server can override a provider or mount additional handlers.

## Verification

From the Hex repository:

```sh
npm run test:local
npm run test:services
```

`test:local` builds a temporary **independent Go module** importing Hex, verifies direct flag-free startup and CLI-managed startup, checks its custom route and provider configuration, publishes a site directly, restarts using a prebuilt binary, verifies that site files persist while in-memory documents/uploads reset, and checks process cleanup.

`test:services` starts the Compose services, runs PostgreSQL and Azure Blob provider integration tests, then repeats the external-server workflow with those services. It stops the containers afterward while retaining their volumes. To test already-running services without invoking Docker, set `HEX_TEST_POSTGRES_URL` and `HEX_TEST_BLOB_CONNECTION_STRING`, then run:

```sh
npm run test:services -- --existing
```

Ordinary `go test ./...` skips those service integration tests when their variables are absent. Local providers do not simulate Azure Files SMB caching, managed identity, private endpoint networking or Entra authentication. Those remain hosted integration checks. This tooling implements no login or claims handling.

# Standalone Go CLI

The CLI is implemented in Go at `cmd/hex` and `internal/cli`. The TypeScript browser client remains at `packages/client`; it is not the CLI runtime.

## Build and install

From this repository:

```sh
go install ./cmd/hex
```

Or build an executable without installing it:

```sh
go build -trimpath -o bin/hex ./cmd/hex
```

A compiled CLI does not need Go, Node.js, npm, or .NET installed to run. Go is needed to build source code, NGINX to serve local websites, AzCopy for Azure Files publishing/login, and Docker Compose only for explicitly selected local integration services. Frontend projects may have their own build-tool requirements.

Cross-compile a binary for a target OS/architecture:

```sh
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -o bin/hex-linux-amd64 ./cmd/hex
CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 go build -trimpath -o bin/hex-darwin-arm64 ./cmd/hex
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -trimpath -o bin/hex-windows-amd64.exe ./cmd/hex
```

The CLI uses the OS certificate trust store for HTTPS. On Unix, local processes receive SIGTERM and have five seconds before forced termination. Windows process termination does not offer the same graceful signal handling; native NGINX local hosting needs separate Windows validation.

## Existing users

Remove the old global npm CLI installation/link if it shadows the Go executable, then put the new executable on PATH. `hex --version` reports the binary version (development builds report `dev`).

Commands and configuration remain compatible: `setup`, `login`, `init`, `publish`, `delete`, `sites`, `capabilities`, `skills`, and `dev`. Existing `hex.json`, `hex.dev.json`, and version-1 profile/connection files can be reused. The profile location and `HEX_CONFIG_DIR` override are unchanged. Setup still delegates browser authentication to the hosting provider and storage login to AzCopy.

## Embedded assets

The binary embeds `internal/cli/assets/`: starter HTML/JavaScript, the agent skill, NGINX configuration, Compose services, and a compiled browser client. Building or using the CLI does not invoke npm.

`hex-client.js` is a checked-in build artifact generated from the TypeScript client. After changing that client, maintainers run:

```sh
npm ci
npm run build
npm run check:cli-client
```

Commit the refreshed embedded asset with the client change. Do not edit it independently. The NGINX Docker image copies the same canonical gateway template used by the local CLI.

## Verification

```sh
go test -race ./...
go vet ./...
```

Go tests cover setup/profile compatibility, browser handoff, dropped-file paths, publishing boundaries, AzCopy delegation and development settings. A binary test runs `hex init` and `hex setup --file` with an empty PATH, proving those operations need neither Go nor Node at runtime.

JavaScript integration tests remain developer tooling for the browser client and full platform. They build and invoke the Go executable:

```sh
npm run test:e2e
npm run test:local
npm run test:browser
```

NGINX is required for these tests. The browser test additionally requires the Playwright Chromium download. See [local development](local-development.md) for optional PostgreSQL/Azurite provider checks.

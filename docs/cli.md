# Standalone Go CLI

The CLI is implemented in Go at `cmd/hex` and `internal/cli`. The TypeScript browser client remains at `packages/client`; it is not the CLI runtime.

## Build and install

From this repository:

```sh
just install
```

Or build an executable without installing it:

```sh
just build
```

A compiled CLI does not need Go, Node.js, npm, or .NET installed to run. Go is needed to build source code, NGINX to serve local websites, AzCopy for Azure Files publishing/login, and Docker Compose only for explicitly selected local integration services. Frontend projects may have their own build-tool requirements.

Build versioned release binaries for macOS ARM64 and x86-64, Windows x86-64, and Linux x86-64:

```sh
just build-cli 0.1.0
```

Artifacts and checksums are written to `dist/cli/0.1.0/`. See [release recipes](releases.md) for publishing npm versions and GitHub CLI releases.

The CLI uses the OS certificate trust store for HTTPS. On Unix, local processes receive SIGTERM and have five seconds before forced termination. Windows process termination does not offer the same graceful signal handling; native NGINX local hosting needs separate Windows validation.

## Existing users

Remove the old global npm CLI installation/link if it shadows the Go executable, then put the new executable on PATH. `hex --version` reports the binary version (development builds report `dev`).

Commands include `setup`, `login`, `init`, `publish`, `delete`, `sites`, `capabilities`, `skills`, and `dev`. Existing `hex.json`, `hex.dev.json`, and version-1 profile/connection files can be reused, including explicit `directory: "public"` and pinned profiles. New projects default to `dist` and resolve the current default platform profile unless one is explicitly selected. The profile location and `HEX_CONFIG_DIR` override are unchanged. Setup delegates browser authentication to the hosting provider and storage login to AzCopy.

## Project initialization and client package

`hex init [directory]` creates only `hex.json` and `.agents/skills/hex/SKILL.md`. It does not scaffold an app, copy a browser client, or invoke a package manager. Existing app files are preserved; an existing hex.json is not overwritten. Use `hex skills` to refresh the skill in an initialized project.

The binary embeds only the agent skill, NGINX configuration, and Compose services from `internal/cli/assets/`. The browser client is independently packaged as `@crazycatviking/hex`; app developers or their agents install it using their package manager and lockfile. See [client packaging](../packages/client/README.md) for registry and local tarball installation.

The NGINX Docker image copies the same canonical gateway template used by the local CLI.

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

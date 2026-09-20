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

A compiled CLI does not need Go, Node.js, npm, or .NET installed to run. Go is needed to build source code, NGINX to serve local websites, and Docker Compose only for explicitly selected local integration services. Hex downloads and caches AzCopy automatically for Azure publishing if it is not already available. Frontend projects may have their own build-tool requirements; plain static sites need no build.

Build versioned release binaries for macOS ARM64 and x86-64, Windows x86-64, and Linux x86-64:

```sh
just build-cli 0.1.0
```

Artifacts and checksums are written to `dist/cli/0.1.0/`. See [release recipes](releases.md) for publishing npm versions and GitHub CLI releases.

The CLI uses the OS certificate trust store for HTTPS. On Unix, local processes receive SIGTERM and have five seconds before forced termination. Windows process termination does not offer the same graceful signal handling; native NGINX local hosting needs separate Windows validation.

## Existing users

Remove the old global npm CLI installation/link if it shadows the Go executable, then put the new executable on PATH. `hex --version` reports the binary version (development builds report `dev`).

Commands include `setup`, `login`, `init`, `publish`, `delete`, `sites`, `capabilities`, `skills`, `dev`, and `update`. Existing `hex.json`, `hex.dev.json`, and version-1 profile/connection files can be reused, including explicit directory settings and pinned profiles. Without a directory setting, publishing detects dist/, public/, or a plain site in the project root. Explicit settings take precedence. Remove an old `directory: "dist"` setting when converting a project to a plain site. Commands resolve the default platform profile unless one is explicitly selected.

## Updating an installed CLI

```sh
hex update
```

This downloads the latest prebuilt binary for the current OS/architecture, verifies SHA256SUMS, checks that the downloaded executable runs, and replaces the installed CLI. Matching installed binaries are not downloaded again. Profiles and project files are preserved. Go, Node, GitHub CLI, and administrator access are not required for user-owned installations; the installation directory must be writable.

The default source is the latest CLI release in `crazycatviking/hex`. A platform's optional `cliReleaseURL` is saved in its profile and used automatically for organization mirrors. `HEX_CLI_RELEASE_URL` or `hex update --release-url <directory-url>` can override the source. URLs require HTTPS, with loopback HTTP allowed for tests. No request to the protected Hex API is needed.

On Windows, the running executable is moved aside as `hex.exe.previous` before replacement and restored if replacement fails. Windows may keep that previous executable locked until processes exit; it is removed on the next update that replaces the binary. Close processes using that older version if Windows prevents cleanup. Unix replacement uses an atomic rename. Native macOS and Windows execution should be validated before broad rollout.

Older CLI releases without `update` need one more installation through the platform script to gain this command. Publish the new CLI release before distributing the updated server's installer/configuration. Use `hex skills` inside existing projects to refresh their agent instructions after updating.

## Project initialization and client package

`hex init [directory]` creates only `hex.json` and `.agents/skills/hex/SKILL.md`. It does not scaffold an app, copy a browser client, or invoke a package manager. Existing app files are preserved; an existing hex.json is not overwritten. Use `hex skills` to refresh the skill in an initialized project.

The binary embeds only the agent skill, NGINX configuration, and Compose services from `internal/cli/assets/`. The browser client is independently packaged as `@crazycatviking/hex`; app developers or their agents install it using their package manager and lockfile. See [client packaging](../packages/client/README.md) for registry and local tarball installation.

The NGINX Docker image copies the same canonical gateway template used by the local CLI.

## Verification

```sh
go test -race ./...
go vet ./...
```

Go tests cover setup/profile compatibility, plain-site publishing, path boundaries, managed AzCopy extraction/authentication, and self-updating a running binary. A binary test runs `hex init` and `hex setup --file` with an empty PATH. `HEX_TEST_DOWNLOAD_AZCOPY=1 go test ./internal/cli -run TestOfficialManagedAzCopy -v` additionally verifies the pinned official download and executable.

JavaScript integration tests remain developer tooling for the browser client and full platform. They build and invoke the Go executable:

```sh
npm run test:e2e
npm run test:local
npm run test:browser
```

NGINX is required for these tests. The browser test additionally requires the Playwright Chromium download. See [local development](local-development.md) for optional PostgreSQL/Azurite provider checks.

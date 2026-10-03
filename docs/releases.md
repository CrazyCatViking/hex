# Release recipes

Run `just --list` from the repository root. Release tooling requires Just, Git, Go, Node/npm, Bash, and `shasum` (available on macOS and through Perl's Digest::SHA on Linux). Publishing CLI releases also requires GitHub CLI authentication. Build recipes can cross-compile all supported binaries from a Linux or macOS development machine.

Start from a clean, committed checkout on a branch that tracks a remote branch. The publishing recipes fetch tags, require the branch to contain its upstream's latest commits, and push to that tracked remote. Commit your release source before invoking them.

## npm client

The package is **`@crazycatviking/hex`**, independently versioned in `packages/client/package.json`. Authenticate to npm as an account with access to that scope, then publish with one command:

```sh
npm login
just publish-client 0.10.0
```

Omit the version to increment the current client minor version and reset the patch to zero (for example, `0.9.3` becomes `0.10.0`):

```sh
just publish-client
```

The recipe checks npm authentication, installs dependencies with `npm ci`, updates the package version and workspace lockfile, builds and tests the client, packs it under `dist/npm/`, and verifies installation and imports in an independent app. It then commits only `packages/client/package.json` and `package-lock.json`, pushes the branch, and publishes publicly to npm with the `latest` tag.

For a prerelease, run `just publish-client 0.10.0-rc.1`; the recipe automatically uses the `next` npm tag. Versions already published to npm cannot be reused.

For packaging without publishing, use `just version-client 0.10.0` and `just pack-client`. These do not commit or push. If npm publishing fails after the release commit was pushed, use the exact `npm publish` retry command printed by the recipe; running `just publish-client` again would start another minor release.

Apps install it with `npm install @crazycatviking/hex`, `pnpm add @crazycatviking/hex`, or their preferred package manager. The Go CLI embeds no copy of this package.

## CLI binaries

Authenticate with `gh auth login`, then publish with one command:

```sh
just publish-cli 0.10.0
```

Or omit the version:

```sh
just publish-cli
```

The default increments the minor version of the highest versioned `cli-v<version>` tag, including prerelease tags, after fetching remote tags. It resets the patch to zero and removes any prerelease suffix. For example, `cli-v0.9.3` becomes `cli-v0.10.0`. With no CLI tags, it starts at `0.1.0`. Client and CLI versions remain independent.

The recipe checks GitHub authentication, runs `go test -race ./...` and `go vet ./...`, builds all four binaries and checksums, pushes the committed branch, creates and pushes `cli-v<version>`, and creates the GitHub release in `crazycatviking/hex`. It rejects an existing version tag. The release tag identifies the current committed source.

For a prerelease, use `just publish-cli 0.10.0-rc.1`; the GitHub release is automatically marked prerelease. If the GitHub release upload fails after the tag was pushed, retry with `just upload-cli 0.10.0` (using your release's actual version). This uploads the existing artifacts without bumping the version or creating another tag.

To build artifacts without publishing:

```sh
just build-cli 0.2.0
```

Outputs under `dist/cli/0.2.0/`:

| Artifact | Target |
| --- | --- |
| `hex-darwin-arm64` | macOS Apple Silicon |
| `hex-darwin-amd64` | macOS Intel x86-64 |
| `hex-windows-amd64.exe` | Windows x86-64 |
| `hex-linux-amd64` | Linux x86-64 |
| `SHA256SUMS` | SHA-256 checksums of all four binaries |

Builds disable CGO and embed the supplied version in `hex --version`. These are standalone executables; ordinary CLI usage needs no Go or Node runtime. Native NGINX is still required for `hex dev`. Cross-compilation does not substitute for runtime testing on each target. macOS binaries are not signed or notarized by these recipes.

Stable CLI releases are marked latest so company landing-page installers can download them automatically. Versions containing a prerelease suffix are marked prerelease and do not replace latest. Keep the four binary asset names and `SHA256SUMS` stable: the [platform installers](portal.md) use that contract. Publish at least one stable release before onboarding employees, and reserve the repository's latest release for CLI assets (or configure a dedicated release mirror).

Download the appropriate artifact and rename it to `hex` (or `hex.exe` on Windows) in a directory on PATH. On macOS/Linux, mark it executable with `chmod +x hex`; on Windows, use a user-owned tools directory on PATH. Download `SHA256SUMS` alongside the original artifact names to verify checksums before renaming.

For source installs on the development machine, use `just install`.

Installed clients can run `hex update` to download and verify the latest CLI without reinstalling or changing profiles. Clients predating the update command need one more run of the platform installer. Release the new CLI before deploying server changes that advertise optional connection fields such as `cliReleaseURL`.

## Release tooling verification

`just test-releases` (also included in `npm test`) verifies the release flows using temporary Git repositories and local remotes, with npm, Go, Just build steps, and GitHub publishing replaced by test commands. It covers default and explicit versions, prereleases, commit/tag pushes, and failures without publishing real releases.

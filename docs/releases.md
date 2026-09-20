# Release recipes

Run `just --list` from the repository root. Release tooling requires Just, Go, Node/npm, Bash, and `shasum` (available on macOS and through Perl's Digest::SHA on Linux). Publishing CLI releases also requires GitHub CLI authentication. Build recipes can cross-compile all supported binaries from a Linux or macOS development machine.

## npm client

The package is **`@crazycatviking/hex`**, independently versioned in `packages/client/package.json`. Authenticate to npm as an account with access to that scope, then:

```sh
npm ci
just version-client 0.2.0
just pack-client
```

`version-client` updates the package and workspace lockfile without creating a commit or tag. `pack-client` builds and tests the client and writes `dist/npm/crazycatviking-hex-0.2.0.tgz`. Install the tarball into an application with its package manager to test the release.

Commit the intended version changes, then publish:

```sh
just publish-client
```

The recipe builds, tests, and publishes the current package version publicly. For a prerelease, set a prerelease version and use `just publish-client next`. Versions already published to npm cannot be reused.

Apps install it with `npm install @crazycatviking/hex`, `pnpm add @crazycatviking/hex`, or their preferred package manager. The Go CLI embeds no copy of this package.

## CLI binaries

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

To publish, commit the release source, create and push its tag, and run:

```sh
git tag cli-v0.2.0
git push origin cli-v0.2.0
just publish-cli 0.2.0
```

Run the recipe from the tagged checkout. It rebuilds the binaries and creates a GitHub release in `crazycatviking/hex`, requiring the remote tag to exist. It does not commit, tag, or push source changes itself.

Stable CLI releases are marked latest so company landing-page installers can download them automatically. Versions containing a prerelease suffix are marked prerelease and do not replace latest. Keep the four binary asset names and `SHA256SUMS` stable: the [platform installers](portal.md) use that contract. Publish at least one stable release before onboarding employees, and reserve the repository's latest release for CLI assets (or configure a dedicated release mirror).

Download the appropriate artifact and rename it to `hex` (or `hex.exe` on Windows) in a directory on PATH. On macOS/Linux, mark it executable with `chmod +x hex`; on Windows, use a user-owned tools directory on PATH. Download `SHA256SUMS` alongside the original artifact names to verify checksums before renaming.

For source installs on the development machine, use `just install`.

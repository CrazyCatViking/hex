# Direct publishing

`hex publish` interacts directly with the provider resolved from the default profile, an explicit `platform`, or settings in `hex.json`. It detects the website directory automatically; optional `directory` pins the source. It does not call Hex for capabilities, credentials, upload, registration or completion. `siteBaseURL` (falling back to `server`) prints the resulting subdomain URL. `hex delete <site> --yes` likewise deletes directly from the publishing destination.

`hex sites` and capability refreshes use the Hex API, with the gateway's authentication. Storage authentication for publishing is independent of that gateway session or `HEX_TOKEN`.

For normal onboarding, download the installer from the [company landing page](portal.md); it installs the CLI and configures the default profile automatically. Then use `hex init` and `hex publish`. Azure publishing prepares its storage tool and starts Microsoft sign-in when needed, without separate installation or login commands. [Manual setup](setup.md) remains available for additional platforms. Projects use cached capabilities by default; `hex capabilities --refresh` explicitly calls the API. Neither installation nor login copies a browser session into the CLI.

## Plain sites and build output

With no `directory` setting, Hex looks for a regular `index.html` in this order:

1. `dist/`
2. `public/`
3. The project root

A plain HTML/CSS/JavaScript site can therefore contain `hex.json`, `index.html`, and its assets in the same directory. Run `hex publish` directly: no build or artificial `dist` folder is required. Asset contents and relative paths are preserved.

If package.json declares a nonempty `scripts.build` and `dist/index.html` is missing, Hex asks you to build first instead of falling back to source files. For another build output, set `"directory": "build"`, for example. An explicit setting always wins and never silently falls back. Set `"directory": "."` to deliberately publish the root, or remove an old `"directory": "dist"` setting to enable detection.

Hidden files/directories and `node_modules` are excluded everywhere. Root publishing also excludes `hex.json`, `hex.dev.json`, `AGENTS.md`, `package.json`, and npm/pnpm/Yarn/Bun lockfiles. Other regular files are website content; use a dedicated directory if the repository also contains unrelated files. Symlinks and paths outside the project are rejected.

## Local filesystem

```json
{
  "name": "demo",
  "server": "http://localhost:8080",
  "directory": "public",
  "publishing": {
    "provider": "filesystem",
    "root": "/absolute/path/to/hex/.hex-data/sites/public/sites"
  }
}
```

The provider synchronizes the build directory to `<root>/<name>/`. Relative roots are resolved from the app project directory. `hex dev` prints the correct absolute root for your platform (by default `<server-repository>/.hex-dev/sites/public/sites`). The repository's `npm run dev` wrapper uses `.hex-data/` for its reference server. Initialize an app with `hex init demo --publish-root <printed-root>`.

## Azure Files

```json
{
  "name": "demo",
  "server": "https://hex.example.com",
  "siteBaseURL": "https://hex.example.com",
  "directory": "dist",
  "publishing": {
    "provider": "azure-files",
    "url": "https://ACCOUNT.file.core.windows.net/sites/public/sites"
  }
}
```

Hex uses an existing AzCopy on PATH if available. Otherwise it downloads the supported official AzCopy release, verifies its pinned SHA-256, and extracts the executable into the OS user cache under `hex/tools/azcopy/<version>/<os>-<arch>/`. No administrator access, shell extraction tools, or PATH changes are needed. `HEX_AZCOPY_PATH` is an optional explicit override for managed installations. Updating Hex supplies updated managed-tool versions when needed.

The operator must authorize publishers with **Storage File Data Privileged Contributor** at an appropriate Azure Files scope. During `hex publish` or `hex delete`, Hex checks the cached storage session. If sign-in is needed in an interactive terminal, it opens Microsoft's device-login page, shows AzCopy's sign-in instructions, and continues the operation after authentication. AzCopy owns tokens and credential caching. `hex login` remains available for explicitly changing or refreshing the storage login, but is not a prerequisite for publishing.

Non-interactive commands never silently wait for a first-time sign-in: they report that the command should be rerun in an interactive terminal. Automation can supply `HEX_PUBLISH_SAS` or `AZCOPY_AUTO_LOGIN_TYPE`; Hex then leaves authentication to that configured mechanism. For example, `AZCOPY_AUTO_LOGIN_TYPE=AZCLI` uses an existing Azure CLI session. `AZCOPY_TENANT_ID` selects a tenant for the automatic device login when needed. Progress and sign-in instructions go to stderr; successful publish stdout remains the site URL.

If the operator supplies a SAS instead, set `HEX_PUBLISH_SAS` in the publisher's environment. Do not put a SAS in `hex.json` or frontend code. It needs the permissions required for reading/listing the destination and writing/deleting files. Azure Files SAS is not a per-site directory authorization boundary. Publishing authorization is controlled by Azure Storage, not by an API capability flag.

The adapter copies the validated build files into a temporary local directory to exclude hidden files and dependencies, then runs:

```text
azcopy sync <temporary-build-directory> <configured-url>/<site> \
  --recursive=true --delete-destination=true
```

The snapshot also contains the generated `.hex-site.json` metadata file. The CLI removes the temporary directory afterward. Unpublishing runs `azcopy remove <configured-url>/<site> --recursive=true`.

The storage account in the OpenTofu example is private. Publishers must have network/DNS access to its private endpoint, such as a VPN-connected machine or private CI runner. Entra login and SAS credentials do not bypass that restriction. The example intentionally does not open public storage access or grant publishing privileges to all Hex users.

## Directory convention and discovery

```text
<platform-sites-root>/
  public/
    sites/
      demo/
        index.html
        app.js
        .hex-site.json
```

NGINX maps each site subdomain to its own directory. The server enumerates the immediate directories under `public/sites/`, keeping valid DNS-label names with a regular root `index.html`. It skips incomplete directories, symlinked sites and symlinked indexes. `GET /api/sites` returns a sorted array of `{name, url, metadata?}`; its length is the site count. URLs are full subdomain URLs such as `https://demo.hex.example.com/`, derived from `HEX_SITE_BASE_URL`. See [Subdomain hosting](subdomains.md) for matching NGINX, DNS and authentication configuration.

Anything that places files in that layout can publish a site. Metadata is optional: existing sites remain discoverable without it. Discovery does not persist or cache its result. Old `sites/*.json` manifests and `releases/` content are ignored, not automatically deleted.

## Site metadata

Add optional `title`, `description`, and `author` strings to the source `hex.json`. The CLI publishes those fields and a generated UTC `publishedAt` timestamp in `.hex-site.json`. It leaves the source configuration and build output untouched, and never copies platform settings, storage destinations, or local paths into metadata. The generated file is limited to 64 KiB.

Set `"discoverable": false` in hex.json and republish to exclude a site from the company overview, statistics, and discovery API. Set it to true or remove it and republish to list the site. The default is visible, including older sites without metadata. Its URL and backend APIs remain accessible; visibility is not an authorization boundary.

```json
{
  "name": "demo",
  "title": "Team dashboard",
  "description": "Shared reports and tasks",
  "author": "Alex"
}
```

The local site provider reads metadata on each discovery request; custom providers can implement `hex.SiteMetadataReader`. NGINX blocks the hidden metadata file from direct static requests, while the discovery API exposes its descriptive fields in `metadata`. Invalid, oversized, or symlinked metadata produces an explicit discovery error. Attribution is project-provided, not verified identity. The timestamp identifies a publication attempt, not a transaction or audit event; failed or concurrent synchronization can leave mixed files.

## Synchronization behavior

- The website directory must be the project root or a subdirectory and contain a regular `index.html` at its root.
- Hidden paths and `node_modules` are excluded; symlinks and special files are rejected before transfer.
- A publish mirrors one site, including deleting obsolete destination files. Other site directories are untouched.
- Whole-site synchronization is not atomic. A failed or concurrent publish can leave mixed content. Republish to recover, and serialize deployment jobs for a site in your CI system.
- The filesystem adapter uses temporary-file renames, replaces root `index.html` after other assets, and writes metadata last. AzCopy controls upload ordering and replacement semantics for Azure Files.
- Unpublishing removes site assets only, not the separate upload store or document database.
- There are no server-side site-upload limits, release archives or rollback commands.

## Other providers

Publishing code lives in `internal/cli/publishing.go`. Provider operations are independent of the Hex API. A future GCS publisher can synchronize through GCP tooling or its Go SDK while retaining the same `hex publish` command. The host separately provides direct static serving and a read-only `SiteDirectory` implementation. GCS publishing is not implemented yet.

References: [AzCopy Azure Files synchronization](https://learn.microsoft.com/en-us/azure/storage/common/storage-use-azcopy-files#synchronize-files), [AzCopy user authorization](https://learn.microsoft.com/en-us/azure/storage/common/storage-use-azcopy-authorize-user-identity).

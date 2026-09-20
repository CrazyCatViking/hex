# Direct publishing

`hex publish` interacts directly with the provider resolved from the default profile, an explicit `platform`, or settings in `hex.json`. It uploads `dist` by default; optional `directory` selects another build output. It does not call Hex for capabilities, credentials, upload, registration or completion. `siteBaseURL` (falling back to `server`) prints the resulting subdomain URL. `hex delete <site> --yes` likewise deletes directly from the publishing destination.

`hex sites` and capability refreshes use the Hex API, with the gateway's authentication. Storage authentication for publishing is independent of that gateway session or `HEX_TOKEN`.

For normal onboarding, use [hex setup](setup.md) to download a profile rather than manually enter the settings below. Projects initialized with a profile use cached capabilities by default; `hex capabilities --refresh` explicitly calls the API. `hex login` delegates publishing sign-in to AzCopy. Neither setup nor login copies a browser session into the CLI.

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

Install a recent AzCopy v10 with Azure Files OAuth support. The OpenTofu example outputs this configuration using `tofu output -json publishing`. Copy the object into `hex.json`, or initialize a project with `hex init demo --server https://hex.example.com --publish-url https://ACCOUNT.file.core.windows.net/sites/public/sites`.

Authorize the publishing user or group with **Storage File Data Privileged Contributor** at an appropriate Azure Files scope, then run `azcopy login --tenant-id=<tenant-id>`. Alternatively use an existing Azure CLI session by setting `AZCOPY_AUTO_LOGIN_TYPE=AZCLI` and `AZCOPY_TENANT_ID` after `az login`. The Hex CLI does not perform that login or mint storage tokens. Use a pre-established login rather than an interactive device prompt inside a publish command.

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

- The build directory must be a subdirectory of the app project and contain `index.html` at its root.
- Hidden paths and `node_modules` are excluded; symlinks and special files are rejected before transfer.
- A publish mirrors one site, including deleting obsolete destination files. Other site directories are untouched.
- Whole-site synchronization is not atomic. A failed or concurrent publish can leave mixed content. Republish to recover, and serialize deployment jobs for a site in your CI system.
- The filesystem adapter uses temporary-file renames, replaces root `index.html` after other assets, and writes metadata last. AzCopy controls upload ordering and replacement semantics for Azure Files.
- Unpublishing removes site assets only, not the separate upload store or document database.
- There are no server-side site-upload limits, release archives or rollback commands.

## Other providers

Publishing code lives in `internal/cli/publishing.go`. Provider operations are independent of the Hex API. A future GCS publisher can synchronize through GCP tooling or its Go SDK while retaining the same `hex publish` command. The host separately provides direct static serving and a read-only `SiteDirectory` implementation. GCS publishing is not implemented yet.

References: [AzCopy Azure Files synchronization](https://learn.microsoft.com/en-us/azure/storage/common/storage-use-azcopy-files#synchronize-files), [AzCopy user authorization](https://learn.microsoft.com/en-us/azure/storage/common/storage-use-azcopy-authorize-user-identity).

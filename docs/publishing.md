# Direct publishing

`hex publish` interacts directly with the provider configured in `hex.json`. It does not call Hex for capabilities, credentials, upload, registration or completion. `siteBaseURL` (falling back to `server`) is only used to print the resulting subdomain URL. `hex delete <site> --yes` likewise deletes directly from the publishing destination.

`hex sites` and `hex capabilities` still use the Hex API, with the gateway's authentication. Storage authentication for publishing is independent of that gateway session or `HEX_TOKEN`.

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

The provider synchronizes the build directory to `<root>/<name>/`. Relative roots are resolved from the app project directory. `npm run dev` prints the correct absolute root for the reference platform. You can initialize a project with `hex init demo --publish-root /absolute/path/to/hex/.hex-data/sites/public/sites`.

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

It removes the temporary directory afterward. No archive or release metadata is stored in Azure. Unpublishing runs `azcopy remove <configured-url>/<site> --recursive=true`.

The storage account in the OpenTofu example is private. Publishers must have network/DNS access to its private endpoint, such as a VPN-connected machine or private CI runner. Entra login and SAS credentials do not bypass that restriction. The example intentionally does not open public storage access or grant publishing privileges to all Hex users.

## Directory convention and discovery

```text
<platform-sites-root>/
  public/
    sites/
      demo/
        index.html
        app.js
```

NGINX maps each site subdomain to its own directory. The server enumerates the immediate directories under `public/sites/`, keeping valid DNS-label names with a regular root `index.html`. It skips incomplete directories, symlinked sites and symlinked indexes. `GET /api/sites` returns a sorted array of `{name, url}`; its length is the site count. URLs are full subdomain URLs such as `https://demo.hex.example.com/`, derived from `HEX_SITE_BASE_URL`. See [Subdomain hosting](subdomains.md) for matching NGINX, DNS and authentication configuration.

Anything that places files in that layout can publish a site. There is no metadata file, manifest, database catalogue, registration step or completion notification. Discovery does not persist or cache its result. Existing public directories from older Hex versions remain discoverable; old `sites/*.json` manifests and `releases/` content are ignored, not automatically deleted.

## Synchronization behavior

- The build directory must be a subdirectory of the app project and contain `index.html` at its root.
- Hidden paths and `node_modules` are excluded; symlinks and special files are rejected before transfer.
- A publish mirrors one site, including deleting obsolete destination files. Other site directories are untouched.
- Whole-site synchronization is not atomic. A failed or concurrent publish can leave mixed content. Republish to recover, and serialize deployment jobs for a site in your CI system.
- The filesystem adapter uses temporary-file renames and replaces root `index.html` last. AzCopy controls upload ordering and replacement semantics for Azure Files.
- Unpublishing removes site assets only, not the separate upload store or document database.
- There are no server-side site-upload limits, release archives or rollback commands.

## Other providers

Publishing adapters live in `packages/cli/src/publishing/`. They provide `publish(name, source)` and `delete(name)` independently of the Hex API. A future GCS adapter can synchronize through GCP tooling or its SDK while retaining the same `hex publish` command. The host separately provides direct static serving and a read-only `SiteDirectory` implementation. GCS publishing is not implemented yet.

References: [AzCopy Azure Files synchronization](https://learn.microsoft.com/en-us/azure/storage/common/storage-use-azcopy-files#synchronize-files), [AzCopy user authorization](https://learn.microsoft.com/en-us/azure/storage/common/storage-use-azcopy-authorize-user-identity).

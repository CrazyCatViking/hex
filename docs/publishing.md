# Publishing

`hex publish` publishes a site through the platform API. The server checks that the caller owns the site, then tells the CLI where to upload each changed file; file contents go straight to storage where the storage provider supports it. `hex delete <site> --yes` unpublishes through the API as well. The profile is resolved from the default platform, an explicit `platform`, or settings in `hex.json`.

For normal onboarding, download the installer from the [company landing page](portal.md); it installs the CLI and configures the default profile automatically. Then use `hex init` and `hex publish`. [Manual setup](setup.md) remains available for additional platforms.

## Plain sites and build output

With no `directory` setting, Hex looks for a regular `index.html` in this order:

1. `dist/`
2. `public/`
3. The project root

A plain HTML/CSS/JavaScript site can therefore contain `hex.json`, `index.html`, and its assets in the same directory. Run `hex publish` directly: no build or artificial `dist` folder is required. Asset contents and relative paths are preserved.

If package.json declares a nonempty `scripts.build` and `dist/index.html` is missing, Hex asks you to build first instead of falling back to source files. For another build output, set `"directory": "build"`, for example. An explicit setting always wins and never silently falls back. Set `"directory": "."` to deliberately publish the root, or remove an old `"directory": "dist"` setting to enable detection.

Hidden files/directories and `node_modules` are excluded everywhere. Root publishing also excludes `hex.json`, `hex.dev.json`, `AGENTS.md`, `package.json`, and npm/pnpm/Yarn/Bun lockfiles. Other regular files are website content; use a dedicated directory if the repository also contains unrelated files. Symlinks and paths outside the project are rejected.

## Protocol

1. **Start.** The CLI sends `POST /api/hex/sites/{site}/publish` with a manifest of every file (`{path, size, md5}`, the MD5 as base64), the site metadata from hex.json, and its optional `access` policy. The server:
   - authorizes the caller: owners may publish; a caller allowed to create sites claims an unowned name and becomes its owner (see [access control](access-control.md));
   - validates the manifest and the access policy;
   - deletes current files that would block the new layout — a file where the manifest needs a directory, or files inside a directory that becomes a file;
   - compares the manifest with the recorded manifest of the current publication and the files actually present;
   - answers `{uploads, unchanged}`: an upload target for each new or changed file, with `index.html` last.
2. **Upload.** Each target names a protocol. `hex` is a plain `PUT` of the file to a same-origin API path (`/api/hex/sites/{site}/publish/files/{path}`), authenticated like other API calls and used by publishers without direct uploads, such as the local filesystem. `azure-files` is a pre-signed Azure Files URL; the CLI creates the file at its size and writes it in ranges without sending any credential of its own. The CLI uploads four files in parallel and `index.html` after all other files.
3. **Complete.** The CLI sends the same request to `POST /api/hex/sites/{site}/publish/complete`. The server checks that every manifest file is present with the declared size (409 lists missing files otherwise), deletes files that are no longer in the manifest, writes `.hex-manifest.json` and `.hex-site.json`, and applies the access policy if the request contains one. It returns the site URL, which the CLI prints on stdout.

`DELETE /api/hex/sites/{site}` removes all of a site's files. Its access policy is kept, so the name stays reserved for its owners. Uploaded app files and database documents are not affected by publishing or unpublishing.

Limits, configurable on `hex.Config`: at most 20,000 files, 256 MiB per file (`MaxPublishFileBytes`) and 2 GiB per site (`MaxPublishBytes`). The manifest request is limited to 16 MiB. Paths are relative, use forward slashes and cannot contain dot-prefixed segments, so publishers cannot write the server-owned records. Uploads through the `hex` protocol also pass the gateway's request size limit (32 MiB in the shipped NGINX template).

## Change detection

`.hex-manifest.json` records the manifest of the last completed publication. A file is skipped when its path, size and MD5 match that record and a file of that size is still present. Everything else is uploaded again. An unreadable manifest only costs a full upload. NGINX blocks direct requests for the hidden records.

## Semantics

Publishing replaces files in place; it is not an atomic whole-site release.

- During a publication, visitors can see a mix of old and new files. Uploading `index.html` last and bundlers' hashed asset names keep this rare; obsolete files are only deleted when the publication completes, so a new page never refers to an asset that has already been removed.
- A failed upload leaves the files uploaded so far in place; run `hex publish` again. Completion verifies sizes, not contents.
- Concurrent publications of one site are not coordinated. Serialize deployment jobs for a site in CI.
- There are no release archives or rollback commands; republish an earlier version from source control instead.

## Authentication

API calls, including `hex`-protocol uploads, use `HEX_TOKEN` when set. Otherwise a profile with `auth.type: "oidc"` discovers the configured issuer and uses authorization code + PKCE through the browser. The CLI checks state and nonce and verifies the signed ID token's issuer, audience and expiry. It privately saves the session per platform/issuer/client/scopes and silently refreshes expired tokens. `hex login` always opens a new sign-in so users can switch identities; `hex logout` removes that platform's saved CLI session. A browser-open failure prints the sign-in URL, allowing manual use from headless terminals. `auth.type: "none"` skips CLI sign-in. See [connection configuration](setup.md#configure-a-hex-server).

The CLI [automatically migrates](setup.md#automatic-auth-format-migration) legacy profiles with a public client and tenant GUID to OIDC. Profiles that still have no `auth` retain the legacy behavior: when the profile has a `resource` (advertised by platforms that set `HEX_API_RESOURCE`), the CLI obtains a token for `<resource>/.default` once per command:

- **Built-in sign-in** when the platform also advertises a `clientId` and `tenantId` (`HEX_CLI_CLIENT_ID`, `HEX_CLI_TENANT_ID`). The CLI signs in as that public-client app registration against `https://login.microsoftonline.com/<tenantId>`, using the browser authorization-code flow with PKCE on an `http://localhost` redirect, with MFA and Conditional Access. The session is saved in `sign-in.json` in the Hex configuration directory (next to `profiles.json`, readable only by the user) and refreshed silently by later commands. No other tools are needed.
- **Azure CLI fallback** for platforms that advertise a resource without a client ID, and for a `--resource` override naming a different resource. The CLI requests the token with `az account get-access-token` and, in an interactive terminal, runs `az login --allow-no-subscriptions --scope <resource>/.default` when there is no session. `HEX_TENANT_ID` pins the tenant for this fallback only.

Interactive commands without a session open the browser automatically; non-interactive commands fail and ask for `hex login`. Azure CLI sessions are managed with `az logout`. The legacy Microsoft sign-in requires a Linux desktop session (DISPLAY or WAYLAND_DISPLAY); headless sessions and Codespaces are rejected rather than falling back to device-code sign-in. Automation supplies `HEX_TOKEN`.

The app registration requirements for built-in sign-in:

- The advertised registration allows public client flows (`isFallbackPublicClient`) and has a **Mobile and desktop applications** redirect URI `http://localhost`.
- Advertise the same registration the gateway authenticates with, so its client ID is the resource's own application. The CLI then requests a token for its own API and needs no extra consent or pre-authorization.
- The gateway accepts `api://<client-id>` as an audience; the Azure example's authentication configuration does.

Only the Azure CLI fallback needs the API scope pre-authorized for Azure CLI (client ID `04b07795-8ddb-461a-bbee-02f9e1bf7b46`).

Storage credentials never reach the CLI profile or hex.json. Azure Files upload URLs carry their own authorization and are never sent the API token; the CLI only sends the API token to same-origin API paths.

## Azure Files

The `azurefiles` provider (`server/providers/azurefiles`) writes with the server's Entra identity and hands out upload URLs:

- one user delegation SAS per changed file, granting create and write on exactly that file, valid for one hour, HTTPS only;
- signed with a user delegation key the server obtains with its own identity and caches for 12 hours (renewed two hours before expiry), so no storage account key is used for publishing;
- directories, including `public/` and `public/sites/`, are created by the server before uploads, so a freshly created share needs no manual initialization.

The server identity needs, on the sites storage account:

- **Storage File Data Privileged Contributor** — creating directories, recording publications and deleting files. A user delegation SAS never grants more than its signer holds.
- **Storage File Delegator** — obtaining the user delegation key.

Publishers need no storage role. They do need network reachability to the storage account's file endpoint for uploads: either public network access on the account (the SAS URLs remain the only way in for them) or private-endpoint connectivity such as a VPN or private runner. The server reaches the account through its private endpoint.

Configure the reference executable with `HEX_PUBLISHER_PROVIDER=azurefiles` and `AZURE_FILES_SHARE_URL=https://ACCOUNT.file.core.windows.net/SHARE`; `AZURE_CLIENT_ID` selects the managed identity. The provider has not been exercised against a live storage account by this repository's tests.

## Directory convention and discovery

```text
<platform-sites-root>/
  public/
    sites/
      demo/
        index.html
        app.js
        .hex-site.json
        .hex-manifest.json
```

NGINX maps each site subdomain to its own directory. The server enumerates the immediate directories under `public/sites/`, keeping valid DNS-label names with a regular root `index.html`. It skips incomplete directories, symlinked sites and symlinked indexes. `GET /api/sites` returns a sorted array of `{name, url, metadata?}`; its length is the site count. URLs are full subdomain URLs such as `https://demo.hex.example.com/`, derived from `HEX_SITE_BASE_URL`. See [Subdomain hosting](subdomains.md) for matching NGINX, DNS and authentication configuration.

Discovery reads the directory tree, not a catalogue, so sites placed there by other means remain discoverable. Metadata is optional: existing sites remain discoverable without it. Old `sites/*.json` manifests and `releases/` content are ignored, not automatically deleted.

## Site metadata

Add optional `title`, `description`, and `author` strings to the source `hex.json`. The CLI sends them with the publication and the server writes them, with a UTC `publishedAt` timestamp, to `.hex-site.json`. The source configuration and build output are untouched, and no platform settings or local paths are copied into metadata. The file is limited to 64 KiB.

Set `"discoverable": false` in hex.json and republish to exclude a site from the company overview, statistics, and discovery API. Set it to true or remove it and republish to list the site. The default is visible, including older sites without metadata. Its URL and backend APIs remain accessible; visibility is not an authorization boundary. To restrict who can view a site, use its [access policy](access-control.md).

```json
{
  "name": "demo",
  "title": "Team dashboard",
  "description": "Shared reports and tasks",
  "author": "Alex"
}
```

The local site provider reads metadata on each discovery request; custom providers can implement `hex.SiteMetadataReader`. Invalid, oversized, or symlinked metadata produces an explicit discovery error.

With an identity provider, the server also records who created the site and who published it last, from the signed-in identity rather than from hex.json: `createdBy` and `createdAt` (kept across publications), `publishedBy` and `publishedAt`, each person as `{id, name}`. Publishers cannot set these fields. The landing page shows the creator instead of `author` when known. Each publication is added to a history of the last 50 (`.hex-history.json`: when, by whom, file count and bytes), which owners and admins read with `GET /api/hex/sites/{site}/history`. Unpublishing removes the site's files, including its metadata and history.

## Publishing files and folders

`hex publish` decides what to publish from its target, the current folder by default:

- **A folder with hex.json** is published as that project's site, as described above. `--name`, `--with` and `--update` are refused, because the name and access come from hex.json.
- **A file, or a folder without hex.json**, is published without a project: reports, exports, prototypes. The platform creates a site with a random name, such as `k7m2x9qp4t.hex.example.com`, that only its creator can open, and the CLI prints its URL. These sites are called artifacts.

For artifacts:

- A folder with `index.html` is published as a site. A single HTML file becomes the page.
- Any other file gets a generated page that previews it (images, PDF, video, audio, and text up to 1 MiB inline) with a download link. A folder without `index.html` gets a generated file listing.
- `--name` (`-n`) sets the human-readable title, which defaults to the file or folder name.
- `--with user:<email or id>` or `--with group:<object id>` (repeatable) shares it; the creator always stays a viewer. `hex access` changes the viewers later.
- `--update <site>` replaces an artifact's content at the same URL, keeping its title and viewers unless `--name` or `--with` is given.
- `hex sites --mine` lists everything you own, and `hex delete <site> --yes` removes one.

Artifacts never appear in the catalogue or discovery API. Anyone who can sign in may create them, even when `HEX_PUBLISHER_GROUPS` limits app names, because their names are random. The feature needs identities, access control and publishing; `GET /api/hex/capabilities` reports it as `artifacts`.

## Safety checks

Before uploading, the CLI refuses content that should not be published:

- in folders published without hex.json: `node_modules`, `.git`, `.ssh` and `.aws` folders anywhere inside;
- in every publication: files whose names look like secrets or keys, such as `.env`, `*.env`, `*.pem`, `*.key`, `*.pfx`, `*.p12`, `*.kdbx`, `id_rsa*`, `id_ed25519*`, `.npmrc` and `credentials`.

Projects keep leaving dot-files and `node_modules` out of their publish directory silently. Unusually large publications (more than 100 files, more than 20 MB in total, or any file over 10 MB) need confirmation: the CLI asks in a terminal and otherwise stops unless `--yes` (`-y`) is given.

## Other providers

A storage backend implements `hex.SitePublisher` (list, read, write and delete site files, delete a site), and optionally `hex.DirectUploader` to hand out pre-signed upload targets instead of receiving uploads through the server. `local.Store` publishes to a filesystem directory for `hex dev` and tests. The host separately provides static serving and a read-only `SiteDirectory`. A GCS publisher is not implemented yet.

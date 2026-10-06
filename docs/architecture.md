# Architecture and API contract

Hex's HTTP framework is independent of cloud infrastructure. See [Hosting contract](hosting.md) for the required host behavior, optional runtime capabilities and future GCP composition. The repository's [OpenTofu infrastructure](../deploy/opentofu/README.md) is an editable example, not part of the framework.

For consuming repositories, `hex dev` launches their own executable and NGINX using packaged local-development assets. `server/dev` optionally constructs local providers; PostgreSQL and Azurite can be started through Docker Compose. The core server has no dependency on this launcher. See [Local development](local-development.md).

## Azure example topology

```text
Browser / CLI
    │ HTTPS
Azure Container Apps ingress + Entra authentication
    │ port 8080
NGINX container
    ├── hex.example.com/ → Go-rendered platform landing page
    ├── <site>.hex.example.com/* → that site's mounted directory
    └── /api/* → Go server on loopback port 8081
                    ├── Azure Files read-only mount: site discovery
                    ├── Azure Files REST (managed identity): publishing, upload SAS
                    ├── Private Blob endpoint: uploaded app files
                    ├── Private PostgreSQL endpoint: JSON documents
                    └── In-process realtime broker
```

NGINX and Go run as two containers in the same Container App replica. Only port 8080 is exposed by ingress; Go binds to loopback. The full composition is shown above; the example defaults to site hosting only. Optional capability modules provision private storage/database resources only when selected. External PostgreSQL can be supplied instead of provisioning a server. Public storage access is disabled.

Azure Container Apps mounts Azure Files, not Blob Storage. Like Quick, NGINX reads published static app files directly from a mounted filesystem. Go handles application APIs, authorization, publishing, read-only site discovery, and the built-in main-domain landing page and installers. Published websites stay on NGINX; Go serves only bounded, authorized favicon previews on the portal origin. NGINX readiness uses its own `/healthz` response rather than the API.

Publishing is authorized by the API and uploads go directly to storage: `hex publish → POST /api/hex/sites/{site}/publish → per-file upload targets → Azure Files → POST .../publish/complete`. The server checks site ownership, signs one short-lived user delegation SAS per changed file with its managed identity, and records the publication when all files have arrived. The mounted share stays read-only. See [Publishing](publishing.md).

A deployment has this layout:

```text
public/sites/my-app/index.html
public/sites/my-app/assets/app.js
public/sites/my-app/.hex-site.json
public/sites/my-app/.hex-manifest.json
```

The site directory is authoritative. The server reads immediate directories under `public/sites/`, checks for a regular root `index.html`, and derives each site's name and subdomain URL. On completing a publication, the server writes the manifest to `.hex-manifest.json` and optional descriptive fields with a publication timestamp to `.hex-site.json`, which discovery exposes as `metadata`. Sites without metadata remain valid. Listing is a fresh enumeration, not a catalogue lookup. NGINX selects `public/sites/<site>/` as the document root from a validated hostname; hidden paths and symlinks are not served.

Publications replace files in place, with root `index.html` uploaded last and obsolete files deleted only on completion. The filesystem publisher writes temporary files and renames them. Publications are not atomic whole-site deployments and concurrent publishers are not coordinated. A failed publication can leave a partial update; republish to recover.

Previously published public directories remain valid. Old metadata and release directories are ignored and can be removed separately by the operator. No migration or registration API is required.

## Trust model

The API does not verify identity-provider tokens. The hosting layer authenticates API requests, site assets and WebSocket upgrades. In the Azure example this is Container Apps' built-in Entra authentication, with no excluded paths; the CLI presents Entra access tokens to the same gateway. Local development deliberately has no authentication.

An optional identity resolver reads the caller that the trusted gateway forwarded (Easy Auth headers in the Azure example) and enables per-site authorization. A server-owned access policy assigns owners, editors and viewers, restricts URL path prefixes, and sets read/write rules per collection, file prefix and realtime channel, including creator-only documents. Only owners publish a site. Without that configuration, every admitted user can view, edit and publish every site. See [Identity and site access control](access-control.md).

Each site has a separate browser origin, isolating browser storage. A site name supplied by a client is an organizational namespace, not an authenticated app identity. See [Subdomain hosting](subdomains.md) for cookie and authentication considerations.

As browser request hygiene, state-changing API requests require `X-Hex-Request: 1`. Requests with a foreign `Origin` or a cross-site Fetch Metadata header are rejected. The client and CLI supply the marker automatically; it is not a credential. No CORS permissions are granted. A reverse proxy must preserve the external `Host` header for same-origin and WebSocket checks.

## HTTP API

| Method | Path | Result |
| --- | --- | --- |
| GET | `/api/hex/capabilities` | Enabled built-ins, contract version and upload limit |
| GET | `/api/hex/config` | Optional non-secret connection document for CLI setup; JSON attachment |
| GET | `/` on the main domain | Go-rendered landing page with app browsing and onboarding |
| GET | `/start` on the main domain | Build-and-publish onboarding with installers |
| GET | `/api/hex/catalog` | HTMX catalog HTML fragment; `search` and `sort` query parameters |
| GET | `/api/hex/overview` | Platform name, visible sites, statistics, and installer availability |
| GET | `/api/hex/install/{os}` | Platform-configured installer attachment for `macos`, `linux`, or `windows` |
| GET | `/api/hex/me` | The caller's resolved identity; 401 for anonymous callers, no route without a resolver |
| GET | `/api/hex/sites/{site}/icon` | Authorized favicon preview for portal listings, with site/path permissions and an initial-letter fallback |
| GET | `/api/hex/authz` | NGINX `auth_request` endpoint for static assets; 204 or 403 for the `X-Hex-Site` and `X-Hex-Path` header values |
| GET/PUT/DELETE | `/api/hex/sites/{site}/access` | Owner- and admin-managed site access policy; no routes without identity and access configuration |
| GET | `/api/hex/sites/{site}/permissions` | The caller's role and rule grants on the site; advisory |
| POST | `/api/hex/sites/{site}/publish` | Manifest → `{uploads, unchanged}`; authorizes the owner or claims an unowned name |
| PUT | `/api/hex/sites/{site}/publish/files/{path}` | Raw file body for publishers without direct uploads (204) |
| POST | `/api/hex/sites/{site}/publish/complete` | Manifest → `{name, url, deleted, access?}`; verifies uploads and removes obsolete files |
| DELETE | `/api/hex/sites/{site}` | Unpublish (204); keeps the access policy |
| GET | `/api/hex/sites/{site}/history` | Owner- and admin-only publication history, newest first |
| POST | `/api/hex/artifacts` | `{title?}` → `{name, url, title, viewers}` (201); creates a private artifact with a random name |
| GET | `/api/hex/my-sites` | The sites and artifacts the caller owns, newest first (`hex sites --mine`) |
| GET | `/api/hex/directory` | People who have used the platform and configured groups, filtered by `query`; for pickers |
| GET | `/manage`, `/manage/{site}` | Management portal pages on the platform domain; `/api/hex/manage/…` serves their HTMX fragments and changes |
| GET | `/api/sites` | Discoverable sites as `{name,url,metadata?}`; hidden and inaccessible listings are omitted |
| GET/HEAD | `https://{site}.<site-domain>/{asset}` | NGINX static file; directory indexes use `index.html` |
| GET | `/api/sites/{site}/files` | Array of `{key,size}` |
| PUT | `/api/sites/{site}/files/{key}` | Raw binary body → `{key,size}` |
| GET | `/api/sites/{site}/files/{key}` | Binary attachment |
| DELETE | `/api/sites/{site}/files/{key}` | Delete (204) |
| GET | `/api/sites/{site}/db/{collection}` | Array of `{id,data,createdBy?}`; `after` and `limit` parameters |
| POST | `/api/sites/{site}/db/{collection}` | JSON object → generated `{id,data,createdBy?}` (201) |
| GET | `/api/sites/{site}/db/{collection}/{id}` | `{id,data,createdBy?}` |
| PUT | `/api/sites/{site}/db/{collection}/{id}` | Replace/upsert JSON object → `{id,data,createdBy?}` |
| DELETE | `/api/sites/{site}/db/{collection}/{id}` | Delete (204) |
| GET | `/api/sites/{site}/realtime/{channel}` | WebSocket upgrade |
| GET | `/api/sites/{site}/integrations` | Integrations and endpoint contracts, marked `allowed` for the caller; see [Integrations](integrations.md) |
| POST | `/api/sites/{site}/integrations/{integration}/{endpoint}` | Call an integration endpoint |
| GET | `/api/sites/{site}/ai/models` | Models the caller may use; see [AI](ai.md) |
| POST | `/api/sites/{site}/ai/stream`, `/api/sites/{site}/ai/complete` | A model turn as server-sent events, or its complete result |
| GET | `/api/hex/integrations`, `/api/hex/integration-approvals` | Integration catalog; admins' approval list |
| POST/PUT/DELETE | `/api/hex/sites/{site}/integrations/{integration}/approval` | Request, approve or revoke an integration for a site |
| GET | `/api/hex/manage/integration-audit` | Admins: the integration audit log; see [Auditing](integrations.md#auditing) |
| GET/DELETE | `/api/hex/connections`, `/api/hex/connections/{connector}` | The caller's connected accounts; disconnect |
| GET | `/api/hex/connections/{connector}/start`, `.../callback` | OAuth authorization code flow for connected accounts |
| GET/PUT | `/api/hex/sites/{site}/automations` | Owners: list or replace automations; see [Automations](automations.md) |
| POST | `/api/hex/sites/{site}/automations/test`, `.../automations/{name}/run` | Run a posted definition or a deployed automation |
| GET | `/api/hex/sites/{site}/automations/{name}/runs`, `/api/hex/sites/{site}/automation-runs/{id}` | Run history and one run |

Publishing routes exist only when a `SitePublisher` is configured. Registered API handlers return errors as `{ "error": "..." }`. Unknown API routes and methods use standard Go HTTP routing responses. Disabled capability APIs have no routes; the platform landing page remains available. Published website responses, redirects, MIME types, conditional requests and range requests are handled by NGINX. Go serves its built-in platform UI/assets and authorized favicon previews, not published website paths. The client handles both JSON and non-JSON errors.

Published site names are DNS labels: 1–63 lowercase letters, digits or hyphens, starting and ending with a letter or digit. Other API identifiers remain 1–64 ASCII letters, digits, underscores or hyphens, starting with a letter or digit. Application file keys and published file paths are relative paths without dot-prefixed segments, backslashes or NULs.

Static sites have no server-side runtime. Assets use relative paths; SPAs should use hash routing. Missing assets return 404 rather than an HTML fallback. Downloads use attachment disposition, while site assets use extension-derived MIME types.

## Provider contract

Providers must be safe for concurrent requests and return `hex.ErrNotFound` for missing objects or documents. Constructors establish resources and the host closes them. The framework does not close externally supplied providers.

### Object stores

`List(ctx, prefix)` returns all matching keys and sizes, including nested keys. Results must use a non-nil empty slice when empty. `Open` returns a reader whose caller closes it. Keys are logical forward-slash paths. Successful writes replace an entire object atomically. The API validates client paths; filesystem providers must additionally prevent escaping their configured root.

`ObjectStore` is for application uploads, not site publishing. Keep it separate from site assets. Storage resources are provisioned by infrastructure, not created by an API request.

### Site directories

`SiteDirectory.ListSites(ctx)` returns names of actual published directories. It is read-only and has no write or registration method. The filesystem implementation reads `public/sites/` and checks indexes without scanning all assets. The HTTP handler filters invalid names, sorts them, and builds subdomain URLs from `Config.SiteBaseURL`. A future provider can enumerate an object prefix. Static serving and publishing (`SitePublisher`) are configured independently of this interface.

### Site publishers

`SitePublisher` lists, reads, writes and deletes files of one site, with paths relative to the site directory, and deletes whole sites. `ListSiteFiles` returns every regular file with its exact size, including the server-owned dotfiles. A publisher that also implements `DirectUploader` returns upload targets (for example pre-signed URLs) for publishers instead of receiving their file bodies through the server; the server still writes its records with `WriteSiteFile`. The server owns authorization, validation, change detection and the `.hex-*` records; providers only perform storage operations. `local.Store` and `azurefiles.Publisher` are the included implementations.

### Database

Documents are keyed by `(site, collection, id)`. `Put(ctx, site, collection, id, data, WriteOptions)` replaces or inserts one JSON object and returns the stored document. `WriteOptions.Creator` is recorded as `createdBy` on insert and never changed; with `CreatorOnly`, replacing or deleting another creator's document returns `hex.ErrForbidden`. `List(ctx, site, collection, ListOptions)` uses exclusive `After`, ascending bytewise ID ordering, a `Limit` of 1–100 and an optional `CreatedBy` filter. Empty results are `[]`. Random generated IDs do not encode creation time. Put timestamps in document data if the application needs them.

The PostgreSQL provider exposes `Migrate`; the reference executable calls it on startup to create `hex_documents` (with its `created_by` column) and `hex_site_policies`, converting a legacy `hex_site_access` table. Embedders can run migration separately and give runtime connections reduced privileges.

### Identity and site access

`IdentityResolver.ResolveIdentity(r)` translates what the trusted gateway forwarded into an `Identity`; nil with a nil error means anonymous, and resolver failures are treated as anonymous so access decisions fail closed. The `easyauth` provider reads Azure Easy Auth's `X-MS-CLIENT-PRINCIPAL` headers and must only run behind that gateway. `StaticIdentity` serves local development.

`AccessStore` persists one `SiteAccess` policy per site and returns `hex.ErrNotFound` for sites without one, which stay open. The PostgreSQL provider stores policies as JSON in `hex_site_policies`; the in-memory store is for development and tests. Authorization reads current policies without a replica-local cache. Enforcement covers the `/api/sites/{site}/` namespace and its data rules, discovery, publishing, and — through NGINX `auth_request` against `/api/hex/authz` — static assets and path rules. App origins are additionally restricted to their own data namespace; management APIs belong on the platform API host. Policies are managed by their owners and by `Config.AdminGroups` members, directly or through publications.

`UpdateSiteAccess(ctx, site, update)` atomically reads the current policy, runs the authorization/update callback, and commits its result. A nil result deletes the policy; an error preserves it. Callbacks receive detached values and must not reenter the store. All writers, including `PutSiteAccess` and `DeleteSiteAccess`, must serialize with it across instances, even for absent names. PostgreSQL uses transaction-scoped advisory locks plus row locks; memory uses a mutex. Custom providers must supply equivalent semantics.

### Integrations and automations

`IntegrationStore` keeps integration approvals per `(site, integration)` and sealed connected-account credentials per `(owner, connector)`; credentials are opaque bytes the server encrypts. `AutomationStore` keeps each site's automations with their next run and the latest runs; `ClaimAutomation` must atomically move `nextRun` from the expected value to the next one and report whether the caller won, which is how instances avoid running an occurrence twice. PostgreSQL stores them in `hex_integration_approvals`, `hex_integration_credentials`, `hex_automations` and `hex_automation_runs`.

### Realtime

The framework constructs channel names as `<site>/<channel>`. `Publish` fans out JSON to active subscribers including the publishing connection. Messages have no persistence, replay or exactly-once guarantee. `Subscription.Close` is idempotent. The HTTP handler owns the subscription lifecycle and calls `Close` on disconnection.

The in-memory broker buffers 64 messages per subscription and disconnects slow subscribers instead of silently dropping individual messages. WebSocket payloads are limited to 64 KiB and must be JSON text. The server sends pings every 30 seconds. Database writes do not automatically publish messages.

Do not increase Azure replica count while using this provider. Revisions and restarts disconnect sockets; apps should reconnect and reload authoritative state. A distributed provider is the extension point for scale-out.

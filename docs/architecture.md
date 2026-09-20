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
    ├── <site>.hex.example.com/* → that site's mounted directory
    └── /api/* → Go server on loopback port 8081
                    ├── Azure Files read-only mount: site discovery
                    ├── Private Blob endpoint: uploaded app files
                    ├── Private PostgreSQL endpoint: JSON documents
                    └── In-process realtime broker
```

NGINX and Go run as two containers in the same Container App replica. Only port 8080 is exposed by ingress; Go binds to loopback. The full composition is shown above; the example defaults to site hosting only. Optional capability modules provision private storage/database resources only when selected. External PostgreSQL can be supplied instead of provisioning a server. Public storage access is disabled.

Azure Container Apps mounts Azure Files, not Blob Storage. Like Quick, NGINX reads static files directly from a mounted filesystem. Go handles application APIs and read-only site discovery; it does not serve or receive website files. NGINX readiness uses its own `/healthz` response rather than the API.

The CLI publishes directly: `hex publish → provider adapter → Azure Files HTTPS endpoint`. A publisher supplies its own storage credentials and private network connectivity. No Hex API call is made during publishing or unpublishing. See [Publishing](publishing.md).

A deployment has this layout:

```text
public/sites/my-app/index.html
public/sites/my-app/assets/app.js
```

The site directory is authoritative. The server reads immediate directories under `public/sites/`, checks for a regular root `index.html`, and derives each site's name and subdomain URL. No site metadata is persisted. Listing is a fresh enumeration, not a catalogue lookup. NGINX selects `public/sites/<site>/` as the document root from a validated hostname; hidden paths and symlinks are not served.

The CLI's filesystem publisher writes temporary files and renames them, with root `index.html` replaced last. The Azure Files publisher delegates synchronization to AzCopy. Neither provides an atomic whole-site deployment or coordinates concurrent publishers. Failed synchronization can leave a partial update; republish to recover.

Previously published public directories remain valid. Old metadata and release directories are ignored and can be removed separately by the operator. No migration or registration API is required.

## Trust model

The API does not verify identity-provider tokens or implement user authorization. The hosting layer authenticates API requests, site assets and WebSocket upgrades. In the Azure example this is Container Apps' built-in Entra authentication, with no excluded paths. Direct publishing is authorized by the storage provider separately. Local development deliberately has no authentication.

Each site has a separate browser origin, isolating browser storage. Every admitted user can still access all API namespaces under the initial trust model. A site name supplied by a client is an organizational namespace, not an authenticated app identity. See [Subdomain hosting](subdomains.md) for cookie and authentication considerations.

As browser request hygiene, state-changing API requests require `X-Hex-Request: 1`. Requests with a foreign `Origin` or a cross-site Fetch Metadata header are rejected. The client and CLI supply the marker automatically; it is not a credential. No CORS permissions are granted. A reverse proxy must preserve the external `Host` header for same-origin and WebSocket checks.

## HTTP API

| Method | Path | Result |
| --- | --- | --- |
| GET | `/api/hex/capabilities` | Enabled built-ins, contract version and upload limit |
| GET | `/api/sites` | Directory-derived array of `{name,url}`; length is the count |
| GET/HEAD | `https://{site}.<site-domain>/{asset}` | NGINX static file; directory indexes use `index.html` |
| GET | `/api/sites/{site}/files` | Array of `{key,size}` |
| PUT | `/api/sites/{site}/files/{key}` | Raw binary body → `{key,size}` |
| GET | `/api/sites/{site}/files/{key}` | Binary attachment |
| DELETE | `/api/sites/{site}/files/{key}` | Delete (204) |
| GET | `/api/sites/{site}/db/{collection}` | Array of `{id,data}`; `after` and `limit` parameters |
| POST | `/api/sites/{site}/db/{collection}` | JSON object → generated `{id,data}` (201) |
| GET | `/api/sites/{site}/db/{collection}/{id}` | `{id,data}` |
| PUT | `/api/sites/{site}/db/{collection}/{id}` | Replace/upsert JSON object → `{id,data}` |
| DELETE | `/api/sites/{site}/db/{collection}/{id}` | Delete (204) |
| GET | `/api/sites/{site}/realtime/{channel}` | WebSocket upgrade |

Registered API handlers return errors as `{ "error": "..." }`. Unknown API routes and methods use standard Go HTTP routing responses. Disabled capabilities have no routes. Website responses, redirects, MIME types, conditional requests and range requests are handled by NGINX. The Go handler returns 404 for website paths. The client handles both JSON and non-JSON errors.

Published site names are DNS labels: 1–63 lowercase letters, digits or hyphens, starting and ending with a letter or digit. Other API identifiers remain 1–64 ASCII letters, digits, underscores or hyphens, starting with a letter or digit. Application file keys are relative paths without dot-prefixed segments, backslashes or NULs. There are no site upload/delete routes; the former publishing endpoints return 404.

Static sites have no server-side runtime. Assets use relative paths; SPAs should use hash routing. Missing assets return 404 rather than an HTML fallback. Downloads use attachment disposition, while site assets use extension-derived MIME types.

## Provider contract

Providers must be safe for concurrent requests and return `hex.ErrNotFound` for missing objects or documents. Constructors establish resources and the host closes them. The framework does not close externally supplied providers.

### Object stores

`List(ctx, prefix)` returns all matching keys and sizes, including nested keys. Results must use a non-nil empty slice when empty. `Open` returns a reader whose caller closes it. Keys are logical forward-slash paths. Successful writes replace an entire object atomically. The API validates client paths; filesystem providers must additionally prevent escaping their configured root.

`ObjectStore` is for application uploads, not site publishing. Keep it separate from site assets. Storage resources are provisioned by infrastructure, not created by an API request.

### Site directories

`SiteDirectory.ListSites(ctx)` returns names of actual published directories. It is read-only and has no write or registration method. The filesystem implementation reads `public/sites/` and checks indexes without scanning all assets. The HTTP handler filters invalid names, sorts them, and builds subdomain URLs from `Config.SiteBaseURL`. A future provider can enumerate an object prefix. Static serving and direct CLI publishing are configured independently of this interface.

### Database

Documents are keyed by `(site, collection, id)`. `Put` replaces or inserts one JSON object. `List` uses exclusive `after`, ascending bytewise ID ordering, and a caller-supplied limit of 1–100. Empty results are `[]`. Random generated IDs do not encode creation time. Put timestamps in document data if the application needs them.

The PostgreSQL provider exposes `Migrate`; the reference executable calls it on startup to create `hex_documents` if absent. Embedders can run migration separately and give runtime connections reduced privileges.

### Realtime

The framework constructs channel names as `<site>/<channel>`. `Publish` fans out JSON to active subscribers including the publishing connection. Messages have no persistence, replay or exactly-once guarantee. `Subscription.Close` is idempotent. The HTTP handler owns the subscription lifecycle and calls `Close` on disconnection.

The in-memory broker buffers 64 messages per subscription and disconnects slow subscribers instead of silently dropping individual messages. WebSocket payloads are limited to 64 KiB and must be JSON text. The server sends pings every 30 seconds. Database writes do not automatically publish messages.

Do not increase Azure replica count while using this provider. Revisions and restarts disconnect sockets; apps should reconnect and reload authoritative state. A distributed provider is the extension point for scale-out.

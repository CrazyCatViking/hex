# Architecture and API contract

Hex's HTTP framework is independent of cloud infrastructure. See [Hosting contract](hosting.md) for the required host behavior, optional runtime capabilities and future GCP composition. The repository's [OpenTofu infrastructure](../deploy/opentofu/README.md) is an editable example, not part of the framework.

## Azure example topology

```text
Browser / CLI
    │ HTTPS
Azure Container Apps ingress + Entra authentication
    │ port 8080
NGINX container
    ├── /sites/* → Azure Files read-only mount (direct static serving)
    └── /api/* → Go server on loopback port 8081
                    ├── Azure Files read-write mount: publishing
                    ├── Private Blob endpoint: uploaded app files
                    ├── Private PostgreSQL endpoint: JSON documents
                    └── In-process realtime broker
```

NGINX and Go run as two containers in the same Container App replica. Only port 8080 is exposed by ingress; Go binds to loopback. The full composition is shown above; the example defaults to site hosting only. Optional capability modules provision private storage/database resources only when selected. External PostgreSQL can be supplied instead of provisioning a server. Public storage access is disabled.

Azure Container Apps mounts Azure Files, not Blob Storage. Like Quick, NGINX reads static files directly from a mounted filesystem. Go handles publishing and backend API operations but is absent from the website request path. No subrequest, authorization lookup or manifest resolution in Go is needed to serve an asset. NGINX readiness uses its own `/healthz` response rather than the API.

A deployment has this layout:

```text
sites/my-app.json
releases/my-app/<release-id>/index.html
releases/my-app/<release-id>/assets/app.js
public/sites/my-app/index.html
public/sites/my-app/assets/app.js
```

The manifest records the last successful publication for the management API; NGINX does not read it. The server stages the full release first, then synchronizes the public site folder, removing stale assets and replacing the root `index.html` last. Unpublishing removes the public files and manifest while retaining release archives and app data. NGINX's document root is only `public/`; metadata, staged releases, hidden temporary files and symlinks are not served.

The local provider writes temporary files and renames them. Each `Put` is atomic, but synchronizing an entire folder is not. Invalid archives and staging failures leave the public site unchanged; failures during synchronization can leave a partial update. Republish to recover. Publishing and unpublishing are serialized within one server process. This is the folder-sync model rather than the previous request-time active-release lookup, and it works on SMB without symlink support.

Sites published using the previous manifest-only layout need to be republished once to populate `public/sites/`.

## Trust model

The API does not verify identity-provider tokens or implement user authorization. The hosting layer authenticates external traffic, including API requests, site assets, publishing and WebSocket upgrades. In the Azure example this is Container Apps' built-in Entra authentication, with no excluded paths. Local development deliberately has no authentication.

Every admitted user can access all site namespaces. All hosted JavaScript shares one origin and is trusted to use these capabilities. A site name supplied by a client is an organizational namespace, not an authenticated app identity.

As browser request hygiene, state-changing API requests require `X-Hex-Request: 1`. Requests with a foreign `Origin` or a cross-site Fetch Metadata header are rejected. The client and CLI supply the marker automatically; it is not a credential. No CORS permissions are granted. A reverse proxy must preserve the external `Host` header for same-origin and WebSocket checks.

## HTTP API

| Method | Path | Result |
| --- | --- | --- |
| GET | `/api/hex/capabilities` | Enabled built-ins, contract version and upload limit |
| GET | `/api/sites` | Published site metadata |
| POST | `/api/sites/{site}/deploy` | ZIP body → published site metadata (201) |
| DELETE | `/api/sites/{site}` | Unpublish (204) |
| GET/HEAD | `/sites/{site}/{asset}` | NGINX static file; directory indexes use `index.html` |
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

Names are 1–64 ASCII letters, digits, underscores or hyphens, starting with a letter or digit. File keys are relative slash-separated paths without dot-prefixed segments, backslashes or NULs. ZIP uploads reject traversal, duplicate files, symlinks, oversized expanded content and archives without a root `index.html`.

Static sites have no server-side runtime. Assets use relative paths; SPAs should use hash routing. Missing assets return 404 rather than an HTML fallback. Downloads use attachment disposition, while site assets use extension-derived MIME types.

## Provider contract

Providers must be safe for concurrent requests and return `hex.ErrNotFound` for missing objects or documents. Constructors establish resources and the host closes them. The framework does not close externally supplied providers.

### Object stores

`List(ctx, prefix)` returns all matching keys and sizes, including nested keys. Results must use a non-nil empty slice when empty. `Open` returns a reader whose caller closes it. Keys are logical forward-slash paths. Successful writes replace an entire object atomically. The API validates client paths; filesystem providers must additionally prevent escaping their configured root.

Keep site and upload stores separate: they have independent internal key layouts. Azure storage containers and file shares are provisioned by infrastructure, not created by an API request. A site store must be paired with an independent static-serving mechanism that exposes its `public/` prefix; the Azure deployment uses a shared filesystem mount. Replacing that store with Blob Storage alone does not make its assets accessible to NGINX.

### Database

Documents are keyed by `(site, collection, id)`. `Put` replaces or inserts one JSON object. `List` uses exclusive `after`, ascending bytewise ID ordering, and a caller-supplied limit of 1–100. Empty results are `[]`. Random generated IDs do not encode creation time. Put timestamps in document data if the application needs them.

The PostgreSQL provider exposes `Migrate`; the reference executable calls it on startup to create `hex_documents` if absent. Embedders can run migration separately and give runtime connections reduced privileges.

### Realtime

The framework constructs channel names as `<site>/<channel>`. `Publish` fans out JSON to active subscribers including the publishing connection. Messages have no persistence, replay or exactly-once guarantee. `Subscription.Close` is idempotent. The HTTP handler owns the subscription lifecycle and calls `Close` on disconnection.

The in-memory broker buffers 64 messages per subscription and disconnects slow subscribers instead of silently dropping individual messages. WebSocket payloads are limited to 64 KiB and must be JSON text. The server sends pings every 30 seconds. Database writes do not automatically publish messages.

Do not increase Azure replica count while using this provider. Revisions and restarts disconnect sockets; apps should reconnect and reload authoritative state. A distributed provider is the extension point for scale-out.

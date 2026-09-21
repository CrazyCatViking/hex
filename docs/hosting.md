# Hosting contract

Hex is a Go framework and browser protocol, not an Azure deployment product. Infrastructure examples configure implementations of capabilities; they are not imported or read by the Go framework, JS client or CLI.

## What any host provides

1. An authenticated HTTPS entry point for site assets, API routes and WebSocket upgrades. Authentication and allowed-user policy are owned by the host. Hex never validates user tokens; an optional identity resolver reads the caller the gateway forwarded. A host enabling the `easyauth` resolver must strip client-supplied `X-MS-CLIENT-PRINCIPAL*` headers at the gateway, as Azure's built-in authentication does.
2. No unauthenticated network path around that entry point to the API or private assets.
3. A static server exposing each `public/sites/<site>/` directory on its own subdomain. Asset responses must not pass through Hex's Go handler, but the static server must consult `GET /api/hex/authz` (with the site name in `X-Hex-Site`) before serving, so [site access entries](access-control.md) also cover assets; the shipped NGINX template does this with `auth_request`. The initial NGINX example uses `HEX_SITE_DOMAIN`; discovery uses the matching `HEX_SITE_BASE_URL` origin.
4. Routing for `/api/` to the Hex HTTP handler, preserving the external Host header, methods, bodies and WebSocket upgrades. Clients use same-origin URLs.
5. Whichever `SiteDirectory`, `ObjectStore`, `Database` and `Realtime` implementations the deployment enables, with credentials available only to backend processes.
6. A direct publishing destination, separately authorized by its storage provider, that the Hex CLI can reach. The CLI uses a local publishing configuration; it does not contact Hex for publishing information or credentials.

Sites, uploads, database and realtime are independent capabilities. An API-only deployment can omit the static server's mount and site-discovery provider. A static-site deployment can omit uploads, database and realtime. Disabling `Sites` removes discovery, not a publisher's storage permissions. Disabled capabilities have no API routes and are reported as disabled by `/api/hex/capabilities`.

The Azure example implements this contract with Container Apps/Entra, NGINX, Azure Files, Blob Storage and PostgreSQL. None of those products is required by `hex.Config`.

## Reference executable

For local testing of a consuming server, call the optional [Go local configuration helper](local-development.md) directly. Application uploads, documents and realtime default to in-memory providers; only published site assets remain on disk. It requires no mode flag or special launcher. `hex dev` optionally orchestrates NGINX and integration-test services. The reference executable's explicit provider selection below is separate and does not switch configurations based on a mode flag.

`cmd/hex-server` is one composition root. It supports these explicit selections:

| Variable | Values | Default when omitted |
| --- | --- | --- |
| `HEX_SITES_PROVIDER` | `none`, `filesystem` | `filesystem` |
| `HEX_FILES_PROVIDER` | `none`, `memory`, `filesystem`, `azureblob` | `azureblob` when `AZURE_BLOB_ENDPOINT` is set, otherwise `filesystem` |
| `HEX_DATABASE_PROVIDER` | `none`, `memory`, `postgres` | `postgres` when `DATABASE_URL` is set, otherwise `memory` |
| `HEX_REALTIME_PROVIDER` | `none`, `memory` | `memory` |
| `HEX_IDENTITY_PROVIDER` | `none`, `easyauth`, `static` | `none` |

The inferred defaults preserve local development convenience. Infrastructure examples must select providers explicitly. `none` takes precedence over any leftover connection variables. Explicit `azureblob` without its endpoint and explicit `postgres` without a URL are startup errors, not requests to fall back to temporary storage.

Other reference executable settings:

| Variable | Purpose |
| --- | --- |
| `HEX_ADDR` | Listener; defaults to `127.0.0.1:8080`. The container image defaults to loopback port 8081. |
| `HEX_SITES_DIR` | Filesystem root for site files; defaults to `.hex-data/sites`. |
| `HEX_SITE_BASE_URL` | Parent site origin used by discovery, default `http://localhost:8080`. For example `https://hex.example.com` yields `https://demo.hex.example.com/`. |
| `HEX_PUBLIC_URL` | Enables the reference executable's connection-settings download and declares its canonical gateway origin. |
| `HEX_PLATFORM_NAME` | Human-readable connection name, default `Hex` (`Local Hex` in the local helper). |
| `HEX_PUBLISH_URL` | Non-secret Azure Files publishing URL advertised in connection settings. |
| `HEX_API_RESOURCE` | Non-secret Entra resource identifier (for example `api://<client-id>`) advertised in connection settings; profiles use it so `az account get-access-token` can authenticate CLI API commands. |
| `HEX_FILES_DIR` | Filesystem root for local uploads; defaults to `.hex-data/files`. |
| `AZURE_BLOB_ENDPOINT` | Blob service endpoint for the Azure upload provider. |
| `AZURE_BLOB_CONTAINER` | Pre-existing container, default `uploads`. |
| `AZURE_CLIENT_ID` | Optional Azure managed-identity selection used by DefaultAzureCredential. |
| `DATABASE_URL` | PostgreSQL connection string. Use TLS verification for remote services. |
| `HEX_ADMIN_GROUPS` | Comma-separated group, role or user IDs that administer every site access entry. |
| `HEX_IDENTITY_ID`, `HEX_IDENTITY_NAME`, `HEX_IDENTITY_GROUPS` | The fixed identity for the `static` resolver; defaults `local-dev`, `Local Developer`, none. |

Site access entries need durable storage: with `easyauth`, access control activates only alongside the `postgres` database provider (a startup warning notes when it is disabled). The `static` resolver accepts the in-memory store for local experimentation. See [Identity and site access control](access-control.md).

For a provider not supported by this executable, write a small executable that imports `server/`, constructs the providers and calls `hex.New`. The cloud SDK is then a dependency of that provider/composition, not the HTTP framework. The browser client and publishing protocol remain unchanged.

An embedded server sets `Config.Connection` to expose non-secret platform settings and OS-specific installers from its built-in main-domain landing page. Employees download a script through their signed-in browser; it installs the CLI and imports those settings automatically. All platform pages and download endpoints use the existing hosting authentication. No user tokens are issued or stored by Hex. See [landing page and installers](portal.md) and [manual setup](setup.md).

## A future GCP deployment

A GCP example could compose a VM, NGINX, GCS/gcsfuse, IAP, and Cloud SQL, following Quick's architecture. A CLI adapter would synchronize directly to GCS, with no Hex API calls. The server would enumerate sites through `SiteDirectory` and use a GCS-backed `ObjectStore` for app uploads. NGINX would expose the public prefix through its mount, and the PostgreSQL provider could connect to Cloud SQL. NGINX can map subdomains to directories independently of the Go API.

This does not require Azure Files, Azure environment variables, or Entra claims in the framework. A future implementation must verify GCS/FUSE cache visibility and storage-operation semantics; the local filesystem provider's rename assumptions should not be assumed to hold on every object-storage mount. GCP infrastructure and GCS providers are not implemented yet.

## Infrastructure composition

Keep cloud-specific modules concrete. The example root decides which modules exist and connects their outputs to the host's environment, secrets and mounts. Avoid a single universal infrastructure module with a cloud selector: each provider should have a readable composition around the same Hex interfaces.

Module outputs are deployment bindings, not an application dependency. The Azure hosting module accepts environment variables and secrets instead of knowing how to create a database; replacing a managed PostgreSQL module with an existing connection does not change Hex's API. Future hosting modules can consume equivalent bindings with their own authentication/networking mechanisms.

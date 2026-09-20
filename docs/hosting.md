# Hosting contract

Hex is a Go framework and browser protocol, not an Azure deployment product. Infrastructure examples configure implementations of capabilities; they are not imported or read by the Go framework, JS client or CLI.

## What any host provides

1. An authenticated HTTPS entry point for site assets, API routes, publishing and WebSocket upgrades. Authentication and allowed-user policy are owned by the host. The initial Hex server neither parses identity claims nor validates user tokens.
2. No unauthenticated network path around that entry point to the API or private assets.
3. A static server exposing the site store's `public/sites/<site>/` files. Asset responses must not pass through Hex's Go handler.
4. Routing for `/api/` to the Hex HTTP handler, preserving the external Host header, methods, bodies and WebSocket upgrades. Clients use same-origin URLs.
5. Whichever `ObjectStore`, `Database` and `Realtime` implementations the deployment enables, with credentials available only to backend processes.

Sites, uploads, database and realtime are independent capabilities. An API-only deployment can omit the static server's mount and site publishing provider. A static-site deployment can omit uploads, database and realtime. Disabled capabilities have no API routes and are reported as disabled by `/api/hex/capabilities`.

The Azure example implements this contract with Container Apps/Entra, NGINX, Azure Files, Blob Storage and PostgreSQL. None of those products is required by `hex.Config`.

## Reference executable

`cmd/hex-server` is one composition root. It supports these explicit selections:

| Variable | Values | Default when omitted |
| --- | --- | --- |
| `HEX_SITES_PROVIDER` | `none`, `filesystem` | `filesystem` |
| `HEX_FILES_PROVIDER` | `none`, `filesystem`, `azureblob` | `azureblob` when `AZURE_BLOB_ENDPOINT` is set, otherwise `filesystem` |
| `HEX_DATABASE_PROVIDER` | `none`, `memory`, `postgres` | `postgres` when `DATABASE_URL` is set, otherwise `memory` |
| `HEX_REALTIME_PROVIDER` | `none`, `memory` | `memory` |

The inferred defaults preserve local development convenience. Infrastructure examples must select providers explicitly. `none` takes precedence over any leftover connection variables. Explicit `azureblob` without its endpoint and explicit `postgres` without a URL are startup errors, not requests to fall back to temporary storage.

Other reference executable settings:

| Variable | Purpose |
| --- | --- |
| `HEX_ADDR` | Listener; defaults to `127.0.0.1:8080`. The container image defaults to loopback port 8081. |
| `HEX_SITES_DIR` | Filesystem root for site files; defaults to `.hex-data/sites`. |
| `HEX_FILES_DIR` | Filesystem root for local uploads; defaults to `.hex-data/files`. |
| `AZURE_BLOB_ENDPOINT` | Blob service endpoint for the Azure upload provider. |
| `AZURE_BLOB_CONTAINER` | Pre-existing container, default `uploads`. |
| `AZURE_CLIENT_ID` | Optional Azure managed-identity selection used by DefaultAzureCredential. |
| `DATABASE_URL` | PostgreSQL connection string. Use TLS verification for remote services. |

For a provider not supported by this executable, write a small executable that imports `server/`, constructs the providers and calls `hex.New`. The cloud SDK is then a dependency of that provider/composition, not the HTTP framework. The browser client and publishing protocol remain unchanged.

## A future GCP deployment

A GCP example could compose a VM, NGINX, GCS/gcsfuse, IAP, and Cloud SQL, following Quick's architecture. It would supply a GCS-backed `ObjectStore` for publishing/uploads, expose the public site prefix through the mount, and reuse the PostgreSQL provider for Cloud SQL. NGINX can map subdomains to directories independently of the Go API.

This does not require Azure Files, Azure environment variables, or Entra claims in the framework. A future implementation must verify GCS/FUSE cache visibility and storage-operation semantics; the local filesystem provider's rename assumptions should not be assumed to hold on every object-storage mount. GCP infrastructure and GCS providers are not implemented yet.

## Infrastructure composition

Keep cloud-specific modules concrete. The example root decides which modules exist and connects their outputs to the host's environment, secrets and mounts. Avoid a single universal infrastructure module with a cloud selector: each provider should have a readable composition around the same Hex interfaces.

Module outputs are deployment bindings, not an application dependency. The Azure hosting module accepts environment variables and secrets instead of knowing how to create a database; replacing a managed PostgreSQL module with an existing connection does not change Hex's API. Future hosting modules can consume equivalent bindings with their own authentication/networking mechanisms.

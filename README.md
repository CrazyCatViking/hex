# Hex

A small internal app platform: static sites, shared backend capabilities, a browser client, and a publishing CLI. The hosting gateway authenticates users; Hex assumes everyone who reaches it can use every API, read and change data, and publish or unpublish any site.

## Packages

| Path | Purpose |
| --- | --- |
| `server/` | Embeddable Go HTTP API framework |
| `server/providers/` | Local storage, Azure Blob Storage, PostgreSQL and in-memory providers |
| `cmd/hex-server/` | Configurable reference server executable |
| `packages/client/` | `@hex-platform/client`, a dependency-free browser JS/TS client |
| `packages/cli/` | `@hex-platform/cli`, publishing tools and bundled agent skill |
| `deploy/` | Container images, NGINX configuration and composable OpenTofu deployment examples |

## Run locally

Requires Go 1.25+, Node.js 20+, and NGINX. These packages are currently built from this workspace, not published to a registry.

```sh
npm ci
npm run build
npm link --workspace @hex-platform/cli
npm run dev
```

The development gateway binds to `127.0.0.1:8080`. NGINX serves published files directly and proxies `/api/` to Go on loopback port 8081. The launcher uses the same NGINX routing configuration as Azure. Set `NGINX_BIN` if NGINX is not on PATH, and optionally `NGINX_MIME_TYPES` if its MIME type file is in a nonstandard location. Files and site releases persist under `.hex-data/`; the default development database is in memory and resets on restart.

In another terminal:

```sh
hex init my-app --name my-app --server http://localhost:8080
```

Open `my-app` in your editor. From that project directory:

```sh
hex capabilities
hex publish
hex sites
```

Open `http://localhost:8080/sites/my-app/`. The starter contains an HTML page, JavaScript app, a browser copy of the client, and `.agents/skills/hex/SKILL.md`. Point your coding agent to that skill if it does not discover `.agents/skills` automatically. `hex skills` refreshes the Hex-owned skill file without modifying existing project instructions.

Edit files under `public/` and publish again. For a bundled app, install the client into that project, build with relative asset URLs, and change `hex.json.directory` to the build output directory. Only that directory is uploaded.

```json
{
  "name": "my-app",
  "server": "https://hex.example.com",
  "directory": "dist"
}
```

`hex delete my-app --yes` unpublishes a site. It does not delete its app data or stored releases.

## Browser API

```ts
import { createHexClient } from '@hex-platform/client';

const hex = createHexClient({ site: 'my-app' });
const capabilities = await hex.capabilities();

await hex.files.upload('notes.txt', new Blob(['Hello']));
const file = await hex.files.download('notes.txt');
const files = await hex.files.list();
await hex.files.delete('notes.txt');

const tasks = hex.db.collection<{ title: string; done: boolean }>('tasks');
const task = await tasks.create({ title: 'Ship the app', done: false });
await tasks.set(task.id, { title: 'Ship the app', done: true });
const page = await tasks.list({ limit: 100 });

const channel = hex.realtime.connect('updates', {
  onMessage: message => console.log(message),
  onClose: () => console.log('Disconnected'),
});
await channel.ready;
await channel.send({ type: 'tasks-changed' });
channel.close();
```

Clients use the current origin by default. Local frontend development should proxy `/api/` and WebSockets to the server rather than introduce cross-origin cookie authentication. `baseURL` is available for non-browser clients and tests.

## Embed the Go framework

```go
package main

import (
    "log"
    "net/http"

    hex "github.com/hex-platform/hex/server"
    "github.com/hex-platform/hex/server/providers/local"
    "github.com/hex-platform/hex/server/providers/memory"
)

func main() {
    files, err := local.New("data/files")
    if err != nil { log.Fatal(err) }
    defer files.Close()
    sites, err := local.New("data/sites")
    if err != nil { log.Fatal(err) }
    defer sites.Close()

    handler := hex.New(hex.Config{
        Files: files,
        Sites: sites,
        Database: memory.NewDatabase(),
        Realtime: memory.NewRealtime(),
    })
    log.Fatal(http.ListenAndServe("127.0.0.1:8080", handler))
}
```

Omit a provider to disable that capability. `GET /api/hex/capabilities` reports which built-ins are enabled. `hex.New` returns an API-only `http.Handler`; provider credentials, connection pools and lifecycle remain under the host application's control. Mount the site store in NGINX and serve its `public/` directory directly. The Go handler has no website-serving route.

The interfaces are defined in `server/storage.go`:

- `ObjectStore`: atomic object replacement, reads, prefix listing and deletion. Configure independent stores for site files and app uploads. The local provider works on the Azure Files share mounted by both Go and NGINX. Azure Blob supplies app upload storage; another site provider also needs a corresponding direct static-serving setup.
- `Database`: site-scoped JSON documents with keyset pagination. The PostgreSQL provider works with Azure Database for PostgreSQL or another PostgreSQL installation.
- `Realtime`: subscriptions and JSON broadcasts, allowing a future distributed broker implementation without changing the browser API.

See [the architecture and API contract](docs/architecture.md) for provider semantics and [the hosting contract](docs/hosting.md) for platform independence and reference-server configuration.

## Infrastructure examples

[OpenTofu examples](deploy/opentofu/README.md) compose cloud-specific modules around the framework. The initial [Azure Container Apps example](deploy/opentofu/examples/azure-container-apps/README.md) defaults to authenticated site hosting and lets you select additional capabilities:

```hcl
capabilities = {
  sites    = true
  files    = true
  database = "managed"
  realtime = true
}
```

Use `database = "external"` with your own PostgreSQL URL, or `"none"` to disable the database API. Disabling a capability also omits its managed resources in this example. The reference server has explicit provider selections so disabled capabilities do not fall back to local or in-memory services.

The infrastructure is an example to copy and adapt, not the only way to host Hex. The framework/client do not depend on Container Apps, Azure resource IDs, or Entra. A future GCP/Quick-style deployment can use the same API with GCP providers and a different authenticated hosting layer.

## Verification

```sh
go test -race ./...
go vet ./...
npm run build
npm test
npm run test:e2e
```

The end-to-end test requires NGINX. It starts Go and NGINX, initializes a real CLI project, publishes it, exercises the client through the gateway, republishes, and unpublishes it. It then stops Go and verifies that NGINX still serves the site's HTML and JavaScript while API requests fail. Go tests also cover realtime broadcasts, namespace separation, invalid archives, staging failures, file/directory transitions, upload limits, and filesystem traversal protection.

## Initial scope

- Sites share one origin at `/sites/<name>/`. Namespacing separates data organization, not authorization or browser trust boundaries.
- The in-process realtime provider requires one backend replica. Messages are transient; clients handle reconnects and refresh state themselves.
- Documents are JSON objects, at most 1 MiB. `set` replaces the entire document. Lists are ordered by ID, with up to 100 results per page. There is no query language or automatic database-change feed.
- Uploads and expanded site archives default to 32 MiB; sites are limited to 5,000 ZIP entries. Uploads are buffered in memory. File and site listings are currently unpaginated.
- Publishing stages and validates a release, then synchronizes `public/sites/<name>/` for NGINX, replacing the root `index.html` last. Each file replacement is atomic, but the entire folder update is not transactional: requests can span versions, and a storage failure during synchronization may leave a partially updated site. Republish to recover. Old releases are retained; there is no rollback command or garbage collector yet. Publish/delete operations are serialized within the server process.
- No custom integrations, identity API, application permissions, code generation or AI proxy is included in this version.

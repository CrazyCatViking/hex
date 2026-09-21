# Hex

A small internal app platform: static sites, shared backend capabilities, a browser client, and a publishing CLI. The hosting gateway authenticates API and website visitors. Publishers authenticate directly to their storage provider; publishing never goes through the Hex API.

This is the [github.com/crazycatviking/hex](https://github.com/crazycatviking/hex) monorepo: one root Go module for the server and CLI, a TypeScript browser-client package, and shared infrastructure examples and documentation.

## Packages

| Path | Purpose |
| --- | --- |
| `server/` | Embeddable Go HTTP API framework |
| `server/providers/` | Local storage, Azure Blob Storage, PostgreSQL, in-memory and Easy Auth identity providers |
| `server/dev/` | Optional local provider adapter for consuming Go applications |
| `cmd/hex-server/` | Configurable reference server executable |
| `packages/client/` | `@crazycatviking/hex`, a dependency-free browser JS/TS client |
| `cmd/hex/` | Standalone Go CLI entry point |
| `internal/cli/` | CLI implementation and embedded skill and development assets |
| `deploy/` | Container images, NGINX configuration and composable OpenTofu deployment examples |
| `examples/custom-server/` | Example consuming executable with its own API endpoint |

## Develop a platform in your own repository

Build/install the CLI from this checkout with `go install ./cmd/hex`, or use a prebuilt executable when available. Running the CLI requires neither Go nor Node.js. Building a consuming Go server still requires Go; `hex dev --binary` does not. Put your Go installation's bin directory on PATH. If you previously linked the Node CLI, remove that old global npm installation/link so `hex` resolves to the Go executable.

See [CLI build, distribution and migration](docs/cli.md), [the client package](packages/client/README.md), and [Just release recipes](docs/releases.md) for cross-platform binaries and npm distribution.

Your executable can import `server/` and use `server/dev` for its local configuration. Start that repository with:

```sh
hex dev --package ./cmd/platform
```

By default, application uploads, documents and realtime are in-memory; published website assets use local files for direct NGINX serving. No service containers are started. Use `hex dev --services postgres,azurite` only for explicit provider integration testing. The CLI builds **your** server, supplies environment variables and runs NGINX. It also supports prebuilt binaries and a `hex.dev.json` configuration file. No .NET or cloud account is required.

You can also run your executable directly with `go run .` or from an IDE: the `dev.Open` convenience configuration needs no mode flag. The CLI only adds orchestration. See [Local development](docs/local-development.md) and the [custom-server example](examples/custom-server/README.md). The commands below run that example using the same tooling.

## Run locally

Building from source requires Go 1.25+. Local website hosting also requires NGINX. Node.js is needed for npm-based app tooling, developing the browser client, and JavaScript/browser integration tests. The CLI itself has no Node.js dependency.

```sh
go install ./cmd/hex
hex dev --package ./examples/custom-server --data-dir .hex-data
```

The development gateway binds to `127.0.0.1:8080`. NGINX serves published files directly and proxies `/api/` to Go on loopback port 8081. The NGINX routing template is shared with the Azure image and embedded in the CLI. Set `NGINX_BIN` if NGINX is not on PATH, and optionally `NGINX_MIME_TYPES` if its MIME type file is in a nonstandard location. Published site directories persist under `.hex-data/`; default application uploads and documents are in memory and reset on restart. The launcher prints the absolute local publishing root.

In another terminal:

```sh
hex setup http://localhost:8080 --name local
hex init my-app
```

Open `my-app` in your editor. Initialization creates only `hex.json` and `.agents/skills/hex/SKILL.md`. A plain site can put index.html and its assets directly in the project root. For a bundled app, have your coding agent install `@crazycatviking/hex` and build the app with your preferred tooling. See [client installation](packages/client/README.md) for a local tarball workflow before a registry release. From the project directory:

```sh
hex capabilities
hex publish
hex sites
```

Open `http://my-app.localhost:8080/`. Each site has its own hostname and browser origin. Modern browsers resolve `*.localhost` to loopback; if your environment does not, add local DNS/hosts entries. Point your coding agent to the installed skill if it does not discover `.agents/skills` automatically. `hex skills` refreshes the Hex-owned skill file without modifying existing project instructions.

The default `hex.json` contains only the site name. Publishing detects index.html in dist/, public/, or the project root, using the current default platform profile. Projects with a package.json build script need their build output before automatic publishing. Set optional `directory` to pin a source (`"."` for the root) or `platform` to pin a saved profile. Add descriptive metadata as needed:

```json
{
  "name": "my-app",
  "title": "Team dashboard",
  "description": "Daily reports and shared tasks",
  "author": "Alex"
}
```

`hex publish` synchronizes directly to storage, and `hex delete my-app --yes` deletes that site's directory directly. Neither command calls the Hex API. Hex automatically prepares AzCopy and starts Microsoft storage sign-in when needed for Azure Files. Local publishing uses filesystem operations. See [Publishing](docs/publishing.md) for directory selection and authentication.

`hex sites` calls the read-only discovery API, which enumerates site directories containing `index.html` and returns names, subdomain URLs, and optional metadata. Publishing writes title, description, author, and a generated UTC `publishedAt` to `.hex-site.json` alongside the site; it excludes connection settings and local paths. Existing sites without metadata remain discoverable. Set `siteBaseURL` when the API origin differs from the parent site domain. App uploads and database data survive unpublishing.

## Connect to a company platform

Use `hex update` to install the latest verified CLI binary while preserving platform profiles. Older releases without this command need one more installation from the landing page first.

Visit the platform's main domain, such as **https://hex.smartdok.dev/**. Its Go-rendered landing page uses HTMX to browse apps and shows site counts, contributors, and recent publications. The **Get started** section offers an installer for the visitor's OS. Download it in the signed-in browser and run the displayed command: it installs the latest CLI and saves the correct platform profile automatically. Employees do not need to run `hex setup` or edit configuration.

Set `"discoverable": false` in an app's hex.json and republish to hide its listing and exclude it from statistics. Its URL continues to work. See [landing page and installers](docs/portal.md) for hosting, configuration, and release prerequisites. Locally, the landing page is at **http://localhost:8080/**.

Agents such as Claude Code can use `hex setup <url> --json` and, when user sign-in is required, `hex setup --file <downloaded-file> --json`. Setup saves a non-secret default profile; publishing resolves it from that cache without contacting Hex. See [Setup and authentication handoff](docs/setup.md).

## Browser API

```ts
import { createHexClient } from '@crazycatviking/hex';

const hex = createHexClient({ site: 'my-app' });
const capabilities = await hex.capabilities();

// Who is viewing, as resolved by the hosting gateway; null when the
// deployment has no identity resolver.
const identity = await hex.identity();

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

    hex "github.com/crazycatviking/hex/server"
    "github.com/crazycatviking/hex/server/providers/local"
    "github.com/crazycatviking/hex/server/providers/memory"
)

func main() {
    if err := run(); err != nil {
        log.Fatal(err)
    }
}

func run() error {
    files, err := local.New("data/files")
    if err != nil {
        return err
    }
    defer func() {
        if err := files.Close(); err != nil {
            log.Printf("close file storage: %v", err)
        }
    }()

    sites, err := local.New("data/sites")
    if err != nil {
        return err
    }
    defer func() {
        if err := sites.Close(); err != nil {
            log.Printf("close site storage: %v", err)
        }
    }()

    handler := hex.New(hex.Config{
        Files:    files,
        Sites:    sites,
        Database: memory.NewDatabase(),
        Realtime: memory.NewRealtime(),
    })
    return http.ListenAndServe("127.0.0.1:8080", handler)
}
```

Omit a provider to disable that capability. `GET /api/hex/capabilities` reports which built-ins are enabled. `hex.New` returns an API-only `http.Handler`; provider credentials, connection pools and lifecycle remain under the host application's control. Mount site storage in NGINX and route each hostname to `public/sites/<name>/`. Set `Config.SiteBaseURL` for directory-derived URLs. The Go handler has no website-serving route.

The interfaces are defined in `server/storage.go`:

- `SiteDirectory`: read-only enumeration of actual site directories. The local provider reads the Azure Files share mounted by Go and NGINX, or a local development directory.
- `ObjectStore`: uploaded application objects, with writes, reads, prefix listing and deletion. This is separate from publishing; Azure Blob supplies app upload storage in the example.
- `Database`: site-scoped JSON documents with keyset pagination. The PostgreSQL provider works with Azure Database for PostgreSQL or another PostgreSQL installation.
- `Realtime`: subscriptions and JSON broadcasts, allowing a future distributed broker implementation without changing the browser API.
- `IdentityResolver` and `AccessStore` (in `server/identity.go` and `server/access.go`): optional gateway-forwarded caller identity and per-site access entries. See [Identity and site access control](docs/access-control.md).

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

Use `gofmt` for Go, Prettier for JS/TS and project files, and `tofu fmt` for infrastructure. Formatting handles layout; the structure and naming principles in [AGENTS.md](AGENTS.md) still apply.

```sh
gofmt -w cmd internal server examples
npm run format
tofu fmt -recursive deploy/opentofu
```

`npm run format:check` checks JS/TS formatting without editing files.

```sh
go test -race ./...
go vet ./...
npm ci
npm run build
npm test
npm run test:e2e
npm run test:local
```

The end-to-end test requires NGINX. It starts Go and NGINX, publishes directly to the shared directory, exercises discovery and application APIs, then stops Go and verifies that publishing and unpublishing still work. CLI tests check that direct publishing makes zero requests to Hex, filters source files, and handles file/directory transitions. Go tests cover filesystem discovery, realtime, namespaces and application-upload limits. Azure Files adapter tests verify AzCopy invocation; live Azure publishing is not tested locally.

## Initial scope

- Sites use separate origins such as `https://demo.hex.example.com/`, isolating localStorage, sessionStorage and IndexedDB. The former shared `/sites/<name>/` web routes are gone. Subdomains are not per-site data authorization. See [Subdomain hosting](docs/subdomains.md).
- Sites are open to every authenticated user unless an owner restricts them to identity-provider groups with a [site access entry](docs/access-control.md), which then covers the site's assets, APIs and discovery listing. Apps read the viewer through `hex.identity()`. Access entries restrict viewers, not publishers: publishing permissions remain storage-wide.
- The in-process realtime provider requires one backend replica. Messages are transient; clients handle reconnects and refresh state themselves.
- Documents are JSON objects, at most 1 MiB. `set` replaces the entire document. Lists are ordered by ID, with up to 100 results per page. There is no query language or automatic database-change feed.
- Application file uploads through the API default to 32 MiB and are buffered in memory. Direct site publishing is not subject to that API limit. File and site listings are currently unpaginated.
- Publishing mirrors one site's directory and deletes obsolete files. It is not a transactional whole-site replacement. Local publishing replaces files atomically and writes `index.html` last; Azure Files synchronization follows AzCopy's ordering and semantics. Concurrent publishers are not coordinated. Republish after an interrupted synchronization.
- No custom integrations, per-site publishing authorization, code generation or AI proxy is included in this version.

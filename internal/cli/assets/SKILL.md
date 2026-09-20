---
name: hex
description: Build and publish web apps on Hex using its file storage, JSON document database, and realtime channels. Use when working in a Hex project with hex.json.
---

# Hex apps

## Platform setup with an agent

Azure publishing uses Azure CLI's Entra browser sign-in, including MFA. Azure CLI must be installed once; Hex invokes it automatically during publishing and gives AzCopy `AZCOPY_AUTO_LOGIN_TYPE=AZCLI`. Do not use device-code login or ask the user to run AzCopy separately. A missing browser session requires the user to run the Hex command in a desktop terminal. `AZCOPY_TENANT_ID` selects the organization tenant when needed.

Employees normally install Hex from their company's main-domain landing page. Its OS-specific installer saves the default platform profile automatically. After installation, start with `hex init` or `hex capabilities`; another setup step is not needed. Use the workflow below only when connecting an unconfigured CLI or adding another platform.

If a user needs to connect to a platform, run `hex setup <platform-url> --json`. This command never blocks on a prompt or opens a browser in JSON mode. Exit code 0 with `status: "ready"` means the profile is configured. Exit code 2 with `status: "download_required"` includes a `downloadURL`: ask the user to open it, sign in through their company's hosting provider, and download the JSON file. When they provide or drop the file into the conversation, run `hex setup --file "/actual/local/path.json" --server <platform-url> --json`. If you only receive file contents, save the JSON to a local file first. Do not ask for passwords, browser cookies, client IDs, or access tokens.

Use `hex init <app-name>` to create `hex.json` and this skill, or `hex init` in an existing app. It does not scaffold an app or install dependencies. By default hex.json contains only `name`; commands resolve the current default platform profile. Use `hex init --platform <profile>` to pin a destination, or `hex publish --platform <profile>` to override it for a command. Azure publishing automatically prepares AzCopy and initiates Microsoft storage sign-in when needed. Do not ask users to install or invoke AzCopy separately. For local filesystem publishing no login is needed. `hex update` installs the latest verified CLI; `hex skills` refreshes this skill afterward.

## Building an app

Read `hex.json` for the site name, source directory, and selected platform profile or explicit connection settings. `hex capabilities` reads cached profile capabilities when available; `--refresh` requests them from the API. Discovery is never a prerequisite for publishing. Read the existing app before editing it.

Choose the app framework and build tooling to fit the project. Install `@crazycatviking/hex` as an application dependency using the project's package manager (for example `npm install @crazycatviking/hex`, `pnpm add @crazycatviking/hex`, or `yarn add @crazycatviking/hex`). Keep its version in package.json and the lockfile; do not copy the client into the app. If the package has not yet been released to the configured registry, ask for its registry or a packed tarball from the Hex checkout (`just pack-client`), then install that tarball through the package manager.

Plain HTML/CSS/JavaScript sites need no build step: place index.html and assets in the project root or public/ and run `hex publish`. Hex selects the first directory containing index.html in this order: dist/, public/, project root. If package.json declares scripts.build and dist/index.html is missing, build first or configure the actual output directory; Hex does not silently publish the source. Set hex.json.directory only to pin a source, including `"."` for the root. Remove an old explicit `"dist"` setting to enable detection. Root publishing excludes Hex configuration, agent instructions, package manifests and lockfiles; hidden paths and node_modules are always excluded. Other regular files are published, so keep unrelated content outside the chosen directory.

When using the npm client, import `createHexClient` from `@crazycatviking/hex` and bundle it for the browser. Configure a relative asset base (for example Vite `base: './'`). Hex does not install application dependencies or run builds.

```js
import { createHexClient } from '@crazycatviking/hex';

const hex = createHexClient({ site: 'my-site' });
const capabilities = await hex.capabilities();
```

Use same-origin requests in deployed apps; no API keys or identity code is needed. Authentication happens at the hosting gateway. All authenticated users can access all sites and data. Site names namespace data, not permissions. Do not implement frontend-only access controls or embed external service credentials.

## Files

```js
await hex.files.upload('reports/report.pdf', file);
const files = await hex.files.list();
const blob = await hex.files.download('reports/report.pdf');
const url = hex.files.url('reports/report.pdf');
await hex.files.delete('reports/report.pdf');
```

Keys are relative paths without dot-prefixed segments. Downloads are attachments. Uploads replace the existing key. Default maximum upload size is 32 MiB. File listing currently returns all files for a site.

## JSON document database

```js
const tasks = hex.db.collection('tasks');
const created = await tasks.create({ title: 'Example', done: false });
await tasks.set(created.id, { title: 'Example', done: true });
const document = await tasks.get(created.id);
const page = await tasks.list({ limit: 100 });
const next = await tasks.list({ limit: 100, after: page.at(-1).id });
await tasks.delete(created.id);
```

Documents are `{ id, data }`. `set` replaces the entire object and creates it if missing. There is no patch, query language, transaction API or automatic database subscription. Lists sort lexicographically by ID, not creation time. Continue pagination using the last ID until an empty page. Maximum document size is 1 MiB. Collection names, IDs and channels contain 1–64 letters, digits, underscores or hyphens. Published site names must instead be lowercase DNS labels of 1–63 characters, with no underscores or leading/trailing hyphens.

## Realtime

```js
const channel = hex.realtime.connect('updates', {
  onMessage: message => console.log(message),
  onClose: () => console.log('Disconnected'),
});
await channel.ready;
await channel.send({ type: 'tasks-changed' });
channel.close();
```

Messages are JSON, limited to 64 KiB, and delivered to connected subscribers including the sender. Channels are scoped by site. They are transient, not durable queues. Slow clients disconnect. Handle disconnects explicitly; no automatic reconnect or history replay is provided. For live database UIs, save first and then broadcast an invalidation message; reload authoritative state from the database on connect and invalidation.

## Errors and publishing

Catch `HexError` for HTTP failures (`status` and `message`). A 404 means a missing object or unavailable capability. Network and authentication redirect failures can be ordinary errors. Never insert untrusted text using innerHTML.

Run `hex publish` from the project root after building if needed. It synchronizes directly to the `publishing` provider and never calls the Hex API. Set `publishing` to `{ "provider": "filesystem", "root": "/path/to/platform/public/sites" }` locally, or `{ "provider": "azure-files", "url": "https://ACCOUNT.file.core.windows.net/sites/public/sites" }` for Azure. The CLI appends the site name to the destination. `index.html` must be at the source root. Sites are served at `https://<name>.<parent-domain>/`, or `http://<name>.localhost:8080/` locally. `siteBaseURL` specifies the parent origin and defaults to `server`. There are no shared `/sites/<name>/` web routes. Use relative or root-relative assets and hash routing; there is no SPA fallback. Keep client API requests same-origin to preserve authentication. Subdomains isolate browser storage but do not grant separate backend permissions.

Add optional `title`, `description`, and `author` strings to hex.json for site attribution. Ask the user for author information rather than inventing it. The CLI publishes these fields, the optional `discoverable` setting, and a generated UTC `publishedAt` timestamp in `.hex-site.json`; connection settings and local paths are excluded. Source configuration and build output are not modified. Attribution is descriptive, not verified identity or access control.

Set `"discoverable": false` in hex.json for an app that should not appear in the company's landing-page overview or discovery API. Republish to apply the setting. Set it to true or omit it and republish when the app should become visible. Sites are visible by default. Hidden apps still work at their URLs; this flag is not access control. Statistics count only discoverable sites.

`hex sites` uses the read-only API to enumerate site directories containing a root index.html. Its JSON array contains names, URLs, and an optional `metadata` object with title, description, author, and publishedAt. Sites published without metadata remain discoverable. NGINX blocks direct requests for the hidden metadata file; discovery exposes its descriptive fields. `hex delete <name> --yes` removes the site and its metadata directly through the provider, leaving app uploads and database documents intact. Folder synchronization is not transactional; timestamps describe the publication attempt that produced the files, not an atomic release or verified audit event. Coordinate concurrent publishers and republish after a failure.

For `sites`/`capabilities` API access, the operator supplies `HEX_TOKEN` or sets `resource` after `az login`. Azure publishing reuses its cached storage session and starts sign-in automatically when required. In a non-interactive agent session, a missing login produces an actionable error: ask the user to run the Hex command in their terminal once, then retry. Automation can use `AZCOPY_AUTO_LOGIN_TYPE=AZCLI` or an operator-supplied `HEX_PUBLISH_SAS`. Publishing still requires Azure Files data permissions and private-endpoint connectivity; a gateway token grants neither. Never store tokens in app files or hex.json. Hex does not implement custom integrations, user identity APIs, AI calls or code generation yet.

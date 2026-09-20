---
name: hex
description: Build and publish web apps on Hex using its file storage, JSON document database, and realtime channels. Use when working in a Hex project with hex.json.
---

# Hex apps

## Platform setup with an agent

If a user needs to connect to a platform, run `hex setup <platform-url> --json`. This command never blocks on a prompt or opens a browser in JSON mode. Exit code 0 with `status: "ready"` means the profile is configured. Exit code 2 with `status: "download_required"` includes a `downloadURL`: ask the user to open it, sign in through their company's hosting provider, and download the JSON file. When they provide or drop the file into the conversation, run `hex setup --file "/actual/local/path.json" --server <platform-url> --json`. If you only receive file contents, save the JSON to a local file first. Do not ask for passwords, browser cookies, client IDs, or access tokens.

Then use `hex init <app-name>` to select the saved default profile. Its hex.json contains `platform`, `name`, and `directory`; the publishing destination and URLs are resolved from that profile. `hex login` delegates Azure Storage sign-in to Microsoft's AzCopy; the user completes its first-party login flow. It does not authenticate CLI requests to the Hex gateway. For local filesystem publishing no login is needed.

## Building an app

Read `hex.json` for the site name, source directory, and selected platform profile or explicit connection settings. `hex capabilities` reads cached profile capabilities when available; `--refresh` requests them from the API. Discovery is never a prerequisite for publishing. Read the existing app before editing it.

The starter is a static ES-module app. Import `createHexClient` from `./hex-client.js`. In bundled TypeScript projects, import it from `@hex-platform/client`. Configure a relative asset base (for example Vite `base: './'`) and set `hex.json.directory` to the build output directory. Do not publish source directories, secrets or node_modules.

```js
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

`hex sites` uses the read-only API to enumerate site directories containing a root index.html. Its JSON array contains names and URLs; its length is the count. There is no separate site metadata. `hex delete <name> --yes` removes that site directory directly through the provider, leaving app uploads and database documents intact. Folder synchronization deletes stale assets and is not transactional. Coordinate concurrent publishers and republish after a failure. No releases or manifests are written.

For `sites`/`capabilities` API access, the operator supplies `HEX_TOKEN` or sets `resource` after `az login`. Direct Azure Files publishing instead uses AzCopy with a prior `azcopy login`, or `AZCOPY_AUTO_LOGIN_TYPE=AZCLI`, or an operator-supplied `HEX_PUBLISH_SAS`. It requires Azure Files data permissions and network access to the private endpoint; the gateway token grants neither. Never store tokens in app files or hex.json. Hex does not implement custom integrations, user identity APIs, AI calls or code generation yet.

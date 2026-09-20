---
name: hex
description: Build and publish web apps on Hex using its file storage, JSON document database, and realtime channels. Use when working in a Hex project with hex.json.
---

# Hex apps

Read `hex.json` for the site name, server origin and publish directory. Run `hex capabilities` to discover enabled built-ins and upload limits. Read the existing app before editing it.

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

Documents are `{ id, data }`. `set` replaces the entire object and creates it if missing. There is no patch, query language, transaction API or automatic database subscription. Lists sort lexicographically by ID, not creation time. Continue pagination using the last ID until an empty page; avoid calling `page.at(-1).id` on an empty page. Maximum document size is 1 MiB. Collection names, IDs, channels and site names contain 1–64 letters, digits, underscores or hyphens, starting with a letter or digit.

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

Run `hex publish` from the project root after building if needed. It uploads the configured directory as a ZIP. `index.html` must be at its root. The resulting URL is `/sites/<name>/`; use relative asset URLs and hash-based routing for single-page apps. There is no SPA fallback.

Use `hex sites` to list published sites and `hex delete <name> --yes` to unpublish one. Publishing stages the release, then synchronizes its public folder, replacing the root index.html last. NGINX serves the files directly; only API calls reach Go. Folder synchronization is not transactional, so republish after a synchronization failure. Unpublishing removes public site files while preserving file uploads, database documents and stored releases.

For Azure CLI access, the operator supplies `HEX_TOKEN` or sets `resource` in hex.json after `az login`. Never write tokens into app files. Hex does not implement custom integrations, user identity APIs, AI calls or code generation yet.

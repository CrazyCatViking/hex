---
name: hex
description: Build and publish web apps on Hex using its file storage, JSON document database, and realtime channels. Use when working in a Hex project with hex.json.
---

# Hex apps

## Platform setup with an agent

Company platforms require a sign-in for CLI API calls, including publishing. Hex signs in itself through the browser (Entra, including MFA) and saves the session, refreshing it silently; no other tools are needed. In a non-interactive agent session without a saved session, commands fail with an actionable error: ask the user to run `hex login` in a desktop terminal once, then retry. `hex logout` forgets the session. Do not use device-code login and never ask for tokens; automation supplies `HEX_TOKEN`. Older platforms without a built-in sign-in app fall back to Azure CLI.

Employees normally install Hex from their company's main-domain landing page. Its OS-specific installer saves the default platform profile automatically. After installation, start with `hex init` or `hex capabilities`; another setup step is not needed. Use the workflow below only when connecting an unconfigured CLI or adding another platform.

If a user needs to connect to a platform, run `hex setup <platform-url> --json`. This command never blocks on a prompt or opens a browser in JSON mode. Exit code 0 with `status: "ready"` means the profile is configured. Exit code 2 with `status: "download_required"` includes a `downloadURL`: ask the user to open it, sign in through their company's hosting provider, and download the JSON file. When they provide or drop the file into the conversation, run `hex setup --file "/actual/local/path.json" --server <platform-url> --json`. If you only receive file contents, save the JSON to a local file first. Do not ask for passwords, browser cookies, client IDs, or access tokens.

Use `hex init <app-name>` to create `hex.json` and this skill, or `hex init` in an existing app. It does not scaffold an app or install dependencies. By default hex.json contains only `name`; commands resolve the current default platform profile. Use `hex init --platform <profile>` to pin a destination, or `hex publish --platform <profile>` to override it for a command. Local platforms need no sign-in. `hex update` installs the latest verified CLI; `hex skills` refreshes this skill afterward.

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

When `capabilities.identity` is true, `await hex.identity()` returns the signed-in viewer as `{ id, name?, provider?, groups?, roles? }`, resolved by the hosting gateway; it returns `null` on platforms without identity support. Use it to personalize the app. Locally, `hex dev` resolves a fixed `Local Developer` identity, who is a platform admin.

Use same-origin requests in deployed apps; no API keys or identity code is needed. Authentication happens at the hosting gateway. Never embed external service credentials.

## Access control

Without an access policy every signed-in user can view and edit a site. Put the policy in the `access` section of hex.json; `hex publish` applies it and the platform enforces it on pages, files, documents and realtime channels. Frontend checks are never enforcement.

```json
{
  "name": "team-dashboard",
  "access": {
    "viewers": ["group:<object-id>"],
    "editors": ["group:<object-id>"],
    "paths": [{ "prefix": "/admin/", "viewers": "owners" }],
    "collections": {
      "settings": { "read": "viewers", "write": "owners" },
      "drafts": { "read": "creator", "write": "creator" }
    },
    "files": { "exports/": { "read": "owners", "write": "owners" } },
    "channels": { "announcements": { "write": "owners" } }
  }
}
```

- Principals are `user:<id or email>`, `group:<object id>` or `role:<value>`; get real values from the user or `hex whoami`, never invent them. Omitted `owners` keep the current owners; the first publisher of a new name becomes its owner.
- Empty `viewers` means every signed-in user; empty `editors` means every viewer. Owners and platform admins pass every rule.
- Audiences are `viewers`, `editors`, `owners`, `creator` or an array of principals. Unset `read` defaults to viewers and unset `write` to editors; `"*"` sets the default for unlisted names.
- `creator` (collections only): documents carry a server-recorded `createdBy`; other people's documents return 404 and changing them returns 403.
- Path rules protect separately served files. Build admin areas as a separate HTML entry such as `admin/index.html` (a multi-page build), not a hash route inside the public page, and protect their data with collection rules.
- Expect `HexError` with status 403 for restricted reads and writes, and handle it in the UI.
- Use `await hex.permissions()` to show or hide links and controls: it returns `{ role, admin, publish, paths: [{ prefix, allowed }], collections, files, channels }` with `read`/`write` grants of `all`, `own` or `none` per rule (including `"*"`).
- `hex access show|set|clear|check <site>` manages or inspects policies directly.

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

Documents are `{ id, data, createdBy? }`; `createdBy` is the creator's identity ID, recorded by the server. `set` replaces the entire object and creates it if missing. There is no patch, query language, transaction API or automatic database subscription. Lists sort lexicographically by ID, not creation time. Continue pagination using the last ID until an empty page. Maximum document size is 1 MiB. Collection names, IDs and channels contain 1–64 letters, digits, underscores or hyphens. Published site names must instead be lowercase DNS labels of 1–63 characters, with no underscores or leading/trailing hyphens.

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

Run `hex publish` from the project root after building if needed. The platform checks that the user owns the site (the first publisher of a new name claims it), then the CLI uploads only changed files, `index.html` last, and completes the publication; it prints the site URL. A 403 means someone else owns the name or the user may not create sites: ask the user rather than picking another name silently. `index.html` must be at the source root. Sites are served at `https://<name>.<parent-domain>/`, or `http://<name>.localhost:8080/` locally. `siteBaseURL` specifies the parent origin and defaults to `server`. There are no shared `/sites/<name>/` web routes. Use relative or root-relative assets and hash routing; there is no SPA fallback. Keep client API requests same-origin to preserve authentication. Subdomains isolate browser storage but do not grant separate backend permissions.

Add optional `title`, `description`, and `author` strings to hex.json for site attribution. Ask the user for author information rather than inventing it. The platform records these fields, the optional `discoverable` setting, and a UTC `publishedAt` timestamp in `.hex-site.json`; connection settings and local paths are excluded. Source configuration and build output are not modified. `author` is descriptive only; on platforms with sign-in, the server itself records the verified creator (`createdBy`) and last publisher (`publishedBy`), shown in discovery and on the landing page.

Set `"discoverable": false` in hex.json for an app that should not appear in the company's landing-page overview or discovery API. Republish to apply the setting. Set it to true or omit it and republish when the app should become visible. Sites are visible by default. Hidden apps still work at their URLs; this flag is not access control. Statistics count only discoverable sites.

`hex sites` uses the read-only API to enumerate site directories containing a root index.html. Its JSON array contains names, URLs, and an optional `metadata` object with title, description, author, and publishedAt. Sites published without metadata remain discoverable. NGINX blocks direct requests for the hidden metadata file; discovery exposes its descriptive fields. `hex delete <name> --yes` unpublishes a site the user owns, leaving app uploads, database documents and the access policy intact. Publishing replaces files in place and is not transactional; coordinate concurrent publishers and republish after a failure.

To share a single report, export or build without creating an app, use `hex publish <file or folder without hex.json> -n "<title>"`: it prints a private link only the user can open, `--with user:<email>` or `--with group:<object id>` shares it, and `--update <site>` replaces its content at the same link. Ask the user who should see it before adding `--with`. `hex sites --mine` lists the user's sites. Publishing refuses node_modules, .git and secret-looking files such as .env or *.pem, and asks before more than 100 files or 20 MB; pass `--yes` only after the user agrees.

All API commands (`publish`, `delete`, `sites`, `capabilities --refresh`, `whoami`, `access`) use the saved sign-in for the profile's `resource`, or an operator-supplied `HEX_TOKEN`. Never store tokens in app files or hex.json. Hex does not implement custom integrations, AI calls or code generation yet.

---
name: hex
description: Build and publish web apps on Hex using its file storage, JSON document database, realtime channels, third-party integrations, built-in AI models and scheduled automations. Use when working in a Hex project with hex.json.
---

# Hex apps

## Platform setup with an agent

Company platforms require a sign-in for CLI API calls, including publishing. Hex signs in itself through the browser (Entra, including MFA) and saves the session, refreshing it silently; no other tools are needed. In a non-interactive agent session without a saved session, commands fail with an actionable error: ask the user to run `hex login` in a desktop terminal once, then retry. `hex logout` forgets the session. Do not use device-code login and never ask for tokens; automation supplies `HEX_TOKEN`. Older platforms without a built-in sign-in app fall back to Azure CLI.

Employees normally install Hex from their company's main-domain landing page. Its OS-specific installer saves the default platform profile automatically. After installation, start with `hex init` or `hex capabilities`; another setup step is not needed. Use the workflow below only when connecting an unconfigured CLI or adding another platform.

If a user needs to connect to a platform, run `hex setup <platform-url> --json`. This command never blocks on a prompt or opens a browser in JSON mode. Exit code 0 with `status: "ready"` means the profile is configured. Exit code 2 with `status: "download_required"` includes a `downloadURL`: ask the user to open it, sign in through their company's hosting provider, and download the JSON file. When they provide or drop the file into the conversation, run `hex setup --file "/actual/local/path.json" --server <platform-url> --json`. If you only receive file contents, save the JSON to a local file first. Do not ask for passwords, browser cookies, client IDs, or access tokens.

Use `hex init <app-name>` to create `hex.json` and this skill, or `hex init` in an existing app. It does not scaffold an app or install dependencies. By default hex.json contains only `name`; commands resolve the current default platform profile. Use `hex init --platform <profile>` to pin a destination, or `hex publish --platform <profile>` to override it for a command. Local platforms need no sign-in. `hex update` installs the latest verified CLI; `hex skills` refreshes this skill afterward.

## Using deployed apps

Use the existing CLI sign-in to read protected apps and call their exposed actions. All commands below accept `--platform <profile>`; otherwise the normal project/default platform applies. Start with `hex sites` and, when useful, `hex access check <site>`.

```sh
hex fetch --site my-app --path /
hex fetch https://my-app.hex.company.example/help/
hex data list --site my-app --collection tasks --limit 100
hex data get --site my-app --collection tasks --id task-123
hex files list --site my-app --prefix exports/
hex files get --site my-app --key exports/latest.csv --output latest.csv
hex actions list --site my-app
hex actions describe --site my-app create-task
hex actions run --site my-app create-task --input @task.json
```

`fetch` returns the original HTML/text/JSON/bytes and does not run JavaScript. It only accepts the selected platform's configured origins/direct site subdomains and follows same-origin redirects. `--output` saves the response to a file. Data and file listings and action results return JSON; document lists paginate with `--after <last-id>` until empty. Data/file commands are read-only.

Before changing data, discover an available action and read its description and input/output schemas. Save the intended JSON input to a file and pass `--input @<path>`. The CLI fetches the current contract and validates input before sending; the backend also checks permissions and the contract. Do not infer undeclared action names or execution URLs. Extra fields are rejected when the schema sets `additionalProperties: false`. Do not automatically retry a mutating action after an ambiguous failure; check the resulting state first.

Actions run as the existing user, default to the site's editors, and use existing site/data permissions. No separate agent sign-in or scopes are needed.

When building an app whose data people or agents should change from the CLI, declare actions in the `actions` array of its hex.json; `hex publish` registers them and the platform performs them, so no server code is needed. Each action has a `name`, a `description` written for agents, an `operation` (`create` stores the input as a new document and returns its ID; `update` merges the input's fields into an existing document; `delete` removes one), a `collection`, an `input` JSON Schema with `"type": "object"` (prefer `additionalProperties: false`), an optional `idField` for update/delete (default `id`, which the schema must require) and an optional `audience` (`viewers`, `editors` (default), `owners` or principals). Callers also need write access to the collection. Declare one action per real task, such as `log-call` or `close-ticket`, rather than generic "edit anything" actions. Logic beyond one create, update or delete needs an action registered by the platform's Go backend in `hex.Config.Actions`; ask the platform developer for that.

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

## Integrations

Platforms can offer typed integrations with third-party systems (for example Slack, GitHub, Azure DevOps, Sentry, HubSpot or BigQuery). The platform calls the third-party API itself: apps never hold credentials and there is no raw proxy. Each integration has named endpoints with JSON Schema input contracts. Check `capabilities.integrations`, then discover what the viewer may use:

```sh
hex integrations list --site my-app          # endpoints, allowed flags, approval and connection state
hex integrations list --site my-app --json   # including input/output schemas
hex integrations catalog                      # every endpoint and its permission
hex integrations call --site my-app slack.users --input '{"limit":50}'
hex integrations call --site my-app hubspot.deals --input @query.json
```

From an app, `GET /api/sites/<site>/integrations` lists integrations (same shape as `--json`), and `POST /api/sites/<site>/integrations/<integration>/<endpoint>` with a JSON body calls one; the browser client wraps both: `await hex.integrations.list()` and `await hex.integrations.call('crm', 'deals', { stage: 'won' })`. A 409 throws `HexConnectionRequiredError`; `await hex.integrations.connect(error)` opens the platform's connect page in a popup and resolves when it closes, and `hex.integrations.callWithConnect(...)` connects and retries once. Use same-origin requests only.

- Every endpoint needs a permission such as `hubspot.deals`, granted by platform administrators to users, groups or Entra app roles. It is separate from site access policies; you cannot grant it in hex.json. Expect 403 and explain in the UI which access to request; use the `allowed` flag to hide unavailable features.
- Endpoints marked `write` change data in the other system and need the site's editor role.
- Some integrations require a platform admin to approve each site (`requiresApproval`). A site owner asks with `hex integrations request --site my-app hubspot --reason "..."`; admins use `hex integrations approvals`, `approve` and `revoke`. Until approved, calls answer 403.
- Results are shaped and may be cached briefly by the platform; do not poll endpoints in tight loops.

## Connected accounts

Some integrations (for example Azure DevOps, Atlassian Confluence or Google Tasks) call the other system as the viewer, so its own permissions apply. The viewer connects their account once through the platform; tokens stay on the server. Such integrations list a `connection` with `connected` and a `connectURL`. When an endpoint answers 409 with `{"error", "connect": {"connector", "title", "url"}}`, open `connect.url` in a popup or new tab (add `?return=<the app URL>` to come back) and retry after it closes. From the CLI:

```sh
hex connections list
hex connections connect atlassian    # opens the browser; the same platform sign-in is used
hex connections disconnect atlassian
```

Never ask users for third-party passwords or tokens.

## AI models

When `capabilities.ai` is true the platform offers language models through one streaming interface, whichever provider answers. Use it instead of calling model APIs from the browser; never embed API keys.

```sh
hex ai models --site my-app
hex ai ask --site my-app --model <id> --thinking medium --show-thinking "Summarize open bugs"
hex ai ask --site my-app --tools azuredevops.bugs,sentry.issues "Which bugs are new this week?"
hex ai ask --site my-app --json "Hi"    # the complete answer as JSON
```

The HTTP API is `GET /api/sites/<site>/ai/models`, `POST /api/sites/<site>/ai/stream` (server-sent events) and `POST /api/sites/<site>/ai/complete` (one JSON answer). The browser client wraps them: `hex.ai.models()`, `hex.ai.complete(request)`, `hex.ai.stream(request, { signal })` (an async iterable of events with `text()` and `finalMessages()` helpers), and `hex.ai.conversation({ model, system, thinking, integrationTools, tools: { name: { description, inputSchema, run } } })`, whose `send(text, { onEvent })` keeps the history, runs app tools and continues automatically — prefer it for chat UIs. A request is `{ model, system?, messages, tools?, thinking?: { effort?: "low"|"medium"|"high"|"xhigh"|"max", show? }, maxTokens?, integrationTools?: ["crm.*", ...] }`. Messages are `{ role: "user"|"assistant", content: [blocks] }` with blocks `text`, `image` (`mediaType`, base64 `data`), `thinking`, `tool_call` (`toolCallId`, `name`, `input`) and `tool_result` (`toolCallId`, `text`, `isError`); the last message must come from the user.

- Stream events are `text` and `thinking` deltas, `tool_call`, `tool_result` (an integration tool the server ran), `message` (a complete message to append to the conversation), `done` (`stopReason`: `end_turn`, `max_tokens`, `tool_use` or `refusal`, plus `usage`) and `error`. Keep the conversation by appending every `message` event's message in order, including thinking blocks unchanged, and send the whole list next turn.
- `integrationTools` offers integration endpoints to the model; the server runs them with the viewer's own permissions and approvals and leaves out endpoints the viewer may not use. Tools defined in `tools` are the app's: on `stopReason: "tool_use"` run them, add `tool_result` blocks to the last user message (it may already hold the server's results), and continue.
- Show thinking only when the user wants it. Handle 403 (no AI permission), 429 (daily allowance used or rate limit) and `error` events in the UI.

## Automations

Automations run work on a schedule or on demand without server code, as the site itself rather than a person. Declare them in hex.json under `automations`, or one per file as `automations/<name>.json` (the name defaults to the file name). `hex publish` deploys them and never publishes the `automations/` folder; `hex automations deploy` deploys them without republishing. A project that declares no automations leaves deployed ones unchanged; `"automations": []` removes them all.

```json
{
  "name": "weekly-defects",
  "description": "Post the weekly defect summary",
  "schedule": "0 8 * * MON",
  "timezone": "Europe/Oslo",
  "steps": [
    { "id": "counts", "call": "sentry.issue-counts", "input": { "statsPeriod": "7d" } },
    { "id": "summary", "ai": { "model": "<id>", "prompt": "Summarize: {{ steps.counts.output | json }}" } },
    { "id": "post", "if": "steps.counts.output.total | gt(0)", "call": "slack.post-message",
      "input": { "channel": "#dev", "text": "Week {{ now.week }}: {{ steps.summary.output.text }}" } },
    { "id": "archive", "save": { "collection": "reports", "id": "week-{{ now.week }}", "data": { "total": "{{ steps.counts.output.total }}" } } }
  ]
}
```

- `schedule` is a five-field cron expression or `@daily`/`@weekly`/`@hourly`, at most every 5 minutes, in `timezone` (IANA, default UTC). Without a schedule the automation only runs when triggered. `"disabled": true` pauses it.
- Each step has a unique `id` and exactly one of `call` (`<integration>.<endpoint>` with `input`), `action` (a site action with `input`), `ai` (`model`, `prompt`, optional `system`, `maxTokens`, `thinking`, `tools`), `query` (`collection`, `where` equality on top-level fields, `limit` up to 1000; output is `[{ id, data }]`) or `save` (`collection`, optional `id`, `data` object). `if` skips a step when falsy; `forEach` repeats it for each item of a list, exposing `item` and `index`, and its output is the list of outputs. A step stops the run when it fails.
- Templates: any string may contain `{{ expression }}`. A string that is exactly one placeholder becomes the JSON value; otherwise the text is inserted. Values are paths (`steps.<id>.output...`, `item`, `index`, `site`, `automation`, `run.trigger`, `run.dryRun`, `now.date|time|iso|year|month|day|monthDay|weekday|week|yesterday|weekStart|previousWeekStart|previousWeekEnd|unix`) or literals, followed by filters: `length`, `json`, `join(", ")`, `default("x")`, `first`, `last`, `limit(n)`, `upper`, `lower`, `truncate(n)`, `map("field")`, `where("field", value)`, `sum("field")`, `round(n)`, `eq(v)`, `ne(v)`, `gt(n)`, `lt(n)`, `not`, `date("DD.MM.YYYY")`. Missing paths are null; steps may only refer to earlier steps.
- Automations need integration permissions granted to `site:<name>` by platform administrators, and site approval where required. They cannot use connected-account integrations. Saved documents have `createdBy` `automation:<name>`.

```sh
hex automations list
hex automations test weekly-defects          # runs the local definition as a dry run: writes, actions and saves are skipped
hex automations test weekly-defects --live   # performs them
hex automations deploy
hex automations run weekly-defects [--dry-run]
hex automations runs weekly-defects --limit 5
```

Always dry-run with `hex automations test` before deploying or running live, and ask the user before running automations that post messages or change data.

## Errors and publishing

Catch `HexError` for HTTP failures (`status` and `message`). A 404 means a missing object or unavailable capability. Network and authentication redirect failures can be ordinary errors. Never insert untrusted text using innerHTML.

Run `hex publish` from the project root after building if needed. The platform checks that the user owns the site (the first publisher of a new name claims it), then the CLI uploads only changed files, `index.html` last, and completes the publication; it prints the site URL. A 403 means someone else owns the name or the user may not create sites: ask the user rather than picking another name silently. `index.html` must be at the source root. Sites are served at `https://<name>.<parent-domain>/`, or `http://<name>.localhost:8080/` locally. `siteBaseURL` specifies the parent origin and defaults to `server`. There are no shared `/sites/<name>/` web routes. Use relative or root-relative assets and hash routing; there is no SPA fallback. Keep client API requests same-origin to preserve authentication. Subdomains isolate browser storage but do not grant separate backend permissions.

Add optional `title`, `description`, and `author` strings to hex.json for site attribution. Ask the user for author information rather than inventing it. The platform records these fields, the optional `discoverable` setting, and a UTC `publishedAt` timestamp in `.hex-site.json`; connection settings and local paths are excluded. Source configuration and build output are not modified. `author` is descriptive only; on platforms with sign-in, the server itself records the verified creator (`createdBy`) and last publisher (`publishedBy`), shown in discovery and on the landing page.

Set `"discoverable": false` in hex.json for an app that should not appear in the company's landing-page overview or discovery API. Republish to apply the setting. Set it to true or omit it and republish when the app should become visible. Sites are visible by default. Hidden apps still work at their URLs; this flag is not access control. Statistics count only discoverable sites.

`hex sites` uses the read-only API to enumerate site directories containing a root index.html. Its JSON array contains names, URLs, and an optional `metadata` object with title, description, author, and publishedAt. Sites published without metadata remain discoverable. NGINX blocks direct requests for the hidden metadata file; discovery exposes its descriptive fields. `hex delete <name> --yes` unpublishes a site the user owns, leaving app uploads, database documents and the access policy intact. Publishing replaces files in place and is not transactional; coordinate concurrent publishers and republish after a failure.

To share a single report, export or build without creating an app, use `hex publish <file or folder without hex.json> -n "<title>"`: it prints a private link only the user can open, `--with user:<email>` or `--with group:<object id>` shares it, and `--update <site>` replaces its content at the same link. Ask the user who should see it before adding `--with`. `hex sites --mine` lists the user's sites. Publishing refuses node_modules, .git and secret-looking files such as .env or *.pem, and asks before more than 100 files or 20 MB; pass `--yes` only after the user agrees.

All API commands, including `fetch`, `data`, `files`, `actions`, `integrations`, `ai` and `automations`, use the saved platform sign-in or an operator-supplied `HEX_TOKEN`. Never store tokens in app files or hex.json.

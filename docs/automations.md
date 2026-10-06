# Automations

Every automation is a **JavaScript module** that performs its workflow. JSON
definitions contain only the metadata Hex needs to identify, schedule and deploy
that module. Branching, loops, transformations, integration calls, AI requests,
and document operations are all ordinary JavaScript inside the script.

## Project layout

`automations/weekly-report.json`:

```json
{
  "description": "Post a weekly issue report.",
  "schedule": "0 8 * * MON",
  "timezone": "Europe/Oslo",
  "script": { "file": "weekly-report.js" }
}
```

`automations/weekly-report.js`:

```js
// @ts-check

/** @param {import("@crazycatviking/hex/automations").AutomationContext} hex */
export default async function run(hex) {
  const issues = await hex.db.query("issues");
  const minimum = 5;
  const totals = new Map();

  for (const { data } of issues) {
    const count = Number(data.count);
    if (count < minimum) continue;
    const team = String(data.team ?? "unassigned");
    totals.set(team, (totals.get(team) ?? 0) + count);
  }

  for (const [team, count] of totals) {
    await hex.call("slack.post-message", {
      channel: "#reports",
      text: `${team}: ${count} issues in week ${hex.now.week}`,
    });
  }

  hex.log("Report finished", { teams: totals.size });
  return { teams: totals.size };
}
```

Metadata can also be declared in the `automations` array of `hex.json`. Both
sources are combined; duplicate names are rejected. Project metadata must
reference a `.js` file; script source is supplied by the CLI's deployment payload.

| Metadata | Meaning |
| --- | --- |
| `name` | Unique within the site; up to 64 letters, digits, `-` or `_`, starting with a letter or digit. Defaults to the JSON filename under `automations/` |
| `description` | Optional description shown in the portal; up to 500 characters |
| `schedule` | Optional five-field cron or descriptor such as `@daily`; no more frequent than every five minutes. Omit for manual-only execution |
| `timezone` | IANA timezone for scheduling and the run's clock snapshot; UTC by default |
| `disabled` | Suppresses scheduled execution while keeping the definition; manual/test runs remain available |
| `script` | Required `{ "file": "path.js" }` reference to the JavaScript module |

Paths are relative to the JSON definition's directory, or the project root for
definitions in `hex.json`, and must stay inside that directory. Referenced
scripts are excluded from static publication files. The CLI reads JavaScript
without transpiling, bundling or executing it, and includes its source in the
deployment. Each run uses the deployed source snapshot, not a server file or the
author's local file.

Definitions with `steps`, `if`, `forEach`, templated inputs or other workflow
fields are rejected. Put that logic in JavaScript. There is no template language
and no separate declarative workflow runner.

## Editor support

JavaScript is the supported language. Install `@crazycatviking/hex` in the app
project to get declarations from `@crazycatviking/hex/automations`. JSDoc imports
provide editor hints without runtime imports; `// @ts-check` enables checking in
supporting editors. No TypeScript transpilation is involved.

For a dedicated automation `jsconfig.json`, use `checkJs: true`,
`target: "ES2022"`, `module: "NodeNext"`, `moduleResolution: "NodeNext"`,
`lib: ["ES2022"]`, and `include: ["automations/**/*.js"]`. Runtime imports,
package installation and native extensions are unavailable.

## Script API

Export a default function taking `hex`. It can return a JSON value or a promise
of one; an omitted return becomes `null`. `hex` is also available globally.

The frozen context contains `site`, `automation`, `run` (`id`, `trigger`,
`dryRun`), and `now`, a snapshot of the run's start time in its timezone:
`iso`, `date`, `time`, `year`, `month`, `day`, `monthDay`, `weekday`, `week`,
`weekYear`, `yesterday`, `weekStart`, `previousWeekStart`, `previousWeekEnd`,
and `unix`. The week fields use ISO weeks and Monday week starts. Script-local
variables and functions hold all workflow state.

| API | Behavior |
| --- | --- |
| `await hex.call("integration.endpoint", input)` | Calls an endpoint with the site's grants and approvals, validating its input and output contracts |
| `await hex.action("name", input)` | Runs a site action as the automation's owner identity |
| `await hex.ai.complete({ model, prompt, system?, maxTokens?, thinking?, tools? })` | Uses the shared AI provider, permissions, model restrictions and budgets; returns `{ text, stopReason, usage }` |
| `await hex.db.query("collection", { where?, limit? })` | Reads this site's documents. Top-level fields must equal every `where` value, including its JSON type. Default limit 100; maximum 1000 |
| `await hex.db.save("collection", data, { id? })` | Creates a document, or replaces the document with `id`, attributed to the automation |
| `hex.log("message", data?)` | Records bounded, structured JSON in run history |

`console.log/info/warn/error` also record script logs. API values are JSON. Calls
execute sequentially, including when collected with `Promise.all`. Errors throw
JavaScript exceptions that can be handled with `try`/`catch`. An uncaught handler
error fails the run; completed effects are not rolled back. Strings such as
`"{{ value }}"` are literal data.

## Identity, isolation and limits

Automations act as their **site**, never as their deployer or the person who
triggers a run. Integration grants match `site:<name>` or `*`; required site
approvals are checked on every call. Personal connected accounts are unavailable.
Within the site, the automation acts as an owner, and documents record its
identity as `automation:<name>`.

Every run executes in a fresh QuickJS WebAssembly instance. There is no host
filesystem, server environment, credential access, unrestricted networking,
process execution, OS module or native Go object access. Hex's Go bridge fixes
the site, identity and dry-run mode and authorizes external operations. Finer
restrictions, such as allowed Slack channels, belong in endpoint policy or its
handler.

- At most 32 automations per site; every one requires a script, even if disabled.
- Script source: 128 KiB maximum.
- Guest memory: 64 MiB, including a JavaScript heap capped at 32 MiB and stack at
  1 MiB.
- Computation: five seconds per run, excluding authorized host operations. The
  ten-minute run deadline still applies while waiting on those operations.
- Script API calls, including logs: 100 per run. AI tools additionally follow the
  platform's tool-round and token limits.
- Operation input/output and final return value: 1 MiB maximum; stored output
  previews are truncated to 16 KiB.
- Logs: 32 KiB per run.
- Database scanning: at most 1,000 documents and 4 MiB of document data per run,
  including nonmatching documents. Providers reject oversized documents before
  copying their data; query reads fetch one bounded document at a time. Exhausted
  scan budgets fail the run even if JavaScript catches the operation error.
- Host operations: 30 seconds per operation, with two minutes of cumulative host
  work per run. Registered Go handlers must honor cancellation and bound their
  own result generation. Results are preflighted for size and complexity before
  JSON encoding; custom result marshalers are unavailable on the automation path
  except standard `time.Time` and `json.RawMessage` values.
- Scheduled, manual and test executions share four run slots per server instance.
  Manual/test starts return HTTP 503 with `Retry-After` when those slots are busy.
  Compile-only validation has a separate bounded lane so deployment can proceed
  while execution slots are occupied.
  At most four validations may be admitted or waiting; additional requests
  receive HTTP 503. Validation follows HTTP request cancellation.

Dry runs execute JavaScript, reads, queries and AI, but the Go bridge suppresses
integration writes, actions and saves. These operations still validate inputs
and permissions and return previews: `{ wouldCall, input }`, `{ wouldRun, input }`,
or `{ wouldSave, id, data }`. A preview is not the real write's return value;
use `hex.run.dryRun` when a branch depends on that result.

## Deployment, testing and history

```sh
hex automations test weekly-report       # local script, dry run
hex automations test weekly-report --live
hex automations deploy                  # replace metadata and scripts
hex automations list
hex automations run weekly-report        # deployed script, live run
hex automations run weekly-report --dry-run
hex automations runs weekly-report
```

Publishing deploys the project's definitions and replaces the entire site set.
An explicit `automations: []`, or an existing empty `automations/` directory,
removes them. Projects without automation metadata or the directory preserve
existing deployments. Unpublishing removes future schedules.

The owner/admin portal shows each automation's script, schedule, next three
occurrences and latest result, with **Test**, **Run now** and **History**.
Definitions are edited in the project, not in the portal.

Run history directly records the script's result, error, logs and host-operation
trace (kind, target, status, duration and output preview), including operations
whose errors the script catches. It also records the source SHA-256 and deployed
definition revision. The latest 50 runs per automation are retained. There are
no step records; progress is saved at run start and completion.
Internal provider errors are logged server-side and redacted from script errors
and history. Human-readable CLI output escapes terminal control characters.

| Method | Path | Result |
| --- | --- | --- |
| GET/PUT | `/api/hex/sites/{site}/automations` | Owners: list with next/last run, or replace the set |
| POST | `/api/hex/sites/{site}/automations/test?dryRun=false` | Execute the posted definition; dry-run by default; returns 202 |
| POST | `/api/hex/sites/{site}/automations/{name}/run?dryRun=true` | Execute a deployed automation; live by default; returns 202 |
| GET | `/api/hex/sites/{site}/automations/{name}/runs?limit=` | Recent runs, newest first |
| GET | `/api/hex/sites/{site}/automation-runs/{id}` | Poll until `status` is not `running` |

API deployment supplies the script source in the definition's `script` object:
`{ "file": "report.js", "source": "export default () => 42;" }`. `file` is an
optional diagnostic label on the API; it never causes server-side file access.

## Scheduling

Hosts call `server.RunBackground(ctx)` once per instance. The scheduler checks
immediately, then every 30 seconds, finding up to 20 due automations and reserving
shared run capacity before claiming each occurrence. Atomic claims compare the
stored revision and expected `nextRun` before advancing it, so multiple instances
sharing a store do not admit the same occurrence twice.

Replacement preserves the authoritative current `nextRun` when `schedule`,
`timezone` and `disabled` are unchanged, including a zero next run. Script or
description edits preserve schedule position but assign fresh, non-reusable
revisions to invalidate stale snapshots. Already claimed work may finish using
its claimed source. Memory uses a mutex; PostgreSQL serializes replacements and
claims with a transaction-scoped site advisory lock and row locks.

Missed occurrences execute once when scheduling resumes, without replaying every
missed time. Execution is in-process, with no automatic retry or restart recovery;
an interrupted persisted run remains `running`. Manual runs do not move the
schedule, and overlapping runs of the same automation are possible. Shutdown
stops scheduling and waits for admitted runs to finish.

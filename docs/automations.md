# Automations

Automations let an app run work on a schedule or on demand without deploying server code: a weekly report posted to Slack, birthday greetings, a nightly sync into the app's documents. An automation is JSON — in the app's `hex.json` or one file per automation under `automations/` — listing steps that call integration endpoints, run the site's actions, ask a model and read or save the site's own documents. Templates pass values between steps. App code never runs on the server.

```json
{
  "name": "weekly-sdi-report",
  "description": "Post last week's defect index to #dev.",
  "schedule": "0 8 * * MON",
  "timezone": "Europe/Oslo",
  "steps": [
    { "id": "counts", "call": "sentry.issue-counts", "input": { "statsPeriod": "7d" } },
    {
      "id": "summary",
      "ai": {
        "model": "claude-opus-5-5",
        "prompt": "Summarize this week's defects in three bullet points for developers: {{ steps.counts.output | json }}"
      }
    },
    {
      "id": "post",
      "if": "steps.counts.output.total | gt(0)",
      "call": "slack.post-message",
      "input": { "channel": "#dev", "text": "*SDI week {{ now.week }}*\n{{ steps.summary.output.text }}" }
    },
    { "id": "archive", "save": { "collection": "reports", "id": "week-{{ now.weekYear }}-{{ now.week }}", "data": { "counts": "{{ steps.counts.output }}" } } }
  ]
}
```

## Definitions

| Field | Meaning |
| --- | --- |
| `name` | Letters, digits, `-` and `_`; unique per site. In `automations/<name>.json` it defaults to the file name |
| `schedule` | Five-field cron (`minute hour day month weekday`) or `@hourly`, `@daily`, `@weekly`, `@monthly`. Without it the automation only runs when triggered. Schedules may not run more often than every five minutes |
| `timezone` | IANA time zone for the schedule and `now`, UTC by default |
| `disabled` | Keeps the automation without scheduling it |
| `steps` | 1–20 steps, run in order; the run stops at the first failing step |

Each step has an `id` and exactly one action:

| Step | Does | Output |
| --- | --- | --- |
| `call` + `input` | Calls `<integration>.<endpoint>` | The endpoint's result |
| `action` + `input` | Runs one of the site's [actions](agents.md) as an owner | The action's result |
| `ai` | `model`, `prompt`, optional `system`, `maxTokens`, `thinking` and `tools` (integration tools) | `{ text, stopReason, usage }` |
| `query` | Up to `limit` (default 100, max 1000) documents of `collection` whose top-level fields equal every `where` value | `[{ id, data }]` |
| `save` | Creates a document in `collection`, or replaces the one with `id` | `{ id, collection }` |

`if` skips the step when its template is falsy (`false`, `null`, `""`, `0`, empty list or object). `forEach` repeats the step for each item of a list (at most 100), exposing `item` and `index`; the step's output is then the list of outputs.

## Templates

Strings may contain `{{ expression }}`. A string that is exactly one placeholder becomes the expression's JSON value (a number stays a number, a list stays a list); otherwise placeholders are replaced by their text. `if` and `forEach` may omit the braces.

Values: `steps.<id>.output…`, `steps.<id>.status`, `item`, `index`, `site`, `automation`, `run.id`, `run.trigger`, `run.dryRun`, literals (`"text"`, `3`, `true`, `null`), and `now` in the automation's time zone: `iso`, `date`, `time`, `year`, `month`, `day`, `monthDay` (`MM-DD`), `weekday`, `week`, `weekYear`, `yesterday`, `weekStart`, `previousWeekStart`, `previousWeekEnd`, `unix`. Paths support list indexes (`items[0].title`); missing paths are `null`.

Filters, applied left to right with `|`: `length`, `json`, `join(", ")`, `default("x")`, `first`, `last`, `limit(n)`, `upper`, `lower`, `truncate(n)`, `map("field")`, `where("field", value)` (or `where("field")` for truthy), `sum` / `sum("field")`, `round(digits)`, `eq(v)`, `ne(v)`, `gt(n)`, `lt(n)`, `not`, `date("DD.MM.YYYY")`.

```json
{ "id": "today", "query": { "collection": "people", "where": { "birthday": "{{ now.monthDay }}" } } },
{ "id": "greet", "forEach": "steps.today.output", "call": "slack.post-message",
  "input": { "channel": "#general", "text": ":tada: Happy birthday {{ item.data.name }}!" } }
```

## Identity and permissions

Automations run as their **site**, never as a person. Integration grants name them with `site:<name>` (or `*`); integrations that require approval need the site's approval; connected accounts are unavailable. Within the site they act as an owner: they can run its actions and read and write its documents, which record `createdBy` as `automation:<name>`. The platform admin therefore decides which integrations each site's automations may use, independently of who published them.

## Deploying and testing

Publishing sends the project's automations and replaces the site's set (`[]` removes them all). Projects without any automation definitions leave existing ones unchanged. Owners can also manage them without republishing:

```sh
hex automations test weekly-sdi-report     # runs the local definition as a dry run
hex automations deploy                     # replaces the site's automations
hex automations list
hex automations run weekly-sdi-report      # run now
hex automations runs weekly-sdi-report
```

In the portal, a site's **Automations** tab (owners and admins) shows each automation's schedule in words, its next three runs in its time zone, its steps and its last result, with **Test** (a dry run), **Run now** and the run history, including each step's output. The portal does not edit definitions.

**Dry runs** perform reads, queries and AI steps but skip write endpoints, actions and saves, recording the input they would have used. Every run records each step's status, duration, error and output (truncated to 16 KiB); the latest 50 runs per automation are kept. Unpublishing a site removes its automations.

| Method | Path | Result |
| --- | --- | --- |
| GET/PUT | `/api/hex/sites/{site}/automations` | Owners: list with next and last run / replace the set |
| POST | `/api/hex/sites/{site}/automations/test?dryRun=false` | Run the posted definition (dry run by default) → run (202) |
| POST | `/api/hex/sites/{site}/automations/{name}/run?dryRun=true` | Run a deployed automation now → run (202) |
| GET | `/api/hex/sites/{site}/automations/{name}/runs?limit=` | Recent runs, newest first |
| GET | `/api/hex/sites/{site}/automation-runs/{id}` | One run; poll until `status` is not `running` |

## Scheduling

Hosts call `server.RunBackground(ctx)` once per instance. Every 30 seconds it finds due automations and claims each occurrence atomically in the `AutomationStore` (`ClaimAutomation` compares and advances `nextRun`), so several instances never run the same occurrence twice. At most four runs execute concurrently per instance; a run may take ten minutes. Occurrences missed while no instance was running run once when the scheduler resumes and are not caught up. A run interrupted by a restart stays recorded as `running`.

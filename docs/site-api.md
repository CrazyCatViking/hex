# Site APIs

Site APIs use the existing action registry and permission policies. Calls stay
on the source site's origin; changing `site` in a URL does not bypass hostname
isolation. Connections are local to one Hex runtime/workspace. No browser site
credentials or arbitrary destination URLs are accepted.

## Explicit execution modes

An `action-consumer` grant can use operation `user` or `site`:

- `user` permits a connection on behalf of a person. The person's target-side
  action and data grants still apply. There is no fallback to site authority.
- `site` authorizes the source site's persistent identity to execute one declared
  action. It supplies action-local data authority, including optional `where`,
  `check`, `fields` and group-binding scopes. It does not grant direct database
  access. All-record access means all records eligible for that action, not every
  operation on the collection. Restricted reads filter before pagination.

Site mode requires a declared action; arbitrary platform handlers cannot receive
automatically synthesized data authority. Automations use the source site identity,
not the triggering human. Browser calls always use user mode.

The calling site needs no grant of its own. Its code already chooses which
targets it calls, so only the target's consent is checked. `site-action` grants
from earlier versions are still accepted in policies but have no effect.

Existing `action-consumer/run` grants retain their original behavior. They are
not automatically replaced. New mode grants are additive; revoke legacy grants explicitly if they
would otherwise continue allowing a connection.

## Export an action

Add `"exported": true, "version": 1` to a declared action or Go action definition.
Existing actions are not exported. Consumers pin a positive version. Publish a
new action name for a breaking version that must coexist with the old contract.
Changing the version of an existing name makes old calls fail closed.
Publishing rejects changes to the input/output schema or read/mutation kind of
an existing exported version. Use a new version for contract changes.

Declarative reads use `operation: "get"` or `"list"`, `collection`, `input`,
`output` (JSON Schema), and `select` (1–64 top-level data fields). They enforce
the caller's collection read policy before projecting fields. Get requires the
configured `idField` in the input schema and returns `{id,data}`. List takes
`after` and `limit` (1–100), returning `{items:[{id,data}],after}`; continue until
an empty page. Read actions require caller authorization. All action outputs
are limited to 1 MiB.

## Connect sites

1. On source A, read `GET /api/sites/A/site-api/identity` (SDK
   `hex.sites.identity()`). The returned `id` is an opaque `site:<id>` principal.
   It is persistent and survives unpublishing, like the site's ownership policy.
   Namespace reassignment must rotate that identity and remove old grants.
2. In B grant `kind: "action-consumer"`, `resource: "action-name"`,
   `operations: ["user"]` and/or `["site"]`,
   `subjects: {"principals":["site:<A-id>"]}`. In the portal, open B's
   **Overview → Actions & events**, choose the action's **Details & access** and
   add A under **Other apps**.
3. In B grant the effective actor `action/run` and the collection permissions
   required by the action. Interactive calls use the user, never A's identity.
   API admission does not grant permission to visit B's pages or manage B.

B's policy is checked on every invocation, including cached results. Use normal
access-policy publishing/reconciliation. `site:*` actors do not match `signedIn`.

```ts
const api = hex.sites.api("inventory", { version: 1 });
const contracts = await api.list(); // only currently authorized exports
const result = await api.call("reserve-stock", { productId: "p123", quantity: 2 }, {
  idempotencyKey: reservationId,
});
```

The transport is `POST /api/sites/A/site-api/call` with
`{target,action,version,input,idempotencyKey}`. Unknown request fields are rejected.
Discovery is `GET /api/sites/A/site-api/actions/B`; it lists the exports A may
call in user mode, or with `?mode=site` those A's automations may call. Actions
B has not exported are omitted.

## Discover APIs

`GET /api/hex/workspace-apis` (CLI `hex actions catalog [--query text]`) lists
exported actions and events across the workspace as JSON: `site`, `kind`, `name`,
`version`, `description`, `sharing` (`workspace` or `restricted`),
`workspaceModes`, `owners`, `owned`, `readOnly` and the input/output schemas. It
applies the same visibility rules as the **Workspace APIs** page below.
`hex actions available --site B [--from A] [--as-site]` shows what A can call on
B now; `--from` defaults to the current project. Neither command requests
access; contact the listed owners.

## Typed consumers

`hex actions codegen --site inventory --out src/inventory.ts` generates input
and output types plus version-pinned wrappers from visible exported contracts.
Add `--from <consumer>` (and `--as-site` for automations) to generate only the
contracts that consumer may call.
Use `typedSiteAPI(hex.sites)` from the generated module. Mutation methods require
an idempotency key at compile time. `--catalog actions.json` supports offline
generation from saved `hex actions list` JSON; `--check --out ...` detects drift
in CI. Exclude generated files from formatters.

## Automations

Scripts call `await hex.sites.call("inventory", "reserve-stock", input,
{version: 1, idempotencyKey: businessOperationId})`. Any automation of the
source site may make the call. The target's `action-consumer`, `action`
and required collection grants select the source's opaque `site:<id>` identity.
Automations never inherit the deploying or triggering user's target privileges.
Dry runs authorize and validate calls but skip mutations; read-only calls execute.
Calls participate in the existing timeout, operation-count, output-size and
operation-history limits. Use a stable business-operation key when subsequent
runs must deduplicate; a new run ID intentionally denotes a new operation.

## Retry semantics and storage

Mutations require a key of 1–128 bytes. Keys are scoped to source, effective
actor, target, action and version and bound to the input bytes. Successful
results persist in the configured database and are reused across restarts.
Concurrent, changed-input and uncertain retries return 409. A handler can commit
before failing or losing its result; Hex does not pretend such failures are
safe to retry. Go handlers receive `InvocationID` for their own deduplication.

Invocation records contain actor, source, target, version, timestamps and status.
Inspect a known ID through `GET /api/sites/A/site-api/invocations/<id>` or
`hex.sites.invocation(id)`. Only the actor and the source site's owners can read
a record. Server logs include invocation IDs. No cross-site transaction is
promised.

Internal records use the inaccessible `.hex-system` database namespace and
provider-atomic guarded mutations. PostgreSQL persists them; the development
memory provider only retains them for its process lifetime. Records are retained
until operator archival; do not prune mutation deduplication records while keys
may still be retried.

Cross-site calls, event publications and workflow starts each have a default
120-operation/source-site/minute admission limit shared across replicas. Hosts
can configure `Config.SiteOperationsPerMinute`. HTTP throttling returns 429 and
`Retry-After`. Cross-site mutation results are limited to 512 KiB to leave room
for the durable invocation envelope.
# Workspace-public APIs

In the portal, open **Your sites → your site → Overview → Actions & events**,
choose an exported action's **Details & access** and set **Other apps → Available
to** to **All workspace apps**. Save access to apply the selection. This grants
the incoming site connection to every site in the workspace, including site
automations. Choosing selected apps or no other apps keeps access to explicit
grants.

The **Workspace APIs** navigation item lists exported actions and durable events,
their sharing status, usage and site owners. It includes workspace-public APIs
even when the site's pages are private. Restricted APIs are listed only for
people admitted to the site; sites marked undiscoverable are omitted. Owners can
go directly from a catalog card to the site's **Actions & events**. Access guidance identifies which
owner to contact; it does not submit an approval request automatically.

The policy audience is `"subjects": {"workspace": true}`. It matches only
authenticated calls brokered inside this workspace, not direct browser requests,
anonymous visitors, local automations or site admission. The source site must
have an existing policy. Schema validation, version pinning, record constraints,
rate limits and idempotency continue to apply. Revocations affect subsequent
requests and are preserved by the normal three-way publication merge.

Servers supporting this audience advertise `capabilities.workspaceAPISharing`.
Upgrade the Hex runtime before publishing policies that use it.

For example, these grants expose an exported `submit` action:

```json
{
  "workspace-connection": {
    "kind": "action-consumer", "resource": "submit", "operations": ["run"],
    "subjects": {"workspace": true}
  },
  "workspace-execution": {
    "kind": "action", "resource": "submit", "operations": ["run"],
    "subjects": {"workspace": true}
  }
}
```

Action execution and underlying data authority remain distinct. An action using
`authorization: "action"` executes its declared, constrained effects with its
action grant. Caller-authorized actions (including declared reads) additionally
need the relevant collection grants; those can also select `workspace: true`
through **Developer tools → Add a rule outside permission groups**. This does
not open direct collection access. Platform handlers may enforce further rules.
The catalog therefore describes connection sharing separately from data access.

The simple sharing selector owns only its dedicated grants. If a custom rule
(such as a wildcard) also opens the resource, restricting it reports that rule
must be changed rather than silently claiming access was revoked.

# Site APIs

Site APIs use the existing action registry and permission policies. Calls stay
on the source site's origin; changing `site` in a URL does not bypass hostname
isolation. Connections are local to one Hex runtime/workspace. No browser site
credentials or arbitrary destination URLs are accepted.

## Export an action

Add `"exported": true, "version": 1` to a declared action or Go action definition.
Existing actions are not exported. Consumers pin a positive version. Publish a
new action name for a breaking version that must coexist with the old contract.
Changing the version of an existing name makes old calls fail closed.

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
2. In A's v2 access policy grant `kind: "site-action"`,
   `resource: "B.action-name"`, `operations: ["run"]` to the users or automations
   allowed to use the connection. This is A's explicit dependency declaration.
3. In B grant `kind: "action-consumer"`, `resource: "action-name"`,
   `operations: ["run"]`, `subjects: {"principals":["site:<A-id>"]}`.
4. In B grant the effective actor `action/run` and the collection permissions
   required by the action. Interactive calls use the user, never A's identity.
   API admission does not grant permission to visit B's pages or manage B.

Both policies are checked on every invocation, including cached results.
Use normal access-policy publishing/reconciliation; declaring an outbound grant
does not grant inbound permission. `site:*` actors do not match `signedIn`.

```ts
const api = hex.sites.api("inventory", { version: 1 });
const contracts = await api.list(); // only currently authorized exports
const result = await api.call("reserve-stock", { productId: "p123", quantity: 2 }, {
  idempotencyKey: reservationId,
});
```

The transport is `POST /api/sites/A/site-api/call` with
`{target,action,version,input,idempotencyKey}`. Unknown request fields are rejected.
Discovery is `GET /api/sites/A/site-api/actions/B`.

## Retry semantics and storage

Mutations require a key of 1–128 bytes. Keys are scoped to source, effective
actor, target, action and version and bound to the input bytes. Successful
results persist in the configured database and are reused across restarts.
Concurrent, changed-input and uncertain retries return 409. A handler can commit
before failing or losing its result; Hex does not pretend such failures are
safe to retry. Go handlers receive `InvocationID` for their own deduplication.

Invocation records contain actor, source, target, version, timestamps and status.
Inspect a known ID through `GET /api/sites/A/site-api/invocations/<id>` or
`hex.sites.invocation(id)`. The actor or source owner must still have outbound
permission. Server logs include invocation IDs. No cross-site transaction is
promised.

Internal records use the inaccessible `.hex-system` database namespace and
provider-atomic guarded mutations. PostgreSQL persists them; the development
memory provider only retains them for its process lifetime. Records are retained
until operator archival; do not prune mutation deduplication records while keys
may still be retried.

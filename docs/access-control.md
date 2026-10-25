# Identity and application permissions

Hex separates three decisions:

1. **Site admission** (`admission`): who may enter the site's API namespace.
2. **Site management** (`owners` and platform administrators): who may publish and change permissions.
3. **Application authorization** (`roles`, `bindings`, `grants`): which operations callers may perform.

Version 2 policies deny application access unless a grant permits it. Owners and platform administrators do **not** implicitly bypass application rules. They can still change the policy, so this is not a security boundary against policy administrators.

## Identity and principals

The gateway authenticates the caller. The configured resolver supplies a stable ID, optional display name/email, groups and identity-provider roles. Read it with `hex whoami` or `hex.identity()`. Never match access against email or display-name claims.

| Principal | Exact, case-sensitive match |
| --- | --- |
| `user:<id>` | Stable identity ID |
| `group:<id>` | Identity-provider group claim |
| `role:<value>` | Identity-provider role claim, not an application role |
| `automation:<name>` | That site's named automation; only matches the automation provider |

Principal values are opaque printable ASCII strings, up to 256 characters. Only the first reserved prefix identifies the claim kind. Easy Auth canonicalizes Entra user/group GUIDs to lowercase; opaque subjects and role values retain their spelling. Application roles are site-defined names, assigned independently of identity-provider roles.

## Complete example

Put this policy under `access` in `hex.json`. IDs below are illustrative, not real directory IDs.

```json
{
  "version": 2,
  "owners": ["user:publisher-id"],
  "admission": { "signedIn": true },
  "roles": {
    "employee": { "description": "Submit your own expenses", "signedIn": true },
    "approver": { "description": "Review expenses within assigned projects" }
  },
  "bindings": {
    "project-a-managers": {
      "principal": "group:project-a-managers-id",
      "role": "approver",
      "scope": { "kind": "project", "id": "A" }
    }
  },
  "grants": {
    "site-pages": {
      "kind": "path", "resource": "*", "operations": ["read"],
      "subjects": { "signedIn": true }
    },
    "own-expenses": {
      "kind": "collection", "resource": "expenses", "operations": ["read"],
      "subjects": { "roles": ["employee"] },
      "where": { "field": "createdBy", "op": "eq", "value": "$actor.id" }
    },
    "create-drafts": {
      "kind": "collection", "resource": "expenses", "operations": ["create"],
      "subjects": { "roles": ["employee"] },
      "fields": ["amount", "projectId", "status"],
      "check": { "field": "data.status", "op": "eq", "value": "draft" }
    },
    "edit-own-drafts": {
      "kind": "collection", "resource": "expenses", "operations": ["update"],
      "subjects": { "roles": ["employee"] },
      "fields": ["amount"],
      "where": {
        "all": [
          { "field": "createdBy", "op": "eq", "value": "$actor.id" },
          { "field": "data.status", "op": "eq", "value": "draft" }
        ]
      }
    },
    "project-review": {
      "kind": "collection", "resource": "expenses", "operations": ["read"],
      "subjects": { "roles": ["approver"] },
      "scope": { "kind": "project", "field": "data.projectId" }
    }
  }
}
```

There is no implicit role hierarchy. `approver` does not inherit `employee`. A caller may hold several roles, including different scoped roles in different projects. An unscoped binding holds that role across all scopes. A role's `signedIn: true` explicitly assigns it to every signed-in visitor, but never to an automation.

Roles can also have an optional `name` (up to 512 bytes) for display in the portal. The role map key remains the stable identifier used by app code, bindings, grants, and reconciliation; changing the display name never changes authorization.

## Grant semantics

| Kind | Resource | Operations |
| --- | --- | --- |
| `collection` | Exact collection or `*` | `read`, `create`, `update`, `delete` |
| `file` | File-key prefix or `*` | `read`, `upload`, `delete` |
| `channel` | Exact channel or `*` | `read`, `send` |
| `path` | Absolute URL prefix or `*` | `read` |
| `action` | Exact action name or `*` | `run` |
| `integration` | Qualified endpoint, `name.*`, or `*` | `read` (non-mutating endpoint), `write` (mutating endpoint) |

Subjects are an OR of `principals`, unscoped application `roles`, `signedIn`, and `owners`. Admission uses the same subjects; a scoped role binding can admit the caller but cannot satisfy an unscoped data grant. A scoped grant must use only role subjects and names the record field carrying its scope ID. Site admission is required even when a data grant names a caller directly.

Grants are additive, but **one complete grant must authorize a mutation**: fields from one grant cannot be combined with another grant's state constraints. No grant means denial. A broad grant does not become restricted merely because a narrower grant also exists. Use `except` selectors to exclude resources from a broad grant.

File prefixes match the exact key and descendants (a trailing slash matches descendants). Path matching uses URL prefixes, including `/admin/` covering `/admin`. `Config.PathCaseInsensitive` applies to path selectors and exclusions on case-insensitive static storage. Paths protect separately served assets, not hash routes or data behind public bundles.

### Record conditions and protected fields

Collection and declared-action grants support:

- `where`: existing record requirement (proposed record for a create).
- `check`: proposed record requirement (existing record for a delete).
- `fields`: top-level fields that may change or be supplied on create. Omitted/null means unrestricted; `[]` means no fields may change. Deleting a record is governed by the explicit delete operation, not a field allowlist.
- `scope`: binding scope IDs must match the record field before and after a mutation. Scope checks cannot be bypassed by changing the project field.

Conditions use a leaf `{field, op, value}` or a nonempty `{all:[...]}` / `{any:[...]}` tree. Supported comparisons: `eq`, `ne`, `in` (value array), `contains` (record array contains the exact JSON value). Missing fields never match, even `ne`; JSON null is distinct from missing. Field paths are `data.<field>[.<nested-field>...]`, `id`, or server-recorded `createdBy`. A comparison value equal to `"$actor.id"` resolves to the caller's stable ID. Conditions are bounded to 8 levels, 128 nodes and 4 KiB per comparison value.

`PUT` selects `create` or `update` from the actual record existence under the storage lock. Replacements preserve `createdBy`. Lists filter in storage **before** sorting, pagination and limit. Unreadable documents return 404; forbidden writes return 403. PostgreSQL uses parameterized JSONB filters and transactional mutation locks, including absent IDs; memory uses the same evaluator under its mutex. Custom providers must implement `GuardedDatabase`; unsupported conditional lists and mutations fail closed.

## Constrained business actions

Declared actions default to `authorization: "caller"`: callers need an `action/run` grant **and** a complete collection-operation grant. To expose a narrow workflow without general collection write access, publish an action like:

```json
{
  "name": "approve", "description": "Approve an expense submitted by someone else",
  "operation": "update", "collection": "expenses", "authorization": "action",
  "input": {
    "type": "object", "properties": { "id": { "type": "string" } },
    "required": ["id"], "additionalProperties": false
  },
  "where": {
    "all": [
      { "field": "data.status", "op": "eq", "value": "submitted" },
      { "field": "createdBy", "op": "ne", "value": "$actor.id" }
    ]
  },
  "set": { "status": "approved", "approvedBy": "$actor.id", "approvedAt": "$now" }
}
```

Add an `action/run` grant for `approve` with subjects `approver` and scope `{kind:"project",field:"data.projectId"}`. The action accepts no writable input fields except its ID unless `fields` explicitly lists them. Fixed `set` effects overwrite allowed input and support `$actor.id` and `$now`. Action grant conditions, definition constraints, and the update execute under one storage lock. Calling the database directly does not inherit action authority.

Go-registered handlers remain trusted server code. Conditional action grants do not authorize them automatically, since Hex cannot infer which record an arbitrary handler targets. `ActionContext.CollectionWriteOptions` only represents unrestricted or creator-only CRUD; use `ActionContext.MutateDocument` for conditional writes. Integration grants further restrict the caller's platform permissions; platform integration permissions and site approvals are still required and cannot be granted by a site policy.

## Publishing and portal reconciliation

The server atomically stores the merged live policy and the incoming local baseline:

- **Base**: previously accepted local `access` section.
- **Remote**: current deployed policy, including portal changes.
- **Local**: incoming `access` section.

Only Base→Local changes apply to Remote. Unchanged local assignments never restore portal revocations or delete portal additions. Roles, bindings and grants have stable map IDs, not array positions or display names. A binding contains one principal/role/scope assignment; keep its ID stable when editing it. Differing changes or delete-versus-edit to the same ID conflict (409). Independent IDs merge. Cross-references and duplicate assignments are validated after merging. Opening a role to all signed-in users concurrently with changing that role's assignments also conflicts.

The baseline is always the **incoming local document**, never the merged live result. Publication warnings report preserved remote differences. Both apps and artifacts use this reconciliation. Publishing without `access` leaves both live permissions and the baseline untouched.

Missing baselines are not guessed. Explicitly establish one without changing live access:

```sh
hex access show my-app
hex access reconcile my-app --file local-policy.json
hex publish
```

`local-policy.json` is the intended Base policy (the access object, not all of hex.json). Existing differences are thereafter treated as portal edits. To deliberately discard remote differences, use `hex publish --replace-access`; this is never implicit. Review its effects first.

### Managing permissions in the portal

Open a site's **Sharing** tab on the platform domain:

1. **Site access** controls admission independently of app data permissions. Add people or directory groups with the searchable picker. Site ownership is managed separately.
2. **Who can do what?** shows a checkbox column per site-owned permission group. The default row applies groups to all admitted signed-in people, not automations. The owner row shows explicit owner-principal assignments, without assuming all owners already have app permissions. Only explicit memberships and intentionally added rows appear, not the whole directory.
3. Click a group heading to create/edit its name, description, and **resource rules**. Each rule selects a resource type, app resource identifier, operations, optional record conditions, and writable fields. Resource suggestions come from the existing policy; they are not a complete automatically discovered app schema. Adding a grant does not create a collection or implement an app action.
4. **Save changes** applies the staged table and rule edits together. **Apply** in a dialog updates the draft only. Validation and stale-form errors preserve the edited controls; incompatible edits are not guessed. Technical full-policy JSON remains a separate collapsed developer tool, not a prerequisite for routine management.

New groups offer explicit assignments to the listed owners by default. Existing policies and owner permissions are never expanded merely by opening the editor. These memberships do not automatically include platform administrators or future owners; record and action restrictions still apply. Mixed or scoped owner memberships remain visible individually.

Membership limits are optional and use the types defined by the app's scoped rules; Hex has no built-in project A/B model. A limited binding satisfies only matching scoped rules, not unscoped page grants. An unrestricted binding or signed-in role applies across all scopes, while still enforcing record conditions and field limits.

The matrix shows **explicit assignments**, not a general effective-access evaluator. Inherited defaults are marked and cannot be denied per person. The platform does not know other people's current directory claims; directory memberships and direct/owner resource grants can independently allow access. **Access granted outside this table** summarizes these existing app grants by audience (for example, one owner entry rather than six wildcard rule cards). **Review access** opens their individual rules, operations, and limits. Shared rules appear with every recipient, and removing a rule from one permission group retains its other audiences. The summary is hidden when no outside app grants exist. Page rules are separate, under **Review page access** in the site-access card. Removing a row or unticking a group does not revoke unrelated grants or site entry.

The Sharing screen uses the approved matrix prototype's light and dark palettes, separate admission/table cards, default and owner row treatments, compact membership chips, checkbox-based group rule editor, and pending-change save bar. **What does each permission group allow?** provides a readable guide below the table. Real app resource rules replace the prototype's fictional expense-specific presets; no sample capability is assumed to exist on a real app.

Edits are narrow transformations of the original form policy. The server retains untouched custom rules, exact condition values and stable IDs, merges independent remote changes, and preserves the existing publication baseline. Stale edits to the same principal/group membership, changes to owners during owner assignments, and concurrent page-boundary or general-admission replacements fail without a partial save. Unchanged local republication continues to preserve portal additions and revocations.

## Automatic migration

Existing deployed policies migrate atomically on first policy use; no opt-in or permanent second authorization engine is required. The stored historical publication baseline migrates with it, independently of live changes. Migration uses stable deterministic IDs and explicit compatibility grants to preserve:

- Existing viewer/editor audiences and creator-only records.
- Owner/admin bypass as **explicit grants**.
- Collection/channel overrides, file longest-prefix rules and path fallback using exclusions; broad fallback grants cannot bypass migrated restrictions.
- Existing declared/registered action audiences and existing automation names. New automations need explicit grants, e.g. `automation:weekly-report`; they are not implicit owners.

Sites without any stored policy retain their historical open behavior. Newly claimed sites without an `access` section are private to owners, with no default data authority. Read the migrated policy with `hex access show`, copy/review it for local source configuration, and adopt cleaner defaults when ready.

**Legacy publications are rejected before claiming ownership, changing policy, or modifying files.** An access section must declare `version: 2` and use the new schema. Merely setting version 2 while retaining even empty legacy `viewers`, `editors`, `paths`, `collections`, `files` or `channels` is rejected with migration guidance. Unknown fields and malformed policies also fail. The direct management API still translates legacy replacement requests for older tooling; it never stores a legacy policy.

PostgreSQL stores v2 policies in a fail-closed envelope: older binaries reject its incompatible legacy `owners` field instead of interpreting absent lists as open access. Back up policies before upgrading. Older binaries cannot serve migrated sites and rollback needs a compatible release; do not run mixed-version replicas as a long-term deployment strategy. No database table migration is needed for v2 JSON policies.

The CLI also checks live `capabilities.permissionVersion` before sending a version 2 policy. Upgrade the server first: an older server might otherwise ignore new fields and interpret absent legacy lists as open access.

### Version mismatch guidance

- **Legacy publication configuration:** the CLI and server reject it with instructions to upgrade an outdated CLI using `hex update` (or rerun the platform installer), then retrieve the migrated policy with `hex access show <site>` and update `hex.json`. A CLI upgrade alone does not migrate local configuration.
- **New CLI, old server:** policy writes are blocked before sending the policy. The error directs the platform operator to upgrade the Hex server/workspace runtime; updating the CLI alone is not sufficient. Missing permission-version support or a missing capabilities endpoint is unsupported, not permission to fall back to legacy writes.
- **New browser SDK, old server:** `hex.can()` checks live permission capabilities before every call and throws `HexError` status 501 with server/runtime upgrade instructions when v2 is unsupported. The record-check request is not sent. Other failures, including 401, 403 and server outages, retain their original errors rather than suggesting an upgrade.
- **Old browser SDK, new server:** existing operations remain server-authorized, but older UI code may not understand application roles or `conditional` summaries. Old SDKs have no automatic version warning. Update `@crazycatviking/hex` in the app, commit its lockfile, rebuild, and republish to adopt the new APIs. Updating the CLI does not update an app's bundled SDK.

## Managing policies and inspecting access

```sh
hex whoami
hex access show my-app
hex access set my-app --file policy.json
hex access check my-app
hex access reconcile my-app --file local-policy.json
```

`access set` explicitly replaces live policy and retains the last publication baseline. Omitted owners keep existing owners, and changes must retain the caller's ability to manage the site. Legacy convenience flags generate explicit compatibility policies. `access clear` removes the policy and opens the site to all signed-in users; it is a destructive administrative operation.

Management endpoints require ownership: `GET/PUT/DELETE /api/hex/sites/{site}/access`, `POST /api/hex/sites/{site}/access/baseline`. `hex.permissions()` returns version, application roles, management role, per-operation grants (`all`, `own`, `conditional`, `none`), and compatibility `read`/`write` summaries. A `conditional` grant is not unrestricted access.

`await hex.can({collection:"expenses",operation:"update",id:"item-id",data:nextRecord})` asks whether the current caller could perform that exact operation. It calls `POST /api/sites/{site}/permissions/check`. Responses are advisory, not authorization tokens: real operations always recheck. Permission descriptions do not expose policy membership to ordinary viewers.

## Storage, enforcement and trust model

Policies reside in the access store, never served app files. `AccessStore.UpdateSiteAccess` must serialize authorization and all mutations across replicas. PostgreSQL uses transaction-scoped advisory and row locks; memory uses a mutex. Each request reads the current policy without a replica-local cache. Already authorized requests and established WebSockets are not retroactively revoked.

Static assets, app APIs, discovery, portal data browsing, actions and automation data operations apply these rules. App hostnames are restricted to their own namespace; management APIs stay on the platform host. Only owners publish/unpublish. `HEX_PUBLISHER_GROUPS` limits new name claims. `HEX_ADMIN_GROUPS` supplies platform policy administrators; configure recovery administrators.

The gateway must authenticate requests and strip spoofed identity headers. Never expose the Easy Auth resolver directly to untrusted clients. Store outages and invalid policies fail closed. Strong protection against policy-editing owners needs platform governance, not an application role.

## Entra ID configuration and local development

Enable `HEX_IDENTITY_PROVIDER=easyauth` behind Easy Auth. Emit assigned group claims (`groupMembershipClaims: ApplicationGroup`) or identity-provider app roles. Group/role membership changes take effect after claims are refreshed; removing a directory membership does not instantly invalidate an existing session. Use exact stable IDs, not email addresses.

`hex dev` supplies identity `local-dev` and local administrator authority. That authority manages policy but does not imply v2 data grants. Policies in the in-memory store reset on restart. Custom persistent stores must preserve the baseline and enforce atomic updates; custom document stores must participate in the guarded mutation protocol for every writer.

See [the legacy mapping reference](access-control-legacy.md) for historical semantics only, and [publishing](publishing.md) for upload behavior.

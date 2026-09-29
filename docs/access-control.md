# Identity and site access control

Hex decides who may view, edit and publish each site using the identity that already authenticates the platform. Hex never validates identity-provider tokens or manages users itself: the hosting gateway authenticates every request, a configured identity resolver reads what the gateway forwarded, and Hex evaluates the site's access policy against that identity.

Access control is optional. Without an identity resolver and access store, every authenticated user can view and edit every site.

## Concepts

An **identity** is what the gateway resolved for the caller: a stable ID, a display name (the principal name, typically an email address), and the group and role values the identity provider emitted. Apps read it from `GET /api/hex/me` or `hex.identity()`, and operators from `hex whoami`.

A **principal** is a typed value in a policy:

| Principal | Matches |
| --- | --- |
| `user:<value>` | The caller's identity ID or principal name, case-insensitively |
| `group:<value>` | One of the caller's group claims (Entra group object IDs) |
| `role:<value>` | One of the caller's role claims (app role values) |

Values are 1–128 letters, digits, `@`, `.`, `_` or `-`, starting with a letter or digit. Configured `HEX_ADMIN_GROUPS` values may be untyped; an untyped value matches the ID, any group or any role.

A **site access policy** is a server-owned record per site name. Each site has one of four roles for a caller:

| Role | Who | May |
| --- | --- | --- |
| Owner | Listed in `owners`, or a platform admin | Manage the policy, publish and unpublish, pass every rule |
| Editor | Listed in `editors`; everyone who can view when `editors` is empty | Write the site's data by default |
| Viewer | Listed in `viewers`; every signed-in user when `viewers` is empty | See the site and read its data by default |
| None | Anyone else | Nothing: 403 for the site's pages and API namespace, and the site is hidden from discovery |

Defaults keep sharing open:

- A site **without a policy** is open: every authenticated user can view and edit it, and only platform admins own it.
- Empty `viewers` lets every signed-in user view. Empty `editors` lets every viewer edit.
- Explicit editors can edit and view even when `viewers` is restricted to someone else.

**Platform admins** — `HEX_ADMIN_GROUPS` (or `Config.AdminGroups`) — own every site. Configure at least one admin group so squatted or orphaned policies can always be corrected.

## Complete example

```json
{
  "owners": ["user:alex@example.com", "group:6f1c1c7e-0000-4000-8000-000000000001"],
  "editors": ["group:6f1c1c7e-0000-4000-8000-000000000002"],
  "viewers": [],
  "paths": [
    { "prefix": "/admin/", "viewers": "owners" },
    { "prefix": "/admin/help/", "viewers": "viewers" }
  ],
  "collections": {
    "settings": { "read": "viewers", "write": "owners" },
    "audit": { "read": ["group:6f1c1c7e-0000-4000-8000-000000000003"], "write": "owners" },
    "drafts": { "read": "creator", "write": "creator" },
    "*": { "read": "viewers", "write": "editors" }
  },
  "files": {
    "exports/": { "read": "owners", "write": "owners" }
  },
  "channels": {
    "announcements": { "read": "viewers", "write": "owners" }
  }
}
```

Every signed-in user can use this site. Only the second group edits its general data, only owners open `/admin/` (except `/admin/help/`), everyone reads `settings` but only owners change it, one group reads the audit log, and each user sees only their own drafts.

## Audiences

Rules grant access to an **audience**: either a level name or an array of principals.

| Level | Grants |
| --- | --- |
| `viewers` | Everyone who can view the site |
| `editors` | Editors and owners |
| `owners` | Owners only |
| `creator` | Only documents the caller created (collections only) |

A principal array grants its members, provided they can view the site at all. Owners and admins pass every rule; callers who cannot view the site pass none.

## Path rules

`paths` restricts static assets under URL prefixes. Prefixes start with `/` and may contain letters, digits, `.`, `_`, `~`, `/` and `-` (no `..` or `//`); at most 32 rules. The longest matching prefix wins, matching ignores case (Azure Files resolves paths case-insensitively), and a prefix such as `/admin/` also covers the bare `/admin`. A path rule needs `viewers`, a level or principals; `creator` is not allowed. Paths without a matching rule use only the site-level decision.

NGINX sends the decoded, normalized request path to the authorization subrequest, so encoded or dot-segment variants of a protected path are checked as that path. Path rules protect separately served files only. An admin area must be its own HTML entry point, such as `admin/index.html` in a multi-page build, with admin-only assets under the same prefix. Hash routes inside a public page cannot be protected, and shared bundles remain readable. Protect the data behind an admin page with collection, file and channel rules.

## Data rules

`collections`, `files` and `channels` map names to `{ "read": <audience>, "write": <audience> }`. An unset `read` defaults to `viewers` and an unset `write` to `editors`. The `"*"` key is the rule for names without their own entry; without it, those defaults apply. At most 64 rules per map.

| Map | Keys | Read covers | Write covers |
| --- | --- | --- | --- |
| `collections` | Collection names | List and get | Create, replace, delete |
| `files` | File key prefixes; the longest match wins. `exports/` covers keys under it, `report.pdf` that key and keys under `report.pdf/` | Get; listings omit unreadable files | Upload, delete |
| `channels` | Channel names | Connecting to the WebSocket | Sending; a read-only subscriber that sends is disconnected with close code 1008 |

The `creator` level is available for collections only. The server records the creating caller's identity ID as the document's `createdBy` when it is first stored; later writes never change it and clients cannot set it. Under `read: creator`, lists contain only the caller's documents and other people's documents answer 404, so their IDs cannot be probed. Under `write: creator`, any viewer may create documents, and only their creator may replace or delete them (403 otherwise). Documents stored before creators were recorded have an empty `createdBy`, so only owners can manage them under a creator rule.

## Enforcement points

1. **Static assets.** The NGINX template sends an `auth_request` subrequest to `/api/hex/authz` for every site-asset request, with the validated site name in `X-Hex-Site` and the normalized path in `X-Hex-Path`. 204 serves the asset; 403 blocks it.
2. **Site APIs.** Every `/api/sites/{site}/...` route — files, documents, realtime upgrades — first requires the viewer role, then applies the matching data rule. The check keys on the path segment, not the hostname, so a restricted site's data cannot be read from another origin.
3. **Discovery.** `GET /api/sites`, the landing page and its statistics omit sites the caller cannot view.
4. **Publishing.** Only owners publish and unpublish. See [Publishing](publishing.md).

`GET /api/hex/sites/{site}/permissions` (`hex.permissions()` in the browser client, `hex access check` in the CLI) describes what the caller may do: `role`, `admin`, `publish`, each path rule with `allowed`, and `read`/`write` grants (`all`, `own` or `none`) for every configured collection, file prefix and channel plus `"*"`. Apps use it to show or hide links and controls; it is advisory, and every request is checked regardless.

The server caches policies in memory for 10 seconds per site; writes through the server invalidate their entry immediately, and the TTL bounds how long a change made elsewhere takes to apply.

## Managing policies

Keep the policy in the `access` section of the app's hex.json. `hex publish` sends it with the publication and the server applies it when the publication completes; the platform validates it before any file is uploaded.

```json
{
  "name": "team-dashboard",
  "access": {
    "viewers": ["group:<sales group object id>"],
    "paths": [{ "prefix": "/admin/", "viewers": "owners" }],
    "collections": { "settings": { "write": "owners" } }
  }
}
```

A policy without `owners` keeps the current owners, or makes the caller the owner of a new policy. Every stored policy must keep the caller able to manage it, so you cannot lock yourself out by accident. Without an `access` section, publishing leaves the existing policy unchanged.

**Claiming names.** The first publication of an unowned name creates a policy that makes the publisher its owner. `HEX_PUBLISHER_GROUPS` (or `Config.PublisherGroups`) limits who may claim new names; empty lets every signed-in user. Owners can always republish their sites, and admins can reassign squatted names.

The CLI manages policies directly:

```sh
hex whoami
hex access show my-app
hex access set my-app --owner user:alex@example.com --viewer group:<object-id>
hex access set my-app --file policy.json
hex access check my-app
hex access clear my-app
```

`hex access set` replaces the whole policy; flags cover owners, editors and viewers, and `--file` accepts the complete JSON shape above. `clear` removes the policy, opening the site to every signed-in user; the name is then unowned. The CLI authenticates like other API commands: `HEX_TOKEN`, or its saved browser sign-in for the platform (see [Publishing](publishing.md#authentication)).

The HTTP routes are `GET`/`PUT`/`DELETE /api/hex/sites/{site}/access`, registered when both an identity resolver and an access store are configured. Reading a policy requires owner or admin rights.

## Storage

Policies live in the platform database, never in the published site directory. The PostgreSQL provider stores them as JSON in `hex_site_policies`. `Migrate` converts entries from the former `hex_site_access` table (whose `groups` become `viewers`) and drops it in one transaction. Converted untyped values keep matching the ID, groups and roles; policies saved afterwards must use typed principals. The in-memory store is for development and tests.

With the reference executable, access control activates with the `easyauth` resolver only alongside the `postgres` database provider; the `static` resolver accepts the in-memory store for local experimentation.

## Entra ID configuration (Azure example)

Easy Auth injects the authenticated caller as `X-MS-CLIENT-PRINCIPAL` headers; `HEX_IDENTITY_PROVIDER=easyauth` makes the server read them. The Azure example enables this and accepts `admin_group_ids` and `publisher_group_ids`.

Group claims are not emitted by default. On the app registration, either:

- Set `groupMembershipClaims` to `ApplicationGroup` and assign the relevant groups to the enterprise application. Only assigned groups are emitted, which avoids the claims overage that silently drops groups for users in many groups. Policies then use `group:<object id>`. A team group must be assigned to the enterprise application before it can appear in a policy.
- Or define **app roles**, assign them to groups or users on the enterprise application, and use `role:<value>` in policies. Roles never overage.

Claims come from the token issued at sign-in, so group and role changes apply after the user signs in again. Removing someone from a group does not revoke an existing session immediately.

If the app registration is shared with other applications, prefer a dedicated registration for Hex before changing its claims configuration.

## Local development

`hex dev` has no authenticating gateway, so the local helper resolves a fixed identity: ID `local-dev`, name `Local Developer`, groups from `HEX_IDENTITY_GROUPS` (comma-separated). The local developer is a platform admin by default, and policies live in memory unless the PostgreSQL provider is selected, so they reset on restart. Simulate a viewer without access by leaving `HEX_IDENTITY_GROUPS` unset and setting `HEX_ADMIN_GROUPS` to something else.

## Trust model and limits

- **The gateway remains the authentication boundary.** Only enable `easyauth` behind a gateway that authenticates every request and strips client-supplied identity headers; never expose the Go server directly with it enabled.
- **Publishers are per-site owners.** Only owners of a site can publish or unpublish it. Azure Files upload URLs are scoped to single files of that site and expire after an hour. The server identity itself holds share-wide write access, so treat the server and its storage credentials as the trust boundary.
- **Names can be claimed first-come.** Limit claiming with `HEX_PUBLISHER_GROUPS`, claim your name when you first publish, and let admins correct squatted names. Unpublishing keeps the policy, so the name stays reserved.
- **Fail-closed behavior.** A restricted site denies anonymous callers and callers whose identity cannot be resolved. If the access store is unavailable, requests fail with a server error rather than falling open.
- **Rules are coarse by design.** Rules apply per path prefix, collection, file prefix and channel, plus per-creator documents. There is no field-level or content-based rule language.

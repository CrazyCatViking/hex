# Identity and site access control

Hex can restrict who views a site — its static assets, files, documents and realtime channels — using groups from the identity provider that already authenticates the platform. Hex never validates identity-provider tokens or manages users itself: the hosting gateway authenticates every request, a configured identity resolver reads what the gateway forwarded, and Hex only answers "is this caller in one of the site's groups?".

Access control is optional. Without it, deployments behave exactly as before: every authenticated user can access every site.

## Concepts

An **identity** is what the gateway resolved for the caller: a stable ID, a display name, and the group and role values the identity provider emitted. Apps read it from `GET /api/hex/me` or `hex.identity()` in the browser client, and operators from `hex whoami`.

A **site access entry** is a server-owned record per site name:

```json
{
  "owners": ["<user or group ID>"],
  "groups": ["<group or role value>"]
}
```

- A site **without** an entry is open to every authenticated user — the previous behavior.
- `groups` lists who may view the site. Members, owners and platform admins pass; everyone else receives 403 for the site's subdomain and its entire `/api/sites/{site}/` namespace, and the site disappears from discovery and the landing page for them.
- An entry with owners but an empty `groups` reserves ownership of the name without restricting viewers.
- `owners` lists who may change or remove the entry. Values are matched against the caller's ID, groups and roles, so an owner can be a person or a group.
- **Platform admins** — configured on the server as `HEX_ADMIN_GROUPS`, matched the same way — can manage every entry and view every site. Configure at least one admin group so squatted or orphaned entries can always be corrected.

Entries live in the platform database (PostgreSQL in the Azure example), never in the published site directory. Publisher-writable files such as `.hex-site.json` carry no authority.

## Enforcement points

Identity flows through two paths, and both enforce the same decision:

1. **Static assets.** The NGINX template sends an `auth_request` subrequest to the Go server (`/api/hex/authz`) for every site-asset request, carrying the validated site name. 204 serves the asset; 403 blocks it. The platform landing page and `/api/` are not affected by this subrequest.
2. **Site APIs.** The Go server checks the `{site}` path segment on every `/api/sites/{site}/...` route — files, documents, realtime WebSocket upgrades — using the caller's resolved identity. The check keys on the path, not the hostname, so a restricted site's data cannot be read from another origin.

Discovery (`GET /api/sites`, the landing page and its statistics) omits sites the caller cannot view.

## Managing entries

Site owners manage entries through the API or the CLI:

```sh
hex whoami
hex access show my-app
hex access set my-app --owner <your-object-id> --group <group-object-id>
hex access clear my-app
```

`hex access set` replaces the whole entry. The resulting entry must keep the caller able to manage it (as an owner, or as an admin), so you cannot lock yourself out by accident. The first `set` for an unclaimed name registers it; claim your site's entry when you first publish. The CLI authenticates these calls like other API commands: `HEX_TOKEN`, or a `resource` with a signed-in Azure CLI. A platform that sets `HEX_API_RESOURCE` advertises the resource in its connection settings, so installer-created profiles authenticate without further configuration; the app registration must expose that identifier as an API scope with the Azure CLI client pre-authorized.

The equivalent HTTP routes are `GET`/`PUT`/`DELETE /api/hex/sites/{site}/access`. Reading an entry requires owner or admin rights; group values are matched case-insensitively.

## Entra ID configuration (Azure example)

The gateway's Easy Auth injects the authenticated caller as `X-MS-CLIENT-PRINCIPAL` headers; setting `HEX_IDENTITY_PROVIDER=easyauth` makes the server read them. The Azure example enables this by default and accepts `admin_group_ids`.

Group claims are not emitted by default. On the app registration, either:

- Set `groupMembershipClaims` to `ApplicationGroup` and assign the relevant groups to the enterprise application. Only assigned groups are emitted, which avoids the claims overage that silently drops groups for users in many groups. Access entries then use **group object IDs**.
- Or define **app roles**, assign them to groups or users on the enterprise application, and use the role values in access entries. Roles never overage.

If the app registration is shared with other applications, prefer a dedicated registration for Hex before changing its claims configuration.

Enable data-plane logging or soft delete on the sites storage account if you need overwrite attribution; see the trust-model caveat below.

## Local development

`hex dev` has no authenticating gateway, so the local helper resolves a fixed identity: ID `local-dev`, name `Local Developer`, groups from `HEX_IDENTITY_GROUPS` (comma-separated). The local developer is a platform admin by default, and access entries live in memory unless the PostgreSQL provider is selected, so they reset on restart. Simulate a viewer without access by leaving `HEX_IDENTITY_GROUPS` unset and setting `HEX_ADMIN_GROUPS` to something else.

## Trust model and limits

- **The gateway remains the authentication boundary.** Only enable `easyauth` behind a gateway that authenticates every request and strips client-supplied identity headers; never expose the Go server directly with it enabled.
- **Access entries protect content from viewers, not from publishers.** Publishing authorization is storage-level and currently share-wide: anyone with publish permission can overwrite any site's files, including a restricted site's code. Treat publishers as a trusted group, and do not put data behind a site restriction whose exposure you could not tolerate from a malicious publisher until per-site publishing authorization exists.
- **Names can be claimed by any authenticated user.** The first `hex access set` for a name registers it. Admins can correct a squatted entry; claim your name when you publish.
- **Fail-closed behavior.** A restricted site denies anonymous callers, callers whose identity cannot be resolved, and everyone when the identity resolver is missing. If the access store is unavailable, requests fail with a server error rather than falling open.
- **Restriction is per site, not per path.** Hash-routed pages and bundled assets cannot be separated server-side; publish a separate site per audience instead. Data always follows the site namespace.

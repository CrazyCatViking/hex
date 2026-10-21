# Workspace directories (optional)

A standalone Hex workspace still uses `hex setup <workspace-url>`, named
profiles, `--platform`, and existing project configuration. Nothing must change
on a standalone server. Directory support is opt-in and never converts a direct
profile or shares its saved sign-in.

## One account, multiple workspaces

A workspace-directory provider such as Seidir advertises public sign-in settings
at its `/api/hex/config` endpoint. Configure it separately from direct profiles:

```sh
hex setup https://console.seidir.app --name seidir
hex login --directory seidir --account personal
hex workspace list --directory seidir --account personal
hex workspace use development --directory seidir --account personal
hex whoami
```

Use the stable workspace ID printed by `list`, or `organization/id`. Listing
supports `--json`. Setup/listing do not choose a workspace, change direct profiles,
or modify a project. `use` explicitly selects the default directory/workspace/account
context. If none is selected and no direct default exists, commands fail with
selection instructions, including in noninteractive execution; they never guess.

The account alias defaults to `default`. Aliases such as `personal` and `operator`
have separate sessions even with the same provider. Explicit operator login does
not replace the personal session or change the selected workspace:

```sh
hex login --directory seidir --account operator
hex logout --directory seidir --account operator
```

Logout only forgets that alias, and works even after workspace access is revoked.
`HEX_TOKEN` remains an explicit automation override.

```sh
hex publish --workspace production
hex whoami --workspace development --directory seidir --account personal
```

`--workspace`, `--directory`, and `--account` are directory options; do not combine
them with an explicit direct `--platform`. A directory flag alone cannot retarget
a project or active context to a different directory. An explicit `--workspace`
is required for that override. No command publishes to all workspaces.

## Project pins and precedence

`hex init my-app` with a directory workspace selected (or `hex init my-app
--workspace development`) pins that workspace in the new project's `hex.json`:

```json
{
  "name": "my-app",
  "workspace": {
    "directory": "https://console.seidir.app",
    "id": "development"
  }
}
```

The pin contains no account alias or credentials. Another developer can register
the same directory under a different local name and use their own account. The
active account for that directory is used, unless `--account` explicitly selects
another alias; without an active account, the alias is `default`.

For an existing project, `hex workspace use development --project` writes just
the workspace pin, preserving other project metadata. It refuses to overwrite
an existing direct `platform`, `server`, or `siteBaseURL` pin. Remove those fields
explicitly if you want to convert the project. A workspace pin cannot be combined
with direct destination settings.

Destination precedence:

1. Explicit `--platform` (direct) or `--workspace` (directory).
2. The project's direct or directory workspace pin.
3. The explicitly selected directory context, otherwise the saved direct default.

Changing a default never changes a pinned project. Running direct `hex setup`
explicitly restores the direct default and deactivates the global directory
context, without deleting directories or account sessions. Existing direct-only
`hex init` behavior remains unchanged.

## Provider protocol and credential boundaries

Public directory discovery (version 1, distinct from a direct connection):

```json
{
  "version": 1,
  "kind": "workspace-directory",
  "name": "Example",
  "server": "https://console.example.com",
  "auth": {
    "type": "oidc",
    "issuer": "https://auth.example.com/realms/example",
    "clientId": "hex-cli",
    "scopes": ["openid", "profile", "offline_access"]
  },
  "workspacesPath": "/api/hex/workspaces"
}
```

The server must match the explicitly configured directory origin. The workspace
API must be a same-origin `/api/` path. It accepts the account's bearer token and
returns an array filtered by current membership:

```json
[
  {
    "id": "development",
    "name": "Development",
    "organization": "acme",
    "organizationName": "Acme",
    "status": "running",
    "role": "member",
    "connection": {
      "version": 1,
      "name": "Development",
      "server": "https://development.example.com",
      "siteBaseURL": "https://development.example.com",
      "auth": { "type": "oidc", "issuer": "https://auth.example.com/realms/example", "clientId": "hex-cli", "scopes": ["openid", "profile", "offline_access"] },
      "capabilities": { "version": 1, "sites": true, "files": true, "database": true, "realtime": false, "maxUploadBytes": 33554432 }
    }
  }
]
```

Workspace IDs are unique within the directory and stable across account sessions.
Every embedded connection is validated as a normal Hex connection and must use
the directory's exact OIDC configuration. A directory is trusted to advertise
workspace origins: do not import an untrusted directory. This does not authorize
arbitrary origins merely because they share an identity provider.

Hex refreshes membership before resolving a directory workspace. It does not
follow discovery/catalog redirects or send directory cookies. Shared sessions
are keyed by directory origin, account alias, and authentication settings, separate
from all direct sessions. API destination overrides cannot redirect those
credentials. Account sessions are private files; public directory/context settings
are kept separately in `directories.json` alongside `profiles.json`.

The gateway must still verify token signature, issuer, audience, client, account
status and workspace access on every request. Discovery is not an authorization
grant, and platform-superadmin status must not imply workspace membership.

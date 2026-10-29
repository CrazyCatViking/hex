# Company portal and installers

Each Hex server includes a portal at the main domain, for example **https://hex.smartdok.dev/**. Published apps remain on **https://<site>.hex.smartdok.dev/**. The portal is written for everyone who uses the apps, not only the people who build them, so it avoids technical terms and puts sharing and finding things first.

## Shared components

`server/portal/portal.css` owns the portal-wide light/dark palette, typography, page width (`--page-width`), control height (`--control-height`), and corner-radius tokens. All pages use the same `.button`, `.field`, `.panel`, and `.site-header` styles. `.form-grid` aligns two-column field layouts without stretching controls to match neighboring help text; it stacks on small screens. `.dialog`, `.dialog-heading`, and `.dialog-body` provide shared modal styling. Use these components rather than feature-scoped theme or page-width overrides. `permissions.css` adds only Sharing-specific layout and matrix treatments.

People/group pickers in permission dialogs use an anchored, scrollable dropdown, not an inline list that resizes the modal. Results use the shared combobox styling, float above modal clipping, and fit the available viewport (including opening upward). Arrow keys and Enter select; Escape dismisses results before the dialog; Tab, outside clicks, and selection close the dropdown. Late directory responses cannot reopen a dismissed picker. The same control is used for site admission, app-permission rows, owners, and direct rule recipients.

## Pages

| Page              | Path                                         | Contents                                                                                                                                                                                                                                                                                 |
| ----------------- | -------------------------------------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Home              | `/`                                          | A greeting, unified search, four personal pins, and bounded recent/shared/owned/company previews. **View all** opens the appropriate complete listing. No traffic aggregate is needed to open Home.                                                                                      |
| App library       | `/library`                                   | Search by name, purpose or person across all accessible discovery/personal listings, with pinned/recent/shared/owned/company scopes, app/file filtering, sorting, grid/list views and 12 results per page.                                                                               |
| Your sites        | `/manage`                                    | The familiar ownership list and actions, with complete server-side search and 12 rows per page. Admins find other apps in the separate admin inventory. Traffic sorting/filtering is optional; it does not run on ordinary list requests.                                                |
| Site              | `/manage/<site>`                             | Tabs for **Overview** (who created and last published it, and its audience), **Sharing**, **Data**, **History** and **Settings** (taking the site down).                                                                                                                                 |
| Build & publish   | `/start`                                     | The installer for the detected OS and three steps: install, share a file or folder, and build an app with a coding agent.                                                                                                                                                                |
| Admin             | `/admin`, `/admin/sites`, `/admin/users`     | Platform-admin inventory with search and 12 rows per page, independent of analytics. Overview, people and site reports load separately when analytics is configured. **Manage** opens the site's existing management page or a restricted temporary-access entry. |
| Integration audit | `/admin/integration-audit`                   | Every call of audited integrations, cached answers included: when, who, from which site, the endpoint, the input and the records it showed, filterable and downloadable as CSV. Requires an integration audit store and platform-admin access; see [Auditing](integrations.md#auditing). |
| Integrations      | `/integrations`, `/integrations?site=<site>` | Registry-driven integration catalog, grants, connection and approval status. Owners request approval for a site; admins review, approve and revoke requests. Site management also exposes an Integrations tab.                                                                           |

The account menu in the header shows the person's name, email, their configured groups and whether they are a platform admin. **Your sites** appears only when management is enabled. **Sign out** appears only when the host supplies `Config.LogoutURL`; it points to that host's logout route. Azure Easy Auth hosts can explicitly configure `/.auth/logout`. There is no provider-specific logout route assumed by the portal, and browser logout does not clear the CLI's saved session (`hex logout` does that).

## Hosting

The Go framework renders the portal pages and catalog fragments with `html/template`. HTMX handles debounced search, sorting and pin refreshes, cancels superseded requests, and preserves query/scope/page in browser history. Main-content responses carry a portal marker so a gateway sign-in page cannot silently replace search results. Sensitive pages are not saved in HTMX's local history cache. The page, styles, scripts, and pinned HTMX distribution are embedded in the server, so there is no frontend build step when installing the Go framework and no runtime CDN dependency. Small scripts detect the visitor's OS, copy links and commands and run the sharing picker. Light and dark themes follow the operating system. Searching, filters and pagination also work with ordinary GET navigation when JavaScript is disabled.

### Personal shortcuts and listing performance

Pinning requires a signed-in human and `Config.Database` implementing
`GuardedDatabase` (the built-in PostgreSQL and memory providers do). Preferences
are stored per provider/identity in the database's workspace, using the internal
`@hex-portal` namespace, which cannot be addressed through site APIs. PostgreSQL
persists them across replicas/restarts; the development memory provider does not
persist across restarts. Up to 100 pins and 20 recently opened site names are
retained. Recent history records portal launches only, not arbitrary app visits,
and saving it never blocks navigation. Neither pins nor history grant access;
each read filters them through current policies. Unpinning is idempotent and is
also allowed after access has been revoked. Sites without a suitable database
still have ordinary discovery and management, but no personal shortcuts.

The descriptive inventory is refreshed at most every five seconds, coalescing
concurrent requests with eight bounded metadata readers and a ten-second refresh
deadline. Cache retention is capped at 10,000 entries and approximately 8 MiB of
descriptive data. Same-replica publish/unpublish invalidates it immediately;
other replicas and out-of-band storage edits converge within five seconds.
**Authorization is never cached:** each listing reads fresh policies in one
batch on PostgreSQL/memory, validates/migrates them through the existing policy
path, and only then projects visible cards. Custom access stores can implement
`SiteAccessBatchReader`; otherwise the fresh per-site fallback remains.

Ordinary home/search/manage requests make no traffic calls. Explicit popularity
or activity requests aggregate only matching accessible candidates, with a
750 ms deadline and an explicit unavailable state on failure (not false zero
counts). PostgreSQL never holds its traffic-cache mutex during database I/O.
Management loads history, people and action summaries only on tabs that use
them; the full per-app page, tabs and sharing editor remain in place.
Admin inventory also reuses the descriptive cache, batches policy reads and
resolves owner labels only for the visible page, with a short lookup deadline.
It does not fetch app icons or wait for reports or historical imports.

This removes repeated remote reads at the reported 100+ app scale, but filtering
and authorization projection are still linear in inventory size. An indexed,
query-shaped persistent directory remains a later step for much larger
installations; UI pagination alone is not that index. PostgreSQL admin reports
now use query-shaped bounded reads; see [platform analytics](analytics.md).

NGINX forwards main-domain requests to Go and serves published app files directly from storage on subdomains. The existing hosting authentication protects the landing page, catalog, and installer downloads. Preserve the external `Host` header when configuring another reverse proxy.

App cards and authorized management pages show the app's published favicon.
Admin inventory and reports use fallback initials, without reading app assets.
The portal discovers local `rel="icon"` / `rel="shortcut icon"` links in the root
`index.html`, then Apple touch icons or conventional `favicon.svg`, `favicon.ico`,
`favicon.png` and `apple-touch-icon.png` files. Relative links, a local `<base>` and
same-site absolute URLs work; external/data URLs are not fetched. This works for
existing publications without republishing. PNG, ICO, SVG, JPEG, GIF and WebP
previews are limited to 256 KiB and preserve site/path access permissions.
Icon source bytes and missing-file probes are cached for 30 seconds (at most
128 files); publish/unpublish invalidates them locally. Current policy and path
checks still run before every response. Rendered icon responses remain
`no-store`, so browser caching never bypasses revocation.

Icon path checks use the same host-selected case mode as static authorization: exact by default, or `Config.PathCaseInsensitive: true` for case-insensitive mounts such as Azure Files. Configure the mode to match the actual static storage before serving protected assets.

Icons use a small authenticated preview endpoint on the portal domain,
`/api/hex/sites/<site>/icon`, so they work with the portal session and same-origin
content policy. Sites without a usable icon keep their initial-letter fallback;
file/folder publications keep the file icon. Icon previews also work without
JavaScript. Published websites continue to be served by nginx.

For SmartDok, configure:

```text
HEX_PLATFORM_NAME=SmartDok Hex
HEX_PUBLIC_URL=https://hex.smartdok.dev
HEX_SITE_BASE_URL=https://hex.smartdok.dev
HEX_AUTH_CONFIG={"type":"oidc","issuer":"https://identity.company.example","clientId":"hex-cli","scopes":["openid","profile","offline_access","hex-api"]}
```

Set up DNS, TLS, ingress bindings, and hosting authentication for the main domain as well as the site domains. The Azure example accepts `platform_url`, `site_base_url`, and `custom_domains`; setting a URL does not create DNS or register Entra callbacks. See [subdomain hosting](subdomains.md).

Consumers can set `hex.Config.Connection` and `SiteBaseURL` directly. Without connection settings, the page still lists apps and explains that the operator must configure installers. `hex dev` configures the local page automatically at **http://localhost:8080/**.

## Discovery visibility

The overview shows discoverable apps, descriptive metadata, and three statistics: app count, distinct author names, and apps published within the last 30 days. These are derived from the current site files, not traffic analytics or authenticated-user counts.

To keep an app out of the overview, set this in its source `hex.json` and republish:

```json
{
  "name": "team-dashboard",
  "title": "Team Dashboard",
  "description": "Daily reports and shared tasks",
  "author": "Alex",
  "discoverable": false
}
```

Set `discoverable` to `true` or remove it and republish to list the app. Missing metadata and omitted visibility fields retain the default visible behavior. Hidden apps and artifacts are excluded from company discovery, `/api/sites`, and overview statistics. Owned or explicitly shared hidden apps/files remain available in personal library scopes; an accessible known app can also be pinned. Their URLs and app APIs continue to work; this is a listing preference, not authorization. The server records the setting in `.hex-site.json` alongside the descriptive metadata. For actual authorization, restrict the site's admission in its [access policy](access-control.md); the library, discovery API and statistics then omit the site for people who cannot view it (ownership still permits management, independently of app admission).

### Testing the production portal

With npm dependencies and Playwright Chromium installed:

```sh
go test -race ./server ./server/providers/memory ./server/providers/local
HEX_TEST_POSTGRES_URL='<local test database>' go test -race ./server/providers/postgres
HEX_PORTAL_BROWSER=1 go test ./server -run '^Test(Portal|AdminAccess)Browser$' -count=1 -v
npm run test:browser
npm run test:analytics
go test ./server -run '^$' -bench BenchmarkPortalInventoryAudit -benchtime=20x
go test ./server -run '^$' -bench BenchmarkPortalColdInventoryWithStorageLatency -benchtime=3x
```

Browser tests exercise actual embedded templates/handlers with 156 sites, not
prototype mocks, including shortcuts, persisted pins, full-inventory search,
pagination, history, no-JavaScript browsing, login-response rejection and mobile
dark layouts. Screenshots go to `/tmp/opencode/hex-portal-implementation`.

## Managing sites

With an identity provider and access control, signed-in users get **Your sites** at `/manage` on the platform domain. It lists the apps and artifacts they actually own. Platform admins find every published site in `/admin/sites`, without acquiring ownership. Each authorized site's page shows:

- who created it and who published it last, and its publication history;
- sharing with separate **Site access** and a checkbox **permission-group table**: configure admitted-user defaults, add only people or directory groups needing custom assignments, create/edit groups, and configure their resource operations, record conditions and field restrictions through visual controls. Dialog edits are staged until **Save changes** applies the batch. Existing custom policies and owner permissions are preserved; JSON is a separate collapsed technical tool, not the primary editor. See [Managing permissions](access-control.md#managing-permissions-in-the-portal);
- a data browser: collections and their documents (with the verified creator of each), the JSON of a document, uploaded files, and deleting documents and files;
- unpublishing from **Settings**, confirmed by typing the site's name.
- an **Analytics** tab with all-time totals, date-filtered trends, and named visitors with per-person page views, visits and last visited, plus search, sorting and pagination when analytics is configured.

See [platform analytics](analytics.md) for durable storage, traffic collection,
metric definitions and admin permissions. Catalogue statistics remain scoped to
visible discoverable apps; admin inventory includes every published site.

Only a site's owners or an administrator with an active, explicit management grant can open its full page. Other platform admins see a restricted entry screen without app data, files or publication history. Management changes retain the API's same-origin and `X-Hex-Request` checks. Pages are rendered by the server with HTMX under the same content security policy as the landing page.

### Temporary administrator access

Being a platform admin does not make a person a site owner or reveal private apps
in normal Home/Library discovery. Admin inventory is a separate governance view.
From **Manage**, admins can deliberately grant themselves one site-specific scope:

- **Management**: publish, change sharing, inspect management/history and take
  down the site. It does not admit the caller to the app or grant data/file access.
- **App read**: admission, all page paths, collection reads, file reads and channel
  reads. It adds no writes, action execution or integration authority.
- **App write**: app-read privileges plus collection/file writes, channel sends,
  action execution and site-level integration read/write grants. Independent
  platform integration permissions, approvals and connected accounts still apply.

Every grant requires a reason and lasts one hour by default, at most four hours.
It is additive to existing permissions, separate from permanent sharing, and
never changes the saved site policy or publication baseline. Deliberate sharing
or publication changes made with management access remain permanent.
Older published sites without a policy also support recovery grants; republishing
without an explicit access-policy change does not claim permanent ownership.
Granting the same scope again ends the previous grant of that scope atomically.
Owners can inspect the paginated **Temporary access** audit and end grants early;
admins can end their own grants. The full management page shows an active-access
banner, expiry and an **End access** control.

`Config.AdminAccess` implements `AdminAccessStore`; when omitted, Hex uses an
implementing access store, then the app database. PostgreSQL implements it in the
platform-owned `hex_admin_access` table, prepared by `MigrateAccess`,
`MigratePlatform` or `Migrate`. Memory implements it for development only, losing
grants and audit history on restart. Custom hosts without a suitable store fail
closed for temporary access; existing owners retain normal management.
Keep all replicas on the same durable access store and synchronize server clocks.
Active grants are read fresh on each request, require continuing platform-admin
membership, and enforce expiry/revocation without browser timers or cleanup jobs.
Elevated WebSockets recheck before each send/delivery and on idle heartbeat.

Adoption requires releasing the Hex Go module, updating consuming servers,
running migrations and rebuilding/deploying **all replicas**. Old replicas retain
the previous bypass until replaced. Apps need no republishing or browser SDK
update. Platform sign-in, configuration and employee onboarding are unchanged.

The portal reads no directory data from the identity provider. People are remembered from their own sign-ins (ID, name and email, refreshed at most hourly) and can be found by name or email once they have used the platform. Selecting a person always saves their stable identity ID; arbitrary email addresses are not authorization principals. Ask new people to sign in first, share with a configured group, or use their verified identity ID through the CLI. The sharing picker is an accessible combobox: it searches `/api/hex/directory` as you type, groups results into groups and people, and supports the arrow keys, Enter and Escape. Groups come from `HEX_GROUPS`, which should list the groups assigned to the app registration, the only ones sign-in tokens carry. The remembered people are stored in PostgreSQL (`hex_people`), or in memory for local development.

## Employee onboarding

1. Visit the main domain and sign in through the company's existing hosting provider.
2. Open **Build & publish**, download the installer for the detected OS (or select another OS).
3. Run the displayed command against that downloaded file.
4. Open a new terminal and run `hex init my-app`.

No separate `hex setup` command, downloaded configuration import, or manual JSON editing is required from employees. The script contains the non-secret connection settings, including auth type, issuer, client ID and scopes, and invokes the CLI's import internally, saving the company profile as the default while preserving other profiles.

The browser download is intentional: terminal `curl`/PowerShell requests cannot reuse a hosting-authentication browser session. Installer scripts do not fetch protected platform configuration, transfer browser cookies, or register OAuth clients. Hosting SSO protects script downloads; the release files must be reachable by the terminal independently.

| Installer                  | Targets                 | User installation directory |
| -------------------------- | ----------------------- | --------------------------- |
| `/api/hex/install/macos`   | Apple Silicon and Intel | `~/.local/bin`              |
| `/api/hex/install/linux`   | x86-64                  | `~/.local/bin`              |
| `/api/hex/install/windows` | x86-64                  | `%LOCALAPPDATA%\Hex\bin`    |

Unix scripts require Bash, curl, base64, and sha256sum or shasum. They add the bin directory to common shell startup files idempotently. The Windows script uses PowerShell and adds its bin directory to the user's PATH. Neither requires administrator privileges. The displayed PowerShell command sets execution policy only for that installation process.

The installer verifies SHA-256 before installing the CLI, imports settings without network access to the platform, and removes temporary files. After installation, `hex update` installs newer binaries while preserving profiles. Rerunning a newly downloaded installer also refreshes platform settings. The first `hex publish` opens the configured identity provider in the browser and saves the session; employees need no other tools.

## Release source

By default, installers download binaries and `SHA256SUMS` from:

```text
https://github.com/crazycatviking/hex/releases/latest/download
```

Publish the first stable CLI release before distributing installers to employees. Use the [Just release recipes](releases.md); stable CLI releases are marked latest, while prereleases are not. Do not mark unrelated releases latest in this repository. A concurrent release change between downloading the checksum and binary can cause a safe checksum failure; rerun the installer.

An operator can set `hex.Config.CLIReleaseURL` or `HEX_CLI_RELEASE_URL` to an organization mirror's HTTPS download directory. The Azure example exposes `cli_release_url`. The directory must provide the same four binary filenames and `SHA256SUMS` used by `just build-cli`, and be reachable without browser-only authentication. HTTP is accepted only on loopback for local installer tests.

Configured mirrors are included as optional `cliReleaseURL` in connection settings, allowing `hex update` to keep using the organization's source automatically. Release the updated CLI before deploying servers that advertise this field, so downloaded installers use a compatible client.

## Development and verification

HTMX is pinned in the root npm lockfile. After updating it, run `npm run build:portal` and commit the updated `server/portal/htmx.min.js`, including its license header. `npm run check:portal` checks synchronization. Go builds use the checked-in asset directly.

`go test ./server ./internal/cli` covers rendering, escaping, visibility, installer generation, and a real Linux installer run against fixture release files. `npm run test:browser` (which needs NGINX on the PATH) exercises the main-domain proxy, HTMX interactions, browser downloads for all OS options on `/start`, mobile layout, and non-JavaScript browsing. macOS and Windows installers still need native OS execution validation; cross-compiling CLI binaries and validating generated scripts does not replace that check.

`npm run test:permission-portal` uses a loopback-only real Hex server and Playwright to exercise the permission table, directory picker, resource-rule builder, staged saves, defaults, explicit owner assignments, scoped memberships, revocation, stale-form error retention, and mobile layout without editing JSON. It also checks the approved prototype's palette and component shapes in light/dark desktop/mobile views, grouped existing-access summaries, page-access review, and preservation of shared rule audiences. Set `HEX_PORTAL_SCREENSHOTS=/absolute/path/prefix` to save visual-review screenshots. It does not call a deployed platform or use real identities.

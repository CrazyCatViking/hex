# Company portal and installers

Each Hex server includes a portal at the main domain, for example **https://hex.smartdok.dev/**. Published apps remain on **https://<site>.hex.smartdok.dev/**. The portal is written for everyone who uses the apps, not only the people who build them, so it avoids technical terms and puts sharing and finding things first.

## Pages

| Page | Path | Contents |
| --- | --- | --- |
| Home | `/` | A greeting for the signed-in person, a search across all apps, **Shared with you**, **Your sites**, and **Apps for everyone**. Cards show all-time page views when analytics is enabled; the catalogue can sort by popularity, visitors or recent visits and filter by activity. Without an identity provider only the catalog is shown. |
| Your sites | `/manage` | Every site the person owns, filterable by text, kind and traffic, with popularity sorting, page-view/visitor counts and open, copy-link, share and analytics actions. Platform admins see every site. |
| Site | `/manage/<site>` | Tabs for **Overview** (who created and last published it, and its audience), **Sharing**, **Data**, **History** and **Settings** (taking the site down). |
| Build & publish | `/start` | The installer for the detected OS and three steps: install, share a file or folder, and build an app with a coding agent. |
| Admin | `/admin`, `/admin/sites`, `/admin/users` | Platform-wide inventory, user activity, publishing history and nginx traffic, with date ranges and site/user drill-downs. Requires analytics and platform-admin access. |
| Integration audit | `/admin/integration-audit` | Every call of audited integrations, cached answers included: when, who, from which site, the endpoint, the input and the records it showed, filterable and downloadable as CSV. Requires an integration audit store and platform-admin access; see [Auditing](integrations.md#auditing). |
| Integrations | `/integrations`, `/integrations?site=<site>` | Registry-driven integration catalog, grants, connection and approval status. Owners request approval for a site; admins review, approve and revoke requests. Site management also exposes an Integrations tab. |

The account menu in the header shows the person's name, email, their configured groups and whether they are a platform admin. **Your sites** appears only when management is enabled. **Sign out** appears only when the host supplies `Config.LogoutURL`; it points to that host's logout route. Azure Easy Auth hosts can explicitly configure `/.auth/logout`. There is no provider-specific logout route assumed by the portal, and browser logout does not clear the CLI's saved session (`hex logout` does that).

## Hosting

The Go framework renders the portal pages and catalog fragments with `html/template`. HTMX handles search, sort, and refresh requests. The page, styles, scripts, and pinned HTMX distribution are embedded in the server, so there is no frontend build step when installing the Go framework and no runtime CDN dependency. Small scripts detect the visitor's OS, copy links and commands, filter lists and run the sharing picker. Light and dark themes follow the operating system. Browsing also works with ordinary GET forms when JavaScript is disabled.

NGINX forwards main-domain requests to Go and serves published app files directly from storage on subdomains. The existing hosting authentication protects the landing page, catalog, and installer downloads. Preserve the external `Host` header when configuring another reverse proxy.

App cards, management pages and admin site lists show the app's published favicon.
The portal discovers local `rel="icon"` / `rel="shortcut icon"` links in the root
`index.html`, then Apple touch icons or conventional `favicon.svg`, `favicon.ico`,
`favicon.png` and `apple-touch-icon.png` files. Relative links, a local `<base>` and
same-site absolute URLs work; external/data URLs are not fetched. This works for
existing publications without republishing. PNG, ICO, SVG, JPEG, GIF and WebP
previews are limited to 256 KiB and preserve site/path access permissions.

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

Set `discoverable` to `true` or remove it and republish to list the app. Missing metadata and omitted visibility fields retain the default visible behavior. Hidden apps are excluded server-side from the HTML catalog, `/api/sites`, and overview statistics. Their URLs and app APIs continue to work; this is a listing preference, not authorization. The server records the setting in `.hex-site.json` alongside the descriptive metadata. For actual authorization, restrict the site's viewers in its [access policy](access-control.md); the catalog, discovery API and statistics then omit the site for people who cannot view it.

## Managing sites

With an identity provider and access control, signed-in users get **Your sites** at `/manage` on the platform domain. It lists the apps and artifacts they own; platform admins can list every site. Each site's page shows:

- who created it and who published it last, and its publication history;
- sharing in the style of familiar document tools: a searchable picker for people and groups, a role per entry (**Owner**, **Can edit**, **Can view**), and a **General access** choice between *Only people added*, *Everyone can view* and *Everyone can edit*. The page warns before saving when general access makes listed roles redundant, and keeps at least one owner. Path, collection, file and channel rules stay available as JSON under **Advanced rules**;
- a data browser: collections and their documents (with the verified creator of each), the JSON of a document, uploaded files, and deleting documents and files;
- unpublishing from **Settings**, confirmed by typing the site's name.
- an **Analytics** tab with all-time totals, date-filtered trends, and named visitors with per-person page views, visits and last visited, plus search, sorting and pagination when analytics is configured.

See [platform analytics](analytics.md) for durable storage, traffic collection,
metric definitions and admin permissions. Catalogue statistics remain scoped to
visible discoverable apps; admin inventory includes every published site.

Only a site's owners and platform admins can open its page; every change goes through `/api/hex/manage/…` with the API's same-origin and `X-Hex-Request` checks. Pages are rendered by the server with HTMX under the same content security policy as the landing page.

The portal reads no directory data from the identity provider. People are remembered from their own sign-ins (ID, name and email, refreshed at most hourly) and can be found by name or email once they have used the platform. Selecting a person always saves their stable identity ID; arbitrary email addresses are not authorization principals. Ask new people to sign in first, share with a configured group, or use their verified identity ID through the CLI. The sharing picker is an accessible combobox: it searches `/api/hex/directory` as you type, groups results into groups and people, and supports the arrow keys, Enter and Escape. Groups come from `HEX_GROUPS`, which should list the groups assigned to the app registration, the only ones sign-in tokens carry. The remembered people are stored in PostgreSQL (`hex_people`), or in memory for local development.

## Employee onboarding

1. Visit the main domain and sign in through the company's existing hosting provider.
2. Open **Build & publish**, download the installer for the detected OS (or select another OS).
3. Run the displayed command against that downloaded file.
4. Open a new terminal and run `hex init my-app`.

No separate `hex setup` command, downloaded configuration import, or manual JSON editing is required from employees. The script contains the non-secret connection settings, including auth type, issuer, client ID and scopes, and invokes the CLI's import internally, saving the company profile as the default while preserving other profiles.

The browser download is intentional: terminal `curl`/PowerShell requests cannot reuse a hosting-authentication browser session. Installer scripts do not fetch protected platform configuration, transfer browser cookies, or register OAuth clients. Hosting SSO protects script downloads; the release files must be reachable by the terminal independently.

| Installer | Targets | User installation directory |
| --- | --- | --- |
| `/api/hex/install/macos` | Apple Silicon and Intel | `~/.local/bin` |
| `/api/hex/install/linux` | x86-64 | `~/.local/bin` |
| `/api/hex/install/windows` | x86-64 | `%LOCALAPPDATA%\Hex\bin` |

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

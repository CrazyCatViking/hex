# Company landing page and installers

Each Hex server includes a landing page at the main domain, for example **https://hex.smartdok.dev/**. Published apps remain on **https://<site>.hex.smartdok.dev/**.

## Hosting

The Go framework renders the landing page and catalog fragments with `html/template`. HTMX handles search, sort, and refresh requests. The page, styles, scripts, and pinned HTMX distribution are embedded in the server, so there is no frontend build step when installing the Go framework and no runtime CDN dependency. A small script detects the visitor's OS and copies installer commands. Browsing also works with ordinary GET forms when JavaScript is disabled.

NGINX forwards main-domain requests to Go and serves published app files directly from storage on subdomains. The existing hosting authentication protects the landing page, catalog, and installer downloads. Preserve the external `Host` header when configuring another reverse proxy.

For SmartDok, configure:

```text
HEX_PLATFORM_NAME=SmartDok Hex
HEX_PUBLIC_URL=https://hex.smartdok.dev
HEX_SITE_BASE_URL=https://hex.smartdok.dev
HEX_PUBLISH_URL=https://ACCOUNT.file.core.windows.net/sites/public/sites
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

Set `discoverable` to `true` or remove it and republish to list the app. Missing metadata and omitted visibility fields retain the default visible behavior. Hidden apps are excluded server-side from the HTML catalog, `/api/sites`, and overview statistics. Their URLs and app APIs continue to work; this is a listing preference, not authorization. The CLI includes the setting in `.hex-site.json` alongside the descriptive metadata.

## Employee onboarding

1. Visit the main domain and sign in through the company's existing hosting provider.
2. In **Get started**, download the installer for the detected OS (or select another OS).
3. Run the displayed command against that downloaded file.
4. Open a new terminal and run `hex init my-app`.

No separate `hex setup` command, downloaded configuration import, or manual JSON editing is required from employees. The script contains the non-secret connection settings and invokes the CLI's import internally, saving the company profile as the default while preserving other profiles.

The browser download is intentional: terminal `curl`/PowerShell requests cannot reuse an Entra browser session. Installer scripts do not fetch protected platform configuration, transfer browser cookies, or register OAuth clients. Hosting SSO protects script downloads; the release files must be reachable by the terminal independently.

| Installer | Targets | User installation directory |
| --- | --- | --- |
| `/api/hex/install/macos` | Apple Silicon and Intel | `~/.local/bin` |
| `/api/hex/install/linux` | x86-64 | `~/.local/bin` |
| `/api/hex/install/windows` | x86-64 | `%LOCALAPPDATA%\Hex\bin` |

Unix scripts require Bash, curl, base64, and sha256sum or shasum. They add the bin directory to common shell startup files idempotently. The Windows script uses PowerShell and adds its bin directory to the user's PATH. Neither requires administrator privileges. The displayed PowerShell command sets execution policy only for that installation process.

The installer verifies SHA-256 before installing the CLI, imports settings without network access to the platform, and removes temporary files. After installation, `hex update` installs newer binaries while preserving profiles. Rerunning a newly downloaded installer also refreshes platform settings. Azure publishers need Azure CLI installed once; `hex publish` then prepares AzCopy and invokes Azure CLI's Entra browser sign-in with MFA support. Employees do not need separate AzCopy installation or login commands.

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

`go test ./server ./internal/cli` covers rendering, escaping, visibility, installer generation, and a real Linux installer run against fixture release files. `npm run test:browser` exercises the main-domain proxy, HTMX interactions, browser downloads for all OS options, mobile layout, and non-JavaScript browsing. macOS and Windows installers still need native OS execution validation; cross-compiling CLI binaries and validating generated scripts does not replace that check.

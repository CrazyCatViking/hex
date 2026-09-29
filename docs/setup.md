# Connect the CLI to a platform

## End-user flow

Visit your company's main Hex domain, such as `https://hex.smartdok.dev`. The landing page lists apps and offers an OS-specific installer under **Get started**. Download it in the signed-in browser and run the displayed command. It installs the latest CLI and automatically saves the platform configuration. Then open a new terminal and run `hex init my-app`. See [landing page and installers](portal.md).

## Manual setup and additional platforms

For an already installed CLI, automation, or an additional platform, the explicit setup workflow remains available:

```sh
hex setup
```

Enter the platform's origin, such as `https://hex.company.example`. The CLI tries `GET /api/hex/config` without credentials or automatic redirects.

- If JSON configuration is available, it validates and saves it directly.
- If the host returns an authentication response/redirect or an HTML login page, the CLI opens the same download URL in the user's browser. Sign in using the company's existing hosting authentication, download `hex-platform.json`, and drag that file into the terminal prompt or paste its path.
- A missing endpoint, network failure or invalid JSON is reported as such; it is not treated as a successful sign-in.

The terminal parser accepts common quoted paths, escaped spaces, PowerShell-style dropped paths and `file:` URLs. Paths are read as data, never executed as shell commands. If automatic browser opening fails, the URL remains visible for manual use.

You can also supply the URL or downloaded file directly:

```sh
hex setup https://hex.company.example --name company
hex setup --file "$HOME/Downloads/hex-platform.json" --name company
```

An optional `--server https://hex.company.example` checks that an imported file belongs to the expected platform. Loopback aliases on the same port are treated as equivalent for local development.

Successful setup sets the saved profile as the default. Then:

```sh
hex init my-app
```

Run `hex publish` from the app directory. Plain sites need only index.html and assets; build bundled apps first. Company platforms require a sign-in for API commands, including publishing. The first such command in a terminal opens the browser for Entra sign-in with MFA support; Hex saves the session in `sign-in.json` in its configuration directory and refreshes it silently afterwards. `hex login` signs in explicitly and `hex logout` forgets the session. No other tools are needed when the platform advertises its sign-in app; older platforms that advertise only a resource fall back to Azure CLI. Local platforms need no sign-in. See [Publishing](publishing.md#authentication).

## Claude Code and other agents

Use non-interactive, structured output:

```sh
hex setup https://hex.company.example --name company --json
```

`--json` never opens a browser or prompts, even in a terminal. Without a TTY, setup also avoids blocking for input. Exit codes are:

| Code | Meaning |
| --- | --- |
| `0` | Configuration imported; `status` is `ready` |
| `2` | User action needed: `download_required` or `input_required` |
| `1` | Failed download, invalid configuration or another error |

A protected platform produces a response like:

```json
{
  "status": "download_required",
  "downloadURL": "https://hex.company.example/api/hex/config",
  "reason": "browser_authentication",
  "server": "https://hex.company.example",
  "message": "Ask the user to open downloadURL in their browser, sign in, and download the connection file. Import its path with hex setup --file.",
  "nextCommand": "hex setup --file <downloaded-file> --server <platform-url> --json"
}
```

The agent gives the user that URL. Once the user says the file is downloaded or drops it into Claude Code, use the supplied local path:

```sh
hex setup --file "/path/with spaces/hex-platform.json" \
  --server https://hex.company.example --name company --json
```

If the agent receives an attachment's contents rather than an accessible local path, save the JSON to a local file first. Do not ask for browser cookies, passwords, access tokens or Entra application IDs. Authentication happens in the user's browser.

## Profiles and projects

Profiles live in `profiles.json` under `HEX_CONFIG_DIR`, or by default under the user's configuration directory (`$XDG_CONFIG_HOME/hex`, `%LOCALAPPDATA%/hex`, or `~/.config/hex`). They contain only the versioned non-secret connection document. Unknown top-level fields and credential-bearing URLs are rejected; capabilities added by newer platforms are tolerated, and the storage `publishing` destination older platforms advertised is accepted and ignored. File imports/downloads are limited to 64 KiB.

By default, initialization creates only the site name and commands use the current default profile:

```json
{
  "name": "my-app"
}
```

`hex init other-app --platform staging` writes `platform: "staging"` to pin a saved profile. Without a pin, changing the default with `hex setup` changes the destination used by subsequent commands. `hex publish --platform staging` selects a profile for that invocation. Publishing detects dist/, public/, or the project root; an optional `directory` field pins a source. Explicit legacy `server` and `siteBaseURL` settings still work; a legacy `publishing` setting is ignored. Rerun setup to refresh a profile after the operator changes its configuration. Profiles are per user; for a pinned project, another developer imports the profile under the project's expected name.

Publishing, unpublishing, `hex sites`, `whoami` and `access` call the protected API. They authenticate with `HEX_TOKEN`, the CLI's saved sign-in for the profile's `resource`, or, when the profile has no `clientId`, an Azure CLI token. `hex capabilities` uses cached capabilities when a profile supplies them; `--refresh` explicitly requests the current API response. Setup deliberately does not transfer browser sessions to the CLI.

## Configure a Hex server

An operator explicitly supplies connection settings:

```go
config := hex.Config{
    Sites:       siteDirectory,
    SiteBaseURL: "https://hex.company.example",
    Connection: &hex.ConnectionConfig{
        Name:     "Company Hex",
        Server:   "https://hex.company.example",
        Resource: "api://<client-id>",
        ClientID: "<client-id>",
        TenantID: "<tenant-id>",
    },
}
```

When `Connection` is configured, the framework registers `/api/hex/config` as a JSON attachment download. Capabilities are derived from the enabled providers, including `publishing` when a `SitePublisher` is configured. `Resource` is the optional Entra resource the CLI requests tokens for. `ClientID` and `TenantID` name the public-client app registration the CLI signs in as; set both together with `Resource`, or neither (the CLI then falls back to Azure CLI for the resource). Omitting `Connection` disables the endpoint entirely. This does not store site metadata.

The endpoint stays behind the hosting layer's existing authentication. It does not issue credentials or require an anonymous login-discovery endpoint. Top-level browser navigation to this read-only download is allowed through the API's origin checks so hosting-login redirects can complete. Scripted cross-origin requests retain the normal checks and no CORS permissions are added.

For the reference executable, set `HEX_PUBLIC_URL`, optionally `HEX_PLATFORM_NAME`, and `HEX_API_RESOURCE` with `HEX_CLI_CLIENT_ID` and `HEX_CLI_TENANT_ID` when CLI API calls need a sign-in. `HEX_SITE_BASE_URL` remains the parent site origin. The Azure OpenTofu example wires these values, deriving the public gateway URL from the hosting environment; set `platform_url` when clients use a custom gateway hostname instead. That declared server origin must match the URL used for setup.

`dev.Open` automatically supplies a local connection document and publishes to the local site directory. `hex dev` supplies the gateway URL, so local users can simply run:

```sh
hex setup http://localhost:8080 --name local
hex init demo
```

Direct `go run .` without NGINX exposes the document on the API listener (normally `http://localhost:8081`); its published-site URL still refers to the separately configured static gateway.

## Hosting and publishing

The hosting layer's browser authentication protects configuration downloads and every API call, including publishing. The server then authorizes each publication against the site's owners and hands out upload targets; on Azure these are short-lived per-file SAS URLs, so publishers need network reachability to the storage account but no storage role. A successful setup import does not verify that the user may publish. Profiles store neither SAS tokens nor client secrets. No remotely supplied commands or plugins are executed from a connection file.

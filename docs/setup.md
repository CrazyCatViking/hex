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

Run `hex publish` from the app directory. Plain sites need only index.html and assets; build bundled apps first. For Azure Files, Hex installs its publishing tool automatically if needed, reuses the cached storage session, and starts Microsoft sign-in when necessary. No separate login command is required. AzCopy owns credential caching; Hex does not implement OAuth or copy tokens. Storage permissions and network connectivity are supplied by the operator. Filesystem publishing needs no login.

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

If the agent receives an attachment's contents rather than an accessible local path, save the JSON to a local file first. Do not ask for browser cookies, passwords, access tokens or Entra application IDs. Authentication happens in the user's browser or Microsoft's storage tool.

## Profiles and projects

Profiles live in `profiles.json` under `HEX_CONFIG_DIR`, or by default under the user's configuration directory (`$XDG_CONFIG_HOME/hex`, `%LOCALAPPDATA%/hex`, or `~/.config/hex`). They contain only the versioned non-secret connection document. Unknown fields, credential-bearing URLs and unsupported providers are rejected. File imports/downloads are limited to 64 KiB.

By default, initialization creates only the site name and commands use the current default profile:

```json
{
  "name": "my-app"
}
```

`hex init other-app --platform staging` writes `platform: "staging"` to pin a saved profile. Without a pin, changing the default with `hex setup` changes the destination used by subsequent commands. `hex publish --platform staging` selects a profile for that invocation. Publishing detects dist/, public/, or the project root; an optional `directory` field pins a source. Explicit legacy `server`, `siteBaseURL` and `publishing` settings still work. Rerun setup to refresh a profile after the operator changes its configuration. Profiles are per user; for a pinned project, another developer imports the profile under the project's expected name.

Publishing and unpublishing only read the cached profile. They do not contact the Hex API for discovery, credentials, registration or completion. `hex capabilities` uses cached capabilities when a profile supplies them; `--refresh` explicitly requests the current API response.

`hex sites` still calls the protected discovery API. Storage login is not a login to that gateway. Use the browser for protected API access, or the existing advanced `HEX_TOKEN`/Azure CLI resource mechanism where programmatic API access is configured. Setup deliberately does not transfer browser sessions to the CLI.

## Configure a Hex server

An operator explicitly supplies connection settings:

```go
config := hex.Config{
    Sites:       siteDirectory,
    SiteBaseURL: "https://hex.company.example",
    Connection: &hex.ConnectionConfig{
        Name:   "Company Hex",
        Server: "https://hex.company.example",
        Publishing: &hex.PublishingConfig{
            Provider: "azure-files",
            URL:      "https://ACCOUNT.file.core.windows.net/sites/public/sites",
        },
    },
}
```

When `Connection` is configured, the framework registers `/api/hex/config` as a JSON attachment download. Capabilities are derived from the enabled providers. Omitting `Publishing` supports platforms without a publishing destination; omitting `Connection` disables the endpoint entirely. This does not store site metadata.

The endpoint stays behind the hosting layer's existing authentication. It does not issue credentials or require an anonymous login-discovery endpoint. Top-level browser navigation to this read-only download is allowed through the API's origin checks so hosting-login redirects can complete. Scripted cross-origin requests retain the normal checks and no CORS permissions are added.

For the reference executable, set `HEX_PUBLIC_URL`, optionally `HEX_PLATFORM_NAME`, and `HEX_PUBLISH_URL` for Azure Files. `HEX_SITE_BASE_URL` remains the parent site origin. The Azure OpenTofu example wires these values, deriving the public gateway URL from the hosting environment; set `platform_url` when clients use a custom gateway hostname instead. That declared server origin must match the URL used for setup.

`dev.Open` automatically supplies a local filesystem connection document. `hex dev` supplies the gateway URL, so local users can simply run:

```sh
hex setup http://localhost:8080 --name local
hex init demo
```

Direct `go run .` without NGINX exposes the document on the API listener (normally `http://localhost:8081`); its published-site URL still refers to the separately configured static gateway. Filesystem publishing profiles are accepted only for loopback/local platform origins, and their directory must be absolute.

## Hosting and publishing remain separate

Azure's browser authentication protects configuration downloads. Azure Storage independently authorizes direct publishing. A successful setup import does not verify storage access, grant publishing permission, or provide VPN connectivity. The current private Azure Files endpoint still requires a VPN/private runner. The profile stores neither SAS tokens nor client secrets; an operator-supplied `HEX_PUBLISH_SAS` remains an explicit environment-only alternative.

The configuration schema currently supports filesystem and Azure Files publishing. Future GCP/AWS adapters can extend the declarative schema without adding authentication to the Hex server. No remotely supplied commands or plugins are executed from a connection file.

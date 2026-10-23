# Harness setup and local MCP

Hex integrates with coding harnesses that can edit files, install dependencies, build and test apps. Hex does not provide a general-purpose file editor, shell or build runner. Its CLI and local MCP server share the same configuration, authentication and publication implementation.

## Install once

```sh
hex agents install
hex agents status
```

The default `--target auto` installs **Claude Code, Codex and OpenCode V2** user integrations, including when the harness has not yet been installed. It is not process detection. Company portal installers run this after configuring the platform. A harness-configuration conflict is reported without failing the CLI/platform installation; resolve it and rerun the command.

The installation adds:

- A global Hex skill that triggers for new apps even before `hex.json` exists.
- A short persistent preference: use the company Hex platform for new web apps unless the user requests otherwise or the existing project requires another platform.
- A local MCP registration using the absolute path of the installed Hex executable, so GUI processes do not depend on shell PATH setup.

Start a new harness session afterward. Existing projects and explicit user choices take precedence over the default. When sign-in is needed, the assistant calls `hex_login`, shows the browser link, and lets the user complete company sign-in and MFA. It then checks `hex_login_status` and retries the operation. Credentials remain in Hex's local session cache; the assistant never receives tokens. CLI-only harnesses can run `hex login` themselves.

```sh
hex agents install --target claude-code
hex agents install --target codex
hex agents install --target opencode
hex agents install --target claude-code --mcp=false

# Run from the project directory:
hex agents install --target cursor --project
hex agents install --target vscode --project
hex agents status --target cursor --project
```

`--mcp=false` installs skills and defaults without changing MCP configuration. It does not remove an existing MCP registration. This suits harnesses that use the CLI directly.

## Installed files

| Harness | Skill | Persistent instruction | MCP configuration |
| --- | --- | --- | --- |
| Claude Code, user | `~/.claude/skills/hex/SKILL.md` | `~/.claude/rules/hex.md` | `~/.claude.json`, `mcpServers.hex` |
| Codex, user | `~/.agents/skills/hex/SKILL.md` | `~/.codex/AGENTS.md` | `~/.codex/config.toml`, `mcp_servers.hex` |
| OpenCode V2, user | `~/.config/opencode/skills/hex/SKILL.md` | `~/.config/opencode/AGENTS.md` | `~/.config/opencode/opencode.json(c)`, `mcp.servers.hex` |
| Claude Code, project | `.claude/skills/hex/SKILL.md` | `.claude/rules/hex.md` | `.mcp.json` |
| Codex, project | `.agents/skills/hex/SKILL.md` | `AGENTS.md` | `.codex/config.toml` |
| OpenCode V2, project | `.opencode/skills/hex/SKILL.md` | `AGENTS.md` | Existing root or `.opencode/opencode.json(c)`; defaults to `.opencode/opencode.json` |
| Cursor, project | `.agents/skills/hex/SKILL.md` | `.cursor/rules/hex.mdc` with `alwaysApply: true` | `.cursor/mcp.json` |
| VS Code/Copilot, project | `.agents/skills/hex/SKILL.md` | `.github/copilot-instructions.md` | `.vscode/mcp.json` |

Claude configuration respects `CLAUDE_CONFIG_DIR`; Codex configuration respects `CODEX_HOME`. Cursor and VS Code adapters currently require `--project`, so their default applies only within that project. Project MCP configurations contain a machine-specific executable path; review before committing them for teammates on other machines. Harness trust/settings can control whether project instructions and MCP servers load.

Instruction updates replace only `hex:managed` blocks and preserve other content. MCP installation preserves other servers and settings. Conflicting existing `hex` registrations are reported rather than overwritten. OpenCode JSONC comments and trailing commas are supported; the other JSON adapters require valid JSON and report commented JSON for manual editing. Codex TOML comments and unrelated configuration are preserved. Skills at the dedicated Hex skill paths are refreshed from the binary's embedded source.

### OpenCode V2

OpenCode uses the shared local MCP server, native skills and `AGENTS.md`; no plugin is required. The global adapter respects `XDG_CONFIG_HOME` (default `~/.config`). It updates an existing `opencode.json` or `opencode.jsonc`, preserving comments and other settings. If multiple configuration files exist within the selected scope, consolidate them before installation to avoid a shadowed registration. Existing legacy-shaped `mcp.hex` entries are reported rather than migrated automatically.

```sh
hex agents install --target opencode
hex agents status --target opencode
# Alternatively, run in the project:
hex agents install --target opencode --project
```

The generated V2 server entry is under `mcp.servers.hex`, with `type: "local"` and `command: ["/absolute/path/to/hex", "mcp", "serve"]`. OpenCode's default Code Mode exposes the tools under `tools.hex` (for example, `tools.hex.hex_get_docs`); native tool names are prefixed, such as `hex_hex_get_docs`. Use the exact tool names advertised by the harness. Sign-in uses the Hex login tools, not OpenCode's remote-MCP OAuth flow.

Check the actual connection with `opencode mcp list` or `/mcps`. The installer/status command verifies configuration, not a live OpenCode connection. OpenCode V2 has a background service; a new conversation does not necessarily restart a running MCP process. Reconnect Hex through `/mcps`, or use `opencode service restart` when you need a freshly installed binary to become active. Automatic updates otherwise use the same background checks and saved Hex login as the other integrations.

Stable release builds of `hex mcp serve` check for updates in the background at startup, at most once per day after a successful check (once per hour after failure). This does not delay the MCP handshake or interrupt ongoing tools. The updater uses the existing release source, SHA-256 verification and executable validation. It replaces the installed CLI on disk; the running MCP process continues using its current version until the next launch. The CLI and MCP are the same executable, so both update together.

Successful checks also refresh existing global Hex guidance using the installed binary. `hex agents refresh` updates only already-managed integrations and does not add MCP registrations. Project guidance can be refreshed with `hex skills`. Manual `hex update` remains available and also refreshes installed global guidance.

Updates need a user-writable installation directory. Offline/download/verification failures keep the installed version running and are reported on stderr. Development/prerelease builds and platforms without a distributed binary do not auto-update. Set `HEX_AUTO_UPDATE=0` in the MCP process environment to opt out for managed deployments. The release source follows `HEX_CLI_RELEASE_URL`, then the default profile's release URL, then the standard Hex release URL. A process lock prevents concurrent automatic updaters. The check timestamp and last failure are stored in `mcp-update.json` in the Hex configuration directory.

Users on a release predating this feature need one initial installer rerun or `hex update` followed by `hex agents install`. Thereafter, starting new harness sessions keeps the integration current without a manual update routine. An indefinitely running MCP session is not hot-reloaded.

To remove the integration, remove its `hex` MCP entry, the Hex skill directory and the `hex:managed` instruction block. Preserve other content in shared instruction/configuration files. In Codex TOML, remove the entire Hex-managed block.

## MCP tools

```sh
hex mcp serve
```

This is a stdio MCP server, not an HTTP endpoint. Stdout is reserved for protocol messages. The initial tools are:

| Tool | Purpose |
| --- | --- |
| `hex_get_context` | Identify Hex and inspect the default profile or explicit project's platform selection |
| `hex_get_docs` | Read embedded, version-matched app development guidance |
| `hex_capabilities` | Read cached capabilities or explicitly refresh them |
| `hex_init` | Create configuration and guidance, refusing to overwrite existing `hex.json` |
| `hex_sites` | List catalogue sites or the user's owned sites |
| `hex_publish` | Publish existing project output and return its URL |
| `hex_login` | Start browser sign-in and return a login ID and browser link when ready |
| `hex_login_status` | Retrieve the sign-in link or completion status without exposing credentials |

Project tools require an absolute `projectPath`; they never infer the project from the MCP process's launch directory. They accept optional `platform`, `workspace`, `directory` and `account` selectors, following the existing CLI's resolution and conflict rules. Publishing keeps normal file checks and access-policy merging. It does not offer an implicit access-policy replacement. `confirmLarge` corresponds to the existing CLI's `--yes` for unusually large publications.

Other platform operations remain available through the CLI, including actions, data, files, integrations, AI and automations. The skill documents these commands. There is no arbitrary command-execution MCP tool.

Sign-in runs independently of the short initial tool call, with a five-minute deadline tied to the MCP server lifetime. `hex_login` returns `starting`, `waiting_for_browser`, `signed_in` or `failed`; `hex_login_status` accepts its `loginID`. While starting, check status for the link. While waiting for the browser, let the user finish rather than continuously polling. Duplicate requests for the same pending login reuse it; a different concurrent account login is refused. A failed or expired flow can be restarted. The browser must be able to reach the loopback callback on the machine running Hex; remote/sandboxed environments need appropriate shared networking or a locally running integration. If the browser cannot open automatically, the assistant can present the link.

## Other clients and Cowork

For a client supporting local stdio MCP, register an absolute executable path and arguments `mcp`, `serve` using that client's supported configuration UI or deployment mechanism. The shared process definition is:

```json
{
  "command": "/absolute/path/to/hex",
  "args": ["mcp", "serve"]
}
```

The enclosing configuration format varies by client. The setup adapters above do not configure ordinary Claude Desktop chat or package a desktop extension. Cowork also needs its own supported local-server configuration and shared-folder setup; a Claude Code registration is not a Cowork registration.

The harness and MCP process must see the same project files. A path inside a VM, container, remote workspace or Cowork sandbox is not automatically accessible to a host-launched MCP process. Launch Hex in the same environment, expose a supported shared directory and pass the host-visible path, or use the CLI within the harness environment. Hex does not copy project sources across that boundary.

## Acceptance workflow

In a fresh supported harness session in an empty directory, ask: “Build a team equipment tracker.” The agent should consult Hex guidance, initialize configuration, use its own editor/build/test tools, and publish through Hex without searching for an unrelated product. In a later session in the project, ask it to add a filter and republish. Also check that an explicit request for another platform overrides the default.

`hex agents status` diagnoses installed artifacts and registration compatibility; it does not claim that a running harness has loaded them or that a sandbox can reach the project. Confirm those in the harness's own MCP/skills UI.

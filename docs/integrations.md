# Curated company integrations and MCP

Hex can expose a company-owned tool catalogue through the CLI, a local stdio MCP
bridge, and an optional remote Streamable HTTP MCP endpoint. Employees configure
one **Company Hex** connection and select a small bundle such as `engineering` or
`product`; adapters can remain separate modules/services behind that connection.

This version supports explicitly registered **read-only** tools. It does not
automatically install, mirror or trust vendor MCP servers, expose arbitrary HTTP
requests/SQL, or register executable code through published sites. The included
[integration server](../examples/integrations-server/README.md) uses fixture issue
and statistics adapters, not live company credentials.

## Architecture and permissions

```text
Agent / employee
  ├─ hex tools ...
  ├─ hex mcp serve --bundle engineering (stdio)
  └─ https://hex.company.example/mcp/engineering (optional remote MCP)
             │
      IntegrationRuntime
      ├─ bundle + tool + resource grants
      ├─ input/output schema and size validation
      ├─ per-call deadline and record bounds
      ├─ shared per-user extraction/concurrency budgets
      └─ durable audit completion before output
             │
      Reviewed company adapters
      ├─ fixed vendor API operations
      ├─ selected, constrained vendor MCP tools
      └─ predefined reports / approved dataset views
```

Tools and bundles require explicit typed `user:`, `group:` or `role:` principals.
There is no implicit site-owner, editor or platform-admin bypass. An identity must
pass the bundle grant, tool grant and any selected resource grant. Unknown tools,
out-of-bundle calls and disabled tools are denied and audited. Deployment-owned
`SetToolEnabled` / `SetBundleEnabled` methods act as kill switches; discovery and
execution consult current registry state. Calls already dispatched may finish.

`ToolBundle.RequiredScopes` optionally requires additional trusted token scopes
on **all** interfaces, supporting separately scoped agent credentials. Selecting a
bundle, an MCP session ID, `clientInfo`, `User-Agent`, or a site name grants no
authority. A local bridge does not sandbox its agent: an agent with access to an
employee's broader credentials can use that employee's other permitted interfaces.
Use narrow credentials and an isolated/managed execution environment when agent
rights must be lower than the employee's normal rights. Hex controls its gateway;
managed client configuration and vendor OAuth restrictions address direct access
to other MCPs or SaaS credentials.

## Register a tool and bundles

The host constructs `IntegrationRegistry`, registers `IntegrationTool` contracts
and handlers, then registers `ToolBundle` definitions referencing those tool names.
The complete executable is in `examples/integrations-server/`.

```go
runtime, err := hex.NewIntegrationRuntime(
    registry,
    state, // memory.NewIntegrationState() locally; postgres.Database in production
    hex.DefaultIntegrationBudget(),
    []string{"role:integration-auditor"},
)
if err != nil {
    return err
}
config.Integrations = runtime
handler := hex.New(config)
```

`IntegrationStateStore` is independent of app documents and website analytics.
The PostgreSQL provider implements it using `hex_tool_calls`; call `Migrate` before
serving and retain the pool lifecycle under the host's control. It can share the
existing PostgreSQL pool or use a separate database. There is no automatic
production fallback to memory. In-memory state is ephemeral and intended for
development; shared quotas across replicas/restarts require PostgreSQL or an
equivalent atomic implementation.

A tool includes:

- A stable name, integration name, explicit version and concise description.
- Self-contained input/output JSON Schemas. Roots must be objects with
  `additionalProperties:false`; nested objects must also reject extra fields.
- Explicit caller principals. Optional `ResourceField` names a required string
  input, with up to 128 literal `ResourceGrant` entries. Discovery narrows its enum
  to the caller's authorized resources; execution checks that selection again.
- `ReadOnly:true` and bounded `ToolLimits`.
- A handler returning `json.RawMessage`. `IntegrationContext` supplies the verified
  identity, fixed bundle, authorized resource and limits, but no incoming token.

Adapters are trusted reviewed server code. A read-only declaration/annotation is
not proof that arbitrary code or a vendor operation is read-only. Use restricted
upstream credentials, fixed API operations/query templates and approved output
field mapping. Delegate through `IntegrationRuntime` when exposing the same
operation elsewhere, rather than creating a second unbounded API path.

No schemas are fetched from external URLs, and validation does not coerce inputs
or apply defaults. A bundle contains at most 16 tools; each schema is at most
16 KiB and descriptions at most 500 bytes. The registry supports up to 256 tools
and 64 bundles. Split large catalogues into purpose-specific bundles rather than
advertising every company system to every agent.

## Hard limits and audit behavior

Default per-call limits are 16 KiB input, 16 KiB output, 20 record units and a
10-second deadline. Configurable ceilings are 64 KiB input, 128 KiB output,
1,000 record units and 60 seconds. Record units count **all array elements
recursively**, with a minimum of one per successful result; nested arrays cannot
hide additional extraction. Bytes measure the approved JSON representation before
MCP text/structured-content packaging, which can duplicate/escape that data.

The default per-user rolling-hour budget is 60 invocations, 1 MiB of returned JSON,
1,000 record units and two concurrent calls. It spans tools, bundles, API clients
and MCP transports. Worst-case bytes/records are reserved atomically before
dispatch, then replaced with actual delivered units on completion. Failed calls
still consume invocation credits. Invalid/denied requests are audited without
dispatch. Abandoned pending reservations retain conservative extraction charges
for the rolling window; concurrency leases expire after the call deadline plus a
minute, allowing recovery after process crashes.

Handlers must respect their context and use bounded, cancelable upstream work.
Hex rejects a late result, but Go cannot forcibly stop an uncooperative in-process
adapter. Enforce vendor-side page/date/cost bounds before fetching. For BigQuery,
use approved views, parameterized reports and `maximumBytesBilled`; a response row
limit alone does not constrain scanning costs. `ReadIntegrationJSON` helps adapters
cap upstream bodies at at most 1 MiB, propagate cancellation and reject redirects.
It is not a generic agent-accessible fetch tool or a substitute for source policy.

Audit records contain user ID, integration/tool/version, bundle, transport,
authorized resource, timestamps, duration, status, byte/record counts and an input
hash. They contain no request/result bodies, tokens or vendor error strings.
HTTP and the stdio bridge share the `http` execution path; remote MCP records
`mcp-http`. Transport labels are not authentication or client attestation.

State/audit failures fail closed: a result is not returned until its completion is
stored. Unexpected vendor failures, output-schema errors and oversized results
produce bounded generic errors without reflecting raw upstream data. Audit access
requires the explicit principals supplied to `NewIntegrationRuntime`, independently
of platform-admin status. Operators can retain/prune historical `hex_tool_calls`
according to company policy; do not remove active or recent quota reservations.

Retrieved notes/issues/feedback remain untrusted data. Tool permissions and
budgets still apply if their contents induce an agent to request another action.
Data classification, redaction and approved AI-client/model choices belong in the
adapter and company deployment policy, before releasing data to a model.

## CLI and stdio MCP onboarding

After ordinary platform setup and `hex login`:

```sh
hex integrations list --platform company
hex tools list --platform company --bundle engineering
hex tools describe sentry_top_issues --platform company --bundle engineering
hex tools run sentry_top_issues --platform company --bundle engineering --input @issues.json
hex integrations audit --platform company --limit 50
hex mcp config --platform company --bundle engineering
```

The `config` command prints a non-secret, common MCP client configuration:

```json
{
  "mcpServers": {
    "company-hex": {
      "command": "hex",
      "args": ["mcp", "serve", "--bundle", "engineering", "--platform", "company"]
    }
  }
}
```

The bridge uses the saved platform sign-in, refreshes discovery on `tools/list`,
validates current contracts before invoking and sends every execution to the
backend for repeat authorization and shared quotas. MCP stdout contains only
protocol messages; diagnostics use stderr and interactive login is disabled.
Perform initial/re-authentication with `hex login` in a terminal. This avoids
separate vendor installations or credentials on each employee's machine.

## Optional remote MCP

Set `Config.IntegrationMCP` after validating an `IntegrationMCPConfig` containing
the canonical resource URL, expected audience, authorization-server URLs and
required baseline scopes. The server exposes `/mcp/<bundle>` using the official
Go MCP SDK, stateless Streamable HTTP with JSON responses. It exposes tools only,
not arbitrary resources, prompts, sampling or subprocess execution. Requests have
a 128 KiB transport cap. Every request resolves the authenticated user again and
checks bearer presence, audience, scopes, configured platform host and Origin.
Sessions never confer identity; application hostnames cannot access this endpoint.

The existing hosting gateway must authenticate and validate access tokens for the
configured audience/issuer, strip incoming identity headers and forward trusted
`aud` and `scp`/`scope` claims through the resolver. A static development identity
is not production authentication. Custom resolvers populate `Identity.Audiences`
and `Identity.Scopes`; Easy Auth resolves these claims, including the mapped scope
claim URI. User/site-owner roles alone do not satisfy integration grants.

`/.well-known/oauth-protected-resource` publishes only the non-secret resource,
issuer and baseline-scope metadata. Unauthorized MCP responses include a
`WWW-Authenticate` resource-metadata challenge. Expose this discovery path without
browser sign-in, while keeping **all `/mcp/` paths authenticated**. The nginx image
has an explicit metadata proxy before its hidden-path rejection. The Azure hosting
module's `mcp_authorization_discovery` defaults to false; opt in to exclude only
the metadata path. Existing deployments retain their default authentication rules.

Hex does not implement an OAuth authorization server, token exchange or dynamic
client registration. Remote clients need compatible issuer discovery, registered
client/redirect configuration, API-style bearer authentication and resource/scope
support from the company's identity/gateway layer. Existing browser-login redirects
alone are insufficient for seamless OAuth MCP onboarding. Preconfigured bearer
clients work with the authenticated endpoint; use the CLI bridge where current
gateway/client OAuth interoperability is not available. Upstream tokens remain
separate and are never passed through from the Hex client.

## HTTP endpoints and rollout

| Method | Route | Meaning |
| --- | --- | --- |
| GET | `/api/hex/integrations` | Bundles permitted for the caller |
| GET | `/api/hex/integrations/bundles/<bundle>/tools` | Curated caller-specific contracts |
| GET | `/api/hex/integrations/bundles/<bundle>/tools/<tool>` | One permitted contract |
| POST | Same tool route | Validated, bounded invocation; requires `X-Hex-Request: 1` |
| GET | `/api/hex/integrations/audit` | Explicitly authorized audit metadata; `user`, `tool`, `limit`, `before` filters |
| POST | `/mcp/<bundle>` | Optional authenticated MCP transport; no REST marker requirement |
| GET | `/.well-known/oauth-protected-resource` | Optional public OAuth resource metadata |

All integration REST endpoints are platform-host-only and use normal Hex origin
checks. Agent bundle selection cannot expand permissions. Capabilities advertise
`integrations` and `mcp`; both remain disabled without configured integration
runtime/identity, and remote MCP requires its additional configuration.

Release the updated framework/server and CLI to distribute this functionality.
Deploy the updated nginx image for remote discovery. The browser client change is
optional capability typing only. Start with reviewed read-only pilots, then add
vendor adapters individually; write workflows and server-approved mutation flows
are a subsequent feature rather than an unrestricted tool annotation.

Verification includes real SDK stdio/remote clients, permission/resource bypass
attempts, cached-tool revocation, shared concurrent PostgreSQL reservations,
response/schema/deadline bounds, fail-closed audit delivery and CLI validation.

# Integrations, grants and connected accounts

Integrations give apps typed, server-side access to third-party systems such as GitHub, Slack or a CRM. The platform registers each integration in Go with named **endpoints**; apps, the CLI, AI tool use and automations call those endpoints, and the server calls the third-party API itself. Apps never see credentials and never get a raw proxy: every endpoint has a JSON Schema input contract, returns a shaped result, and is authorized on every call.

Access is decided in three layers, all of which must pass:

1. **The site.** The caller must be able to view the site the call is made from (its [access policy](access-control.md)); endpoints marked `Write` also need the site's editor role.
2. **The caller's grant.** Each endpoint has a permission, `<integration>.<endpoint>` by default. `Config.IntegrationGrants` gives principals permissions. Grants are separate from site access: they decide which *data* a person may reach, whichever app they use.
3. **Approval, when required.** Integrations registered with `RequiresApproval` (for example a CRM) additionally need a platform admin to approve each site that uses them.

Endpoints call with either a **platform credential** the integration owns (the default) or, when the integration names a **connector**, with the caller's **own account**, which they connect once through the platform's OAuth broker. Use connected accounts wherever the third-party system has its own per-user permissions that a shared credential would bypass (Confluence page restrictions, Azure DevOps area paths, personal tasks).

## Grants

Grants map principals to permission patterns. Principals use the [access-control syntax](access-control.md#concepts) plus two additions:

| Principal | Matches |
| --- | --- |
| `*` | Every signed-in user and every automation |
| `user:<id>`, `group:<id>`, `role:<value>` | That identity, group claim or app role claim |
| `site:<name>` | Automations of that site (never people) |

Patterns are exact permissions (`hubspot.deals`), prefixes (`hubspot.*`) or `*`. Platform admins get no implicit grants. With the reference configuration, grants are a JSON object in `HEX_INTEGRATION_GRANTS`:

```json
{
  "*": ["slack.users", "github.members", "ai"],
  "role:hubspot.read": ["hubspot.owners", "hubspot.companies", "hubspot.tickets"],
  "role:hubspot.deals": ["hubspot.deals", "hubspot.deal"],
  "role:sentry.read": ["sentry.projects", "sentry.issues"],
  "site:sdi-report": ["sentry.issue-counts", "slack.post-message"]
}
```

### Entra ID app roles

With Easy Auth, define **app roles** on the platform's app registration and assign them to groups on the enterprise application; their values arrive as `role:` claims. Roles never overage, so they are the recommended way to drive grants. Name roles after the integration they open up: `<integration>.read` for its everyday data and a separate role for each more sensitive group of endpoints (`hubspot.deals`, `github.copilot`), rather than one role per endpoint or one per department. Grant each role its endpoints one by one, not with `<integration>.*`, so an endpoint added later stays closed until it is granted; the grant map keeps per-endpoint control in reviewed configuration, while Entra assignments stay coarse. Role changes apply when the person signs in again, and only direct members of an assigned group receive its roles.

## Approvals

A site owner requests approval for an integration that requires it; a platform admin approves or revokes it. Admins requesting approval for a site approve it at once.

```sh
hex integrations request crm --site sales-dashboard --reason "Pipeline overview for sales leads"
hex integrations approvals --status requested   # admins
hex integrations approve crm --site sales-dashboard
hex integrations revoke crm --site sales-dashboard
```

Approvals are stored in the `IntegrationStore` and checked on every call, including AI tool use and automations.

## Calling endpoints

| Method | Path | Result |
| --- | --- | --- |
| GET | `/api/sites/{site}/integrations` | Integrations with approval and connection status and every endpoint's contract, marked `allowed` for the caller |
| POST | `/api/sites/{site}/integrations/{integration}/{endpoint}` | JSON input (empty means `{}`) → the endpoint's result |
| GET | `/api/hex/integrations` | Catalog of every integration and permission, for grant authors |
| GET | `/api/hex/integration-approvals` | Admins: approvals, optionally `?status=requested` |
| POST/PUT/DELETE | `/api/hex/sites/{site}/integrations/{integration}/approval` | Owners request (POST, `{"reason"}`), admins approve (PUT) and revoke (DELETE); owners may withdraw a pending request |

Errors: 400 invalid input, 403 missing grant, role or approval, 404 unknown endpoint or third-party 404, 409 account not connected (with a `connect` object, see below), 429 third-party rate limiting, 502 other third-party failures, 503 an audited call that could not be recorded, 504 timeouts. Read results may be cached per input for the endpoint's `CacheTTL` (per person for connected accounts); cached answers carry `X-Hex-Cache: hit`. Every call is logged with site, endpoint, caller, duration and whether it was cached; calls of audited integrations are also [recorded](#auditing).

```ts
const deals = await hex.integrations.call('hubspot', 'deals', { pipeline: 'default', limit: 50 });
```

### Typed wrappers

Every endpoint publishes its input and output JSON Schemas in the catalog, and results are validated against the output schema before they leave the server. `hex integrations codegen` turns them into an optional TypeScript module: an interface per input and output (local `$defs` become named types, so recursive results work), and a `typedIntegrations(caller)` function whose wrappers call `hex.integrations.call` with those types. The module imports nothing, so it works with any client version, and untyped calls keep working alongside it.

```sh
hex integrations codegen --out src/hex-integrations.ts                         # every endpoint
hex integrations codegen --only hubspot.*,slack.users --out src/hex-integrations.ts
hex integrations catalog > catalog.json                                         # for builds without platform access
hex integrations codegen --catalog catalog.json --out src/hex-integrations.ts --check
```

Output is deterministic, so `--check` fails a build when the file is stale. Exclude the generated file from formatters, whose line wrapping would otherwise make it differ.

## Auditing

Third-party systems called with a shared platform credential cannot tell who saw what. Integrations registered with `Audit: true` therefore keep their own audit trail in `Config.IntegrationAudit`: one record per call that passed the access checks, with the time, the site, the endpoint, the caller (`user:<id>` or `automation:<site>/<name>`, with their name), the compact input and the records the result showed. Cached answers are recorded too, marked cached, because they show data to a new caller; calls whose handler failed are recorded as failed without records. Calls refused by grants, approvals or input validation never reach data and are only logged.

Auditing fails closed. When a record cannot be written the call fails with 503 and returns no data, and an audited integration refuses every call when no audit store is configured; `hex.New` logs an error for each such integration. The memory and PostgreSQL providers implement the store (PostgreSQL in the `hex_integration_audit` table). `Server.RunBackground` removes records older than `Config.IntegrationAuditRetention`, 365 days by default.

An endpoint's `AuditRecords` names what a result showed. It receives the validated JSON output, also of cached answers, and returns stable IDs such as `ticket:123` or `contact:42`; repeated IDs are dropped and at most 1,000 are kept. Return identifiers only, never names, email addresses or other personal data: the log is kept for a year, and IDs are enough to look the records up in the third-party system. Endpoints without `AuditRecords` are still recorded, without records.

```go
AuditRecords: func(output json.RawMessage) []string {
    var result struct{ Tickets []struct{ ID string `json:"id"` } `json:"tickets"` }
    if err := json.Unmarshal(output, &result); err != nil {
        return nil
    }
    ids := make([]string, 0, len(result.Tickets))
    for _, ticket := range result.Tickets {
        ids = append(ids, "ticket:"+ticket.ID)
    }
    return ids
},
```

Platform admins read the log under **Admin → Integration audit** (`/admin/integration-audit`), which filters by dates, person, site, endpoint and record and downloads the selection as CSV (`?format=csv`, up to 10,000 calls), or through the API:

| Method | Path | Result |
| --- | --- | --- |
| GET | `/api/hex/manage/integration-audit` | Admins: audit records, newest first |

Parameters: `since` and `until` (RFC 3339 times, or dates where `until` includes the whole day), `site`, `caller` (a `user:<id>` or `automation:<site>/<name>` key, the exact name or email of a remembered person, or a user ID), `endpoint` (`<integration>.<endpoint>`, or an integration name for all its endpoints), `record` (one exact record ID) and `limit` (200 by default, at most 1,000). Reading and exporting the log is itself logged with the admin's name.

## Connected accounts

A connector is an OAuth 2.0 authorization server (Atlassian, Google, Microsoft Entra ID for Azure DevOps). The platform runs the authorization code flow with PKCE on its own domain and stores each person's tokens in the `IntegrationStore`, sealed by `Config.CredentialSealer` and bound to the person and connector. It refreshes tokens itself, serializing refreshes per account because some providers rotate refresh tokens. Apps and browsers never see the tokens.

| Sealer | Use |
| --- | --- |
| `server/providers/keyvault` | Production. Envelope encryption: each token gets its own AES-256 data key, wrapped by an RSA key in Azure Key Vault that never leaves the vault. A copy of the database and the server's configuration opens nothing; unwrapping needs the vault, so access can be cut off there. Unwrapped data keys are cached in memory for ten minutes. New tokens use the key's current version, so rotating the key leaves existing connections readable |
| `hex.KeySealer` (`Config.CredentialKey`) | Development: an AES-256 key held in process memory |

Connections unused for `Config.ConnectionIdleExpiry` (90 days by default) count as disconnected and are removed by `Server.RunBackground`. People see their connections, when each was last used, and disconnect them under **Connected accounts** in the portal's account menu; platform admins also see and remove everyone's there, for example when someone leaves. Changing the sealer makes existing connections unreadable, and people connect again.

| Method | Path | Result |
| --- | --- | --- |
| GET | `/api/hex/connections` | The caller's connections |
| GET | `/api/hex/connections/{connector}/start?return=<url>` | Browser navigation to the provider; `return` must be the platform or one of its sites |
| GET | `/api/hex/connections/{connector}/callback` | Provider redirect; register `{platform}/api/hex/connections/{connector}/callback` with the provider |
| DELETE | `/api/hex/connections/{connector}` | Disconnect |

Calling a connector-backed endpoint without a connection answers 409:

```json
{ "error": "connect your Atlassian account to use this", "connect": { "connector": "atlassian", "title": "Atlassian", "url": "https://hex.example.com/api/hex/connections/atlassian/start" } }
```

The browser client turns this into `HexConnectionRequiredError`; `hex.integrations.connect(error)` opens the connect page in a popup, which closes itself when the connection completes. The CLI opens it with `hex connections connect atlassian`; the browser's platform sign-in is the same identity, so the CLI can use the connection immediately. A connector's `Account` callback names the connected account and can refuse it (for example a personal Google account when a Workspace domain is required).

Automations cannot use connected accounts.

## Registering an integration

```go
registry := new(hex.IntegrationRegistry)
err := registry.Register(hex.Integration{
    Name: "crm", Title: "CRM", RequiresApproval: true,
    Endpoints: []hex.IntegrationEndpoint{{
        Name:        "deals",
        Description: "Open deals, newest first. Filter by owner with ownerId.",
        InputSchema: json.RawMessage(`{"type":"object","properties":{"ownerId":{"type":"string"}},"additionalProperties":false}`),
        CacheTTL:    2 * time.Minute,
        Handler: func(ctx context.Context, call hex.IntegrationCall, input json.RawMessage) (any, error) {
            request, err := hex.NewJSONRequest(ctx, http.MethodGet, crmURL+"/deals", nil)
            if err != nil {
                return nil, err
            }
            var deals []Deal
            return deals, hex.DoJSON(crmClient, request, "CRM", &deals)
        },
    }},
})
config.Integrations = registry
```

Both schemas are required. Describe results precisely, including the properties of list items: they become the app's generated types. Descriptions are shown to app developers and to models as tool descriptions, so say what the endpoint returns and when to use it. Return slim, purpose-built results rather than raw third-party payloads, and keep personal data to what the endpoint is for. `hex.DoJSON` maps third-party 400/404/409/422/429 to statuses the app sees and 401/403 to "refused access"; return `*hex.IntegrationError` for other app-visible failures. Connector-backed handlers use `call.HTTPClient(ctx)`; register the connector first with `registry.RegisterConnector`. Tests can build calls with `hex.NewIntegrationCall`.

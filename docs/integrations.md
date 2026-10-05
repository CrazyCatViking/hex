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
  "role:Data.Sales": ["hubspot.*"],
  "role:Data.Engineering": ["github.*", "sentry.*", "azuredevops.*"],
  "site:sdi-report": ["sentry.issue-counts", "slack.post-message"]
}
```

### Entra ID app roles

With Easy Auth, define **app roles** on the platform's app registration and assign them to groups on the enterprise application; their values arrive as `role:` claims. Roles never overage, so they are the recommended way to drive grants. Grant roles to personas (`Data.Sales`, `Data.Engineering`) rather than creating one role per endpoint: the grant map keeps per-endpoint control in reviewed configuration, while Entra assignments stay coarse. Role changes apply when the person signs in again, and only direct members of an assigned group receive its roles.

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

Errors: 400 invalid input, 403 missing grant, role or approval, 404 unknown endpoint or third-party 404, 409 account not connected (with a `connect` object, see below), 429 third-party rate limiting, 502 other third-party failures, 504 timeouts. Read results may be cached per input for the endpoint's `CacheTTL` (per person for connected accounts); cached answers carry `X-Hex-Cache: hit`. Every call is logged with site, endpoint, caller and duration.

```ts
const deals = await hex.integrations.call('hubspot', 'deals', { pipeline: 'default', limit: 50 });
```

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

Descriptions are shown to app developers and to models as tool descriptions, so say what the endpoint returns and when to use it. Return slim, purpose-built results rather than raw third-party payloads, and keep personal data to what the endpoint is for. `hex.DoJSON` maps third-party 400/404/409/422/429 to statuses the app sees and 401/403 to "refused access"; return `*hex.IntegrationError` for other app-visible failures. Connector-backed handlers use `call.HTTPClient(ctx)`; register the connector first with `registry.RegisterConnector`. Tests can build calls with `hex.NewIntegrationCall`.

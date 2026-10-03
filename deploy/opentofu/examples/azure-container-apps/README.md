# Azure Container Apps example

This is an editable OpenTofu example, not a mandatory Hex stack. Defaults provision **site hosting only**. File APIs, a document database and realtime are independently selectable. All combinations keep the same authenticated gateway and one always-running replica.

## Choose capabilities

```hcl
capabilities = {
  sites    = true
  files    = false
  database = "none"
  realtime = false
}
```

| Setting | Values | Resources and behavior |
| --- | --- | --- |
| `sites` | `true` / `false` | Azure Files, private endpoint, read-only mounts and the server's publishing roles; enables static hosting, directory-based discovery and publishing through the API. Without it NGINX has no site mount. |
| `files` | `true` / `false` | Separate Blob account/container, private endpoint and managed-identity role; enables app uploads/downloads. |
| `database` | `"none"` | No database and no database API. |
| | `"managed"` | Private Azure PostgreSQL; supply `postgres_password`. |
| | `"external"` | Use `database_url`; creates no database, database subnet or database DNS zone. Operator supplies connectivity. |
| `realtime` | `true` / `false` | In-process WebSocket broker; no additional Azure service. |

The environment is wired with explicit `HEX_*_PROVIDER` selections. Disabling a cloud capability never silently turns on filesystem uploads or an ephemeral document database. `GET /api/hex/capabilities` reflects the selected providers.

Example profiles are in `profiles/`. The local developer launcher continues enabling development capabilities by default; this cloud example intentionally starts smaller.

Managed/external PostgreSQL also enables durable [platform analytics](../../../../docs/analytics.md)
and the loopback nginx traffic collector. Configure `admin_group_ids` to access
`/admin`; site owners get an Analytics tab. The site-only profile has analytics
disabled until storage is configured. Rebuild/deploy both server and nginx images.

## Prerequisites

- OpenTofu 1.11+ and Azure CLI, or an Azure workload identity for CI.
- An Azure subscription, registered resource providers and permissions to create these resources and assign roles.
- Built server and NGINX images, available to Container Apps.
- An existing single-tenant Entra web app registration and client secret. Identity lifecycle is outside this example.

Build from the repository root:

```sh
docker build -t YOUR_REGISTRY/hex-server:0.1.0 .
docker build -f deploy/nginx/Dockerfile -t YOUR_REGISTRY/hex-nginx:0.1.0 .
docker push YOUR_REGISTRY/hex-server:0.1.0
docker push YOUR_REGISTRY/hex-nginx:0.1.0
```

For an existing private ACR, set `container_registry = { id = "<ARM resource ID>", server = "<registry>.azurecr.io" }`. The example assigns AcrPull to its user-assigned identity. This assumes a registry using standard registry RBAC; adapt the role for ABAC-enabled registries. Other private registries require adapting the hosting module's registry/secret configuration.

## Deploy

Copy `terraform.tfvars.example` to `terraform.tfvars` in this directory and edit the IDs, image references, name and capabilities. Supply secrets using `TF_VAR_entra_client_secret`, and, if applicable, `TF_VAR_postgres_password` or `TF_VAR_database_url` through your shell or CI secret store.

Run from this directory:

```sh
az login
tofu init
tofu plan
tofu apply
tofu output url
tofu output redirect_uri
```

To select a profile in addition to your base variables, pass the same profile to both plan and apply:

```sh
tofu plan -var-file=profiles/full.tfvars.example
tofu apply -var-file=profiles/full.tfvars.example
```

For the full profile, supply `TF_VAR_postgres_password`. For `profiles/external-database.tfvars.example`, supply `TF_VAR_database_url` with TLS verification enabled and arrange routing/DNS/firewall access from the Container Apps subnet to that database.

The root example owns a dedicated resource group, VNet, user-assigned identity and hosting environment. To use existing organizational infrastructure, copy the composition and replace these resources/modules with inputs or data sources. The modules themselves accept resource IDs rather than requiring the example's resource group.

## Authentication and exposure

The hosting module creates the Container App with **internal ingress**, creates its Entra auth configuration, then enables external HTTPS ingress. The separate `azapi_update_resource.public_ingress` owns the ingress `external` property; the base app ignores changes to that one property so image updates do not fight the public-ingress resource. Preserve the explicit dependency on the authentication resource when adapting the module.

Add the output `redirect_uri` as a Web redirect URI to the Entra app registration. Configure enterprise-application assignments if access should be restricted to particular employees/groups. There are no authentication exclusions. The Go listener is loopback-only. Storage public network access is disabled by default; both NGINX and Go get read-only SMB mounts when sites are enabled.

The server resolves each caller from the gateway's Easy Auth headers (`HEX_IDENTITY_PROVIDER=easyauth`), which enables `/api/hex/me` and, together with the database capability, [per-site access policies](../../../../docs/access-control.md). Set `admin_group_ids` to the Entra group object IDs that administer every site, and optionally `publisher_group_ids` to limit who may claim new site names. Group claims are not emitted by default: on the app registration either set `groupMembershipClaims` to `ApplicationGroup` and assign the relevant groups to the enterprise application (assigned groups avoid claims overage), or define app roles and use their values in policies. Without the database capability there are no policies: every signed-in user may view, edit and publish every site. Prefer a dedicated app registration for Hex before changing claims configuration shared with other applications.

## Publishing and site domains

The example advertises connection settings at `/api/hex/config` behind the existing Entra authentication. Users run `hex setup <gateway-url>`; if authentication blocks the direct download, they download through the browser and import the file. Set `platform_url` if users connect through a custom gateway hostname rather than the generated Azure URL. It must match the setup origin. No custom Hex CLI Entra registration is introduced. See [Setup](../../../../docs/setup.md).

`hex publish` and `hex delete` go through the Hex API. The server (`HEX_PUBLISHER_PROVIDER=azurefiles`, `AZURE_FILES_SHARE_URL` from the sites module) authorizes site owners and signs per-file upload SAS URLs with its managed identity, which the example grants **Storage File Data Privileged Contributor** and **Storage File Delegator** on the sites account. Publishers need no storage role, but they must reach the account's file endpoint: set `site_storage_public_network_access = true` to accept uploads from outside the VNet (the SAS URLs remain required), or give publishers private-endpoint connectivity. See [Publishing](../../../../docs/publishing.md).

Set `site_base_url = "https://hex.example.com"` for the parent site origin. It configures NGINX's hostname routing and the discovery API's URL generation. In the app project, set `siteBaseURL` to this value when `server` uses the Azure-generated gateway hostname.

Before browsing a site such as `demo.hex.example.com`, configure its DNS, certificate and Container Apps hostname binding. The `custom_domains` input accepts `{ name, certificate_id }` objects referring to certificates in this environment; `environment_id` is output for certificate provisioning. Add the corresponding `custom_domain_redirect_uris` to the Entra registration. DNS and certificates are operator-managed. Initially deploying with no bindings is allowed, but custom site URLs will not yet work.

This example uses explicit domain bindings/callbacks; it does not assume a wildcard DNS record enables wildcard Container Apps routing or Entra redirects. See [Subdomain hosting](../../../../docs/subdomains.md) for setup and the distinction between hosting configuration and publishing.

The base domain now serves the built-in Go/HTMX landing page. For example, set both `platform_url` and `site_base_url` to `https://hex.smartdok.dev`, then configure DNS, TLS, an ingress binding, and the Entra callback for that main domain alongside site domains. Employees use its platform-configured installer downloads rather than manual CLI setup. Set optional `cli_release_url` for an organization mirror; otherwise installers use the latest public CLI release. See [landing page and installers](../../../../docs/portal.md).

The CLI calls the API, including for publishing, with Entra access tokens and signs in itself; employees need no Azure CLI. The example advertises the gateway registration as the CLI's sign-in app (`HEX_API_RESOURCE=api://<entra_client_id>`, `HEX_CLI_CLIENT_ID`, `HEX_CLI_TENANT_ID`), so the CLI requests a token for its own API without extra consent. On that registration, set the Application ID URI to `api://<client-id>` and expose an API scope, allow public client flows, add a **Mobile and desktop applications** redirect URI `http://localhost`, and use v2 access tokens to match the issuer. Setting `api_resource` to another API advertises it without a sign-in app; the CLI then uses Azure CLI, which must be pre-authorized (`04b07795-8ddb-461a-bbee-02f9e1bf7b46`) for that API's scope. Users can also supply a token through `HEX_TOKEN`. See [Publishing](../../../../docs/publishing.md#authentication).

Before destroying a deployed environment, disable its Container App ingress (for example `az containerapp ingress disable --name <name>-gateway --resource-group <name>-example`) before `tofu destroy`. Authentication and application deletion are separate Azure operations, and the authentication resource is removed first during teardown. Do not publish data into a partially configured or unverified deployment.

## Capacity and storage

`minReplicas = maxReplicas = 1`; the example does not scale to zero. Go and NGINX together reserve 0.75 vCPU and 1.5 GiB. Keep one replica while using the in-memory realtime provider. A different broker and hosting composition can support scale-out later.

Site storage uses an explicitly selected `Hot` Azure Files tier, configurable through `site_access_tier`; it is not implicitly transaction-optimized. `site_quota_gib` defaults to 100. Uploads use Blob Hot storage. Read-only SMB mount UID/GID values match the provided NGINX and non-root Go images. Azure Files keys and database credentials are stored in infrastructure state, as described in the [OpenTofu overview](../../README.md).

Private endpoints and storage accounts exist only for selected storage capabilities. PostgreSQL is often the largest fixed cost and is not provisioned by default. This example does not provision an ACR, Log Analytics workspace, managed broker, or application monitoring stack.

## Validation

```sh
tofu init -backend=false
tofu validate
tofu test
```

Tests use a mocked provider and do not create Azure resources. They cover site-only and full configurations, publishing roles and environment, admin and publisher groups, CLI sign-in settings, external PostgreSQL, missing required secrets, always-on scaling, authentication settings and mount access modes. They do not prove live Azure behavior. Verify Entra sign-in, anonymous rejection for assets/APIs/WebSockets, private endpoint routing, SMB visibility, a publication through signed upload URLs, and image pulls in a real subscription before using the example for production.

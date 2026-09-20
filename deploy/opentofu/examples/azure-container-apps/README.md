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
| `sites` | `true` / `false` | Azure Files, private endpoint and mounts; enables publishing/site listing. Without it NGINX has no site mount. |
| `files` | `true` / `false` | Separate Blob account/container, private endpoint and managed-identity role; enables app uploads/downloads. |
| `database` | `"none"` | No database and no database API. |
| | `"managed"` | Private Azure PostgreSQL; supply `postgres_password`. |
| | `"external"` | Use `database_url`; creates no database, database subnet or database DNS zone. Operator supplies connectivity. |
| `realtime` | `true` / `false` | In-process WebSocket broker; no additional Azure service. |

The environment is wired with explicit `HEX_*_PROVIDER` selections. Disabling a cloud capability never silently turns on filesystem uploads or an ephemeral document database. `GET /api/hex/capabilities` reflects the selected providers.

Example profiles are in `profiles/`. The local developer launcher continues enabling development capabilities by default; this cloud example intentionally starts smaller.

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

Add the output `redirect_uri` as a Web redirect URI to the Entra app registration. Configure enterprise-application assignments if access should be restricted to particular employees/groups. There are no authentication exclusions. The Go listener is loopback-only. Storage public network access is disabled; NGINX gets a read-only SMB mount and Go gets a read-write mount when sites are enabled.

For CLI access, configure an exposed API scope and appropriate consent on the Entra registration. Use v2 access tokens to match the issuer. Projects can set `resource: "api://<client-id>"` after authorizing Azure CLI as a client, or supply an appropriate token through `HEX_TOKEN`. Hex does not implement identity or claims APIs.

Before destroying a deployed environment, disable its Container App ingress (for example `az containerapp ingress disable --name <name>-gateway --resource-group <name>-example`) before `tofu destroy`. Authentication and application deletion are separate Azure operations, and the authentication resource is removed first during teardown. Do not publish data into a partially configured or unverified deployment.

## Capacity and storage

`minReplicas = maxReplicas = 1`; the example does not scale to zero. Go and NGINX together reserve 0.75 vCPU and 1.5 GiB. Keep one replica while using the in-memory realtime provider. A different broker and hosting composition can support scale-out later.

Site storage uses an explicitly selected `Hot` Azure Files tier, configurable through `site_access_tier`; it is not implicitly transaction-optimized. `site_quota_gib` defaults to 100. Uploads use Blob Hot storage. SMB mount UID/GID values match the provided NGINX and non-root Go images. Azure Files keys and database credentials are stored in infrastructure state, as described in the [OpenTofu overview](../../README.md).

Private endpoints and storage accounts exist only for selected storage capabilities. PostgreSQL is often the largest fixed cost and is not provisioned by default. This example does not provision an ACR, Log Analytics workspace, managed broker, or application monitoring stack.

## Validation

```sh
tofu init -backend=false
tofu validate
tofu test
```

Tests use a mocked provider and do not create Azure resources. They cover site-only and full configurations, external PostgreSQL, missing required secrets, always-on scaling, authentication settings and mount access modes. They do not prove live Azure behavior. Verify Entra sign-in, anonymous rejection for assets/APIs/WebSockets, private endpoint routing, SMB visibility, and image pulls in a real subscription before using the example for production.

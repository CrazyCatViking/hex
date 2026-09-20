# OpenTofu deployment examples

Infrastructure in this directory is example composition, not part of the Hex framework or a required installation mechanism. Copy it into your infrastructure repository, select modules, and adapt the networking, naming, state backend and operating policies to your organization.

The initial example is [Azure Container Apps](examples/azure-container-apps/README.md). It keeps one replica running, uses Entra-managed authentication, and serves static files directly from NGINX. Provider-specific modules live under `modules/azure/`; a future GCP deployment should have its own composition rather than adding GCP conditionals to Azure modules.

## Boundaries

| Layer | Responsibility |
| --- | --- |
| `server/` | Provider-neutral HTTP APIs and capability interfaces |
| `server/providers/` | Implementations for specific storage/database/broker services |
| `cmd/hex-server/` | Example executable selecting providers from environment variables |
| `modules/azure/` | Concrete Azure resources; no application business logic |
| `examples/azure-container-apps/` | Chooses resources, connects outputs, selects enabled runtime capabilities |

No resource identifiers, credentials, or cloud SDKs are required by the browser client. The CLI can use an operator-supplied `HEX_TOKEN`; its optional Entra/Azure CLI token acquisition is a convenience, not a server requirement. Authentication enforcement belongs to the host.

## Modules

All Azure modules accept a resource group ID and deployment location; modules do not configure their own providers or state backends. The example owns the resource group and passes an inherited AzAPI provider. These modules target Azure public cloud.

| Module | Creates | Integration outputs |
| --- | --- | --- |
| `azure/network` | VNet and Container Apps/private endpoint subnets; optional PostgreSQL subnet | Network and subnet IDs |
| `azure/private-storage` | LRS storage account, private endpoint, DNS zone and VNet link for one service | Storage account ID/name |
| `azure/sites` | Private Azure Files account and an explicitly Hot SMB share | Read-only mount binding, reference-server environment, direct CLI publishing destination |
| `azure/files` | Private Blob account/container and scoped data-access role assignment | Reference-server environment |
| `azure/postgres` | Private PostgreSQL server/database and private DNS | Sensitive connection string |
| `azure/container-apps` | Environment, optional site mounts, two-container gateway, Entra authentication | Public URL and callback URI |

`azure/private-storage` owns the private DNS zone for its selected service. Instantiate it once per service in a resource group; for an organization with shared DNS zones, adapt that module to accept existing zone IDs instead. The example uses separate storage accounts for site files and uploads so capabilities can be independently selected.

The hosting module accepts `server_environment`, `server_secrets`, and an optional `site_mount`. It does not provision PostgreSQL or Blob Storage. You can therefore pass outputs from these modules, supply bindings for existing services, or replace a capability module entirely. The reference example demonstrates an existing PostgreSQL connection with `database = "external"`.

Site publication is CLI-to-storage, without a Hex API call. Runtime site mounts are read-only. Hosting uses a subdomain per site; `site_base_url` and operator-supplied `custom_domains` bindings describe the domain setup. See [subdomain configuration](../../docs/subdomains.md) and [direct publishing](../../docs/publishing.md). Discovery enumerates directories and does not use infrastructure bindings as a site catalogue.

## State and lifecycle

No backend is hardcoded. For a shared deployment, configure your organization's encrypted, access-controlled remote state with locking before applying. The example's local state is for evaluation only. State and saved plans can contain the Entra client secret, storage account keys and database credentials; `sensitive` redacts CLI output but does not encrypt those files. State, plans and real variable files are gitignored. Keep the dependency lock file in version control.

Changing a provisioned capability to disabled removes its resources from the plan and can destroy its data. To disable only its API while retaining resources, separate resource provisioning from runtime selection in your composition, or retain the module and pass `none` for its provider. Review plans before applying to a populated environment. The examples do not create backup or retention policies beyond the managed PostgreSQL backup setting.

The old Bicep templates have been removed. If they were already deployed, do not apply this example over them assuming state is shared: import and adapt the existing resources into OpenTofu or deploy into a separate environment. This example uses a different resource layout, including separate storage accounts.

See [Hosting contract](../../docs/hosting.md) for what an alternative Azure, GCP, AWS or self-hosted deployment must supply.

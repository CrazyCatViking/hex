locals {
  suffix = substr(sha256("${var.subscription_id}/${var.name}"), 0, 12)
}

resource "azapi_resource" "group" {
  type      = "Microsoft.Resources/resourceGroups@2024-03-01"
  name      = "${var.name}-example"
  parent_id = "/subscriptions/${var.subscription_id}"
  location  = var.location
}

module "network" {
  source            = "../../modules/azure/network"
  name              = "${var.name}-network"
  resource_group_id = azapi_resource.group.id
  location          = var.location
  postgres_enabled  = var.capabilities.database == "managed"
}

resource "azapi_resource" "identity" {
  type                   = "Microsoft.ManagedIdentity/userAssignedIdentities@2023-01-31"
  name                   = "${var.name}-server"
  parent_id              = azapi_resource.group.id
  location               = var.location
  response_export_values = ["properties.clientId", "properties.principalId"]
}

module "sites" {
  count             = var.capabilities.sites ? 1 : 0
  source            = "../../modules/azure/sites"
  storage_name      = "hexsites${local.suffix}"
  resource_group_id = azapi_resource.group.id
  location          = var.location
  network_id        = module.network.id
  subnet_id         = module.network.endpoints_subnet_id
  quota_gib         = var.site_quota_gib
  access_tier       = var.site_access_tier
}

module "files" {
  count             = var.capabilities.files ? 1 : 0
  source            = "../../modules/azure/files"
  storage_name      = "hexfiles${local.suffix}"
  resource_group_id = azapi_resource.group.id
  location          = var.location
  network_id        = module.network.id
  subnet_id         = module.network.endpoints_subnet_id
  principal_id      = azapi_resource.identity.output.properties.principalId
  subscription_id   = var.subscription_id
}

module "database" {
  count             = var.capabilities.database == "managed" ? 1 : 0
  source            = "../../modules/azure/postgres"
  name              = "${var.name}-db-${local.suffix}"
  resource_group_id = azapi_resource.group.id
  location          = var.location
  network_id        = module.network.id
  subnet_id         = module.network.database_subnet_id
  password          = var.postgres_password
}

resource "azapi_resource" "registry_access" {
  count     = var.container_registry == null ? 0 : 1
  type      = "Microsoft.Authorization/roleAssignments@2022-04-01"
  name      = uuidv5("url", "${var.container_registry.id}/${azapi_resource.identity.id}/acr-pull")
  parent_id = var.container_registry.id
  body = {
    properties = {
      principalId      = azapi_resource.identity.output.properties.principalId
      principalType    = "ServicePrincipal"
      roleDefinitionId = "/subscriptions/${var.subscription_id}/providers/Microsoft.Authorization/roleDefinitions/7f951dda-4ed3-4680-a7ca-43fe172d538d"
    }
  }
}

locals {
  server_environment = merge(
    {
      HEX_SITES_PROVIDER    = "none"
      HEX_FILES_PROVIDER    = "none"
      HEX_DATABASE_PROVIDER = var.capabilities.database == "none" ? "none" : "postgres"
      HEX_REALTIME_PROVIDER = var.capabilities.realtime ? "memory" : "none"
      AZURE_CLIENT_ID       = azapi_resource.identity.output.properties.clientId
      HEX_PLATFORM_NAME     = var.name
    },
    var.site_base_url == null ? {} : { HEX_SITE_BASE_URL = var.site_base_url },
    var.platform_url == null ? {} : { HEX_PUBLIC_URL = var.platform_url },
    var.capabilities.sites ? module.sites[0].environment : {},
    var.capabilities.files ? module.files[0].environment : {}
  )

  server_secrets = var.capabilities.database == "none" ? {} : {
    DATABASE_URL = var.capabilities.database == "managed" ? module.database[0].connection_string : var.database_url
  }
}

module "hosting" {
  source              = "../../modules/azure/container-apps"
  name                = "${var.name}-gateway"
  resource_group_id   = azapi_resource.group.id
  location            = var.location
  subnet_id           = module.network.apps_subnet_id
  identity_id         = azapi_resource.identity.id
  server_image        = var.server_image
  nginx_image         = var.nginx_image
  entra_tenant_id     = var.entra_tenant_id
  entra_client_id     = var.entra_client_id
  entra_client_secret = var.entra_client_secret
  sites_enabled       = var.capabilities.sites
  site_mount          = var.capabilities.sites ? module.sites[0].mount : null
  server_environment  = local.server_environment
  server_secrets      = local.server_secrets
  registries = var.container_registry == null ? [] : [{
    server   = var.container_registry.server
    identity = azapi_resource.identity.id
  }]

  depends_on     = [azapi_resource.registry_access]
  site_domain    = var.site_base_url == null ? "localhost" : trimsuffix(trimprefix(var.site_base_url, "https://"), "/")
  custom_domains = var.custom_domains
}

output "url" {
  value = module.hosting.url
}

output "redirect_uri" {
  value = module.hosting.redirect_uri
}

output "capabilities" {
  value = var.capabilities
}

output "publishing" {
  value = var.capabilities.sites ? module.sites[0].publishing : null
}

output "site_base_url" {
  value = var.site_base_url
}

output "custom_domain_redirect_uris" {
  value = module.hosting.custom_domain_redirect_uris
}

output "environment_id" {
  value = module.hosting.environment_id
}

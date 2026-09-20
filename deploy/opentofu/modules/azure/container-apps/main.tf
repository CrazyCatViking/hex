terraform {
  required_providers {
    azapi = {
      source  = "Azure/azapi"
      version = "~> 2.0"
    }
  }
}

resource "azapi_resource" "environment" {
  type      = "Microsoft.App/managedEnvironments@2024-03-01"
  name      = "${var.name}-environment"
  parent_id = var.resource_group_id
  location  = var.location

  body = {
    properties = {
      vnetConfiguration = {
        infrastructureSubnetId = var.subnet_id
        internal               = false
      }
      workloadProfiles = [{
        name                = "Consumption"
        workloadProfileType = "Consumption"
      }]
    }
  }
}

resource "azapi_resource" "mount" {
  for_each = var.sites_enabled ? {
    sites          = "ReadWrite"
    sites-readonly = "ReadOnly"
  } : {}

  type      = "Microsoft.App/managedEnvironments/storages@2024-03-01"
  name      = each.key
  parent_id = azapi_resource.environment.id

  body = {
    properties = {
      azureFile = {
        accountName = var.site_mount.account_name
        accountKey  = var.site_mount.account_key
        shareName   = var.site_mount.share_name
        accessMode  = each.value
      }
    }
  }
}

locals {
  environment = merge(var.server_environment, { HEX_ADDR = "127.0.0.1:8081" })

  secrets = concat(
    [{
      name  = "entra-client-secret"
      value = var.entra_client_secret
    }],
    [for name, value in var.server_secrets : {
      name  = lower(replace(name, "_", "-"))
      value = value
    }]
  )

  volumes = var.sites_enabled ? [
    {
      name         = "sites"
      storageType  = "AzureFile"
      storageName  = "sites"
      mountOptions = "dir_mode=0770,file_mode=0660,uid=65532,gid=65532"
    },
    {
      name         = "sites-readonly"
      storageType  = "AzureFile"
      storageName  = "sites-readonly"
      mountOptions = "dir_mode=0550,file_mode=0440,uid=101,gid=101"
    }
  ] : []

  overlapping_environment_keys = setintersection(
    toset(keys(var.server_environment)),
    toset(keys(var.server_secrets))
  )
}

resource "azapi_resource" "app" {
  type      = "Microsoft.App/containerApps@2024-03-01"
  name      = var.name
  parent_id = var.resource_group_id
  location  = var.location

  identity {
    type         = "UserAssigned"
    identity_ids = [var.identity_id]
  }

  body = {
    properties = {
      managedEnvironmentId = azapi_resource.environment.id
      workloadProfileName  = "Consumption"
      configuration = {
        activeRevisionsMode = "Single"
        secrets             = local.secrets
        registries          = var.registries

        ingress = {
          external      = false
          targetPort    = 8080
          transport     = "auto"
          allowInsecure = false
        }
      }
      template = {
        containers = [
          {
            name  = "nginx"
            image = var.nginx_image
            resources = {
              cpu    = 0.25
              memory = "0.5Gi"
            }
            volumeMounts = var.sites_enabled ? [{
              volumeName = "sites-readonly"
              mountPath  = "/mnt/sites"
            }] : []
            probes = [{
              type                = "Readiness"
              initialDelaySeconds = 5
              periodSeconds       = 10
              httpGet = {
                port = 8080
                path = "/healthz"
              }
            }]
          },
          {
            name  = "server"
            image = var.server_image
            resources = {
              cpu    = 0.5
              memory = "1Gi"
            }
            env = concat(
              [for name, value in local.environment : {
                name  = name
                value = value
              }],
              [for name, value in var.server_secrets : {
                name      = name
                secretRef = lower(replace(name, "_", "-"))
              }]
            )
            volumeMounts = var.sites_enabled ? [{
              volumeName = "sites"
              mountPath  = "/mnt/sites"
            }] : []
          }
        ]
        volumes = local.volumes
        scale = {
          minReplicas = 1
          maxReplicas = 1
        }
      }
    }
  }

  lifecycle {
    ignore_changes = [body.properties.configuration.ingress.external]

    precondition {
      condition     = !var.sites_enabled || var.site_mount != null
      error_message = "Site hosting requires an Azure Files mount binding."
    }

    precondition {
      condition     = length(local.overlapping_environment_keys) == 0
      error_message = "A server environment variable cannot also be supplied as a secret."
    }
  }
  depends_on = [azapi_resource.mount]
}

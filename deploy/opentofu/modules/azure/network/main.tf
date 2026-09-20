terraform {
  required_providers {
    azapi = {
      source  = "Azure/azapi"
      version = "~> 2.0"
    }
  }
}

variable "name" {
  type = string
}

variable "resource_group_id" {
  type = string
}

variable "location" {
  type = string
}

variable "address_space" {
  type    = string
  default = "10.80.0.0/16"
}

variable "postgres_enabled" {
  type    = bool
  default = false
}

locals {
  application_subnets = [
    {
      name = "apps"
      properties = {
        addressPrefix = cidrsubnet(var.address_space, 7, 0)
        delegations = [{
          name = "apps"
          properties = {
            serviceName = "Microsoft.App/environments"
          }
        }]
      }
    },
    {
      name = "endpoints"
      properties = {
        addressPrefix                  = cidrsubnet(var.address_space, 8, 2)
        privateEndpointNetworkPolicies = "Disabled"
      }
    }
  ]

  database_subnets = var.postgres_enabled ? [{
    name = "database"
    properties = {
      addressPrefix = cidrsubnet(var.address_space, 8, 3)
      delegations = [{
        name = "database"
        properties = {
          serviceName = "Microsoft.DBforPostgreSQL/flexibleServers"
        }
      }]
    }
  }] : []
}

resource "azapi_resource" "network" {
  type      = "Microsoft.Network/virtualNetworks@2024-05-01"
  name      = var.name
  parent_id = var.resource_group_id
  location  = var.location

  body = {
    properties = {
      addressSpace = { addressPrefixes = [var.address_space] }
      subnets      = concat(local.application_subnets, local.database_subnets)
    }
  }
}

output "id" {
  value = azapi_resource.network.id
}

output "apps_subnet_id" {
  value = "${azapi_resource.network.id}/subnets/apps"
}

output "endpoints_subnet_id" {
  value = "${azapi_resource.network.id}/subnets/endpoints"
}

output "database_subnet_id" {
  value = var.postgres_enabled ? "${azapi_resource.network.id}/subnets/database" : null
}

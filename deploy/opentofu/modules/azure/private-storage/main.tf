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

variable "network_id" {
  type = string
}

variable "subnet_id" {
  type = string
}

variable "service" {
  type = string

  validation {
    condition     = contains(["file", "blob"], var.service)
    error_message = "Storage service must be file or blob."
  }
}

resource "azapi_resource" "account" {
  type      = "Microsoft.Storage/storageAccounts@2023-05-01"
  name      = var.name
  parent_id = var.resource_group_id
  location  = var.location

  body = {
    kind = "StorageV2"
    sku  = { name = "Standard_LRS" }
    properties = {
      minimumTlsVersion        = "TLS1_2"
      supportsHttpsTrafficOnly = true
      allowBlobPublicAccess    = false
      publicNetworkAccess      = "Disabled"
      accessTier               = "Hot"
    }
  }
}

resource "azapi_resource" "zone" {
  type      = "Microsoft.Network/privateDnsZones@2020-06-01"
  name      = "privatelink.${var.service}.core.windows.net"
  parent_id = var.resource_group_id
  location  = "global"
}

resource "azapi_resource" "link" {
  type      = "Microsoft.Network/privateDnsZones/virtualNetworkLinks@2020-06-01"
  name      = var.name
  parent_id = azapi_resource.zone.id
  location  = "global"

  body = {
    properties = {
      registrationEnabled = false
      virtualNetwork      = { id = var.network_id }
    }
  }
}

resource "azapi_resource" "endpoint" {
  type      = "Microsoft.Network/privateEndpoints@2024-05-01"
  name      = var.name
  parent_id = var.resource_group_id
  location  = var.location
  body = {
    properties = {
      subnet = { id = var.subnet_id }
      privateLinkServiceConnections = [{
        name = var.service
        properties = {
          privateLinkServiceId = azapi_resource.account.id
          groupIds             = [var.service]
        }
      }]
    }
  }
}

resource "azapi_resource" "endpoint_zone" {
  type      = "Microsoft.Network/privateEndpoints/privateDnsZoneGroups@2024-05-01"
  name      = "default"
  parent_id = azapi_resource.endpoint.id

  body = {
    properties = {
      privateDnsZoneConfigs = [{
        name = var.service
        properties = {
          privateDnsZoneId = azapi_resource.zone.id
        }
      }]
    }
  }
}

output "id" {
  value      = azapi_resource.account.id
  depends_on = [azapi_resource.link, azapi_resource.endpoint_zone]
}

output "name" {
  value = var.name
}

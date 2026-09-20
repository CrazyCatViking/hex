terraform {
  required_providers {
    azapi = { source = "Azure/azapi", version = "~> 2.0" }
  }
}

variable "name" { type = string }
variable "resource_group_id" { type = string }
variable "location" { type = string }
variable "network_id" { type = string }
variable "subnet_id" { type = string }
variable "administrator" {
  type    = string
  default = "hexadmin"
}
variable "password" {
  type      = string
  sensitive = true
}
variable "sku" {
  type    = string
  default = "Standard_B1ms"
}
variable "sku_tier" {
  type    = string
  default = "Burstable"
}

resource "azapi_resource" "zone" {
  type      = "Microsoft.Network/privateDnsZones@2020-06-01"
  name      = "${var.name}.private.postgres.database.azure.com"
  parent_id = var.resource_group_id
  location  = "global"
}

resource "azapi_resource" "link" {
  type      = "Microsoft.Network/privateDnsZones/virtualNetworkLinks@2020-06-01"
  name      = var.name
  parent_id = azapi_resource.zone.id
  location  = "global"
  body      = { properties = { registrationEnabled = false, virtualNetwork = { id = var.network_id } } }
}

resource "azapi_resource" "server" {
  type      = "Microsoft.DBforPostgreSQL/flexibleServers@2024-08-01"
  name      = var.name
  parent_id = var.resource_group_id
  location  = var.location
  body = {
    sku = { name = var.sku, tier = var.sku_tier }
    properties = {
      version                    = "16"
      administratorLogin         = var.administrator
      administratorLoginPassword = var.password
      storage                    = { storageSizeGB = 32 }
      backup                     = { backupRetentionDays = 7, geoRedundantBackup = "Disabled" }
      network = {
        delegatedSubnetResourceId   = var.subnet_id
        privateDnsZoneArmResourceId = azapi_resource.zone.id
        publicNetworkAccess         = "Disabled"
      }
    }
  }
  response_export_values = ["properties.fullyQualifiedDomainName"]
  depends_on             = [azapi_resource.link]
}

resource "azapi_resource" "database" {
  type      = "Microsoft.DBforPostgreSQL/flexibleServers/databases@2024-08-01"
  name      = "hex"
  parent_id = azapi_resource.server.id
  body      = { properties = { charset = "UTF8", collation = "en_US.utf8" } }
}

output "connection_string" {
  value      = "postgres://${replace(urlencode(var.administrator), "+", "%20")}:${replace(urlencode(var.password), "+", "%20")}@${azapi_resource.server.output.properties.fullyQualifiedDomainName}:5432/hex?sslmode=verify-full"
  sensitive  = true
  depends_on = [azapi_resource.database]
}

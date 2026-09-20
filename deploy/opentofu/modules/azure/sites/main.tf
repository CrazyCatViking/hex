terraform {
  required_providers {
    azapi = { source = "Azure/azapi", version = "~> 2.12" }
  }
}

variable "storage_name" { type = string }
variable "resource_group_id" { type = string }
variable "location" { type = string }
variable "network_id" { type = string }
variable "subnet_id" { type = string }
variable "quota_gib" {
  type    = number
  default = 100
}
variable "access_tier" {
  type    = string
  default = "Hot"
  validation {
    condition     = contains(["Hot", "TransactionOptimized", "Cool"], var.access_tier)
    error_message = "Choose a supported standard Azure Files access tier."
  }
}

module "storage" {
  source            = "../private-storage"
  name              = var.storage_name
  resource_group_id = var.resource_group_id
  location          = var.location
  network_id        = var.network_id
  subnet_id         = var.subnet_id
  service           = "file"
}

resource "azapi_resource" "service" {
  type      = "Microsoft.Storage/storageAccounts/fileServices@2023-05-01"
  name      = "default"
  parent_id = module.storage.id
}

resource "azapi_resource" "share" {
  type      = "Microsoft.Storage/storageAccounts/fileServices/shares@2023-05-01"
  name      = "sites"
  parent_id = azapi_resource.service.id
  body      = { properties = { shareQuota = var.quota_gib, enabledProtocols = "SMB", accessTier = var.access_tier } }
}

data "azapi_resource_action" "keys" {
  type                             = "Microsoft.Storage/storageAccounts@2023-05-01"
  resource_id                      = module.storage.id
  action                           = "listKeys"
  sensitive_response_export_values = ["keys"]
}

output "mount" {
  value = {
    account_name = module.storage.name
    account_key  = data.azapi_resource_action.keys.sensitive_output.keys[0].value
    share_name   = azapi_resource.share.name
  }
  sensitive = true
}

output "environment" {
  value = { HEX_SITES_PROVIDER = "filesystem", HEX_SITES_DIR = "/mnt/sites" }
}

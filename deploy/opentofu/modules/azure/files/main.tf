terraform {
  required_providers {
    azapi = {
      source  = "Azure/azapi"
      version = "~> 2.0"
    }
  }
}

variable "storage_name" {
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

variable "principal_id" {
  type = string
}

variable "subscription_id" {
  type = string
}

module "storage" {
  source            = "../private-storage"
  name              = var.storage_name
  resource_group_id = var.resource_group_id
  location          = var.location
  network_id        = var.network_id
  subnet_id         = var.subnet_id
  service           = "blob"
}

resource "azapi_resource" "service" {
  type      = "Microsoft.Storage/storageAccounts/blobServices@2023-05-01"
  name      = "default"
  parent_id = module.storage.id
}

resource "azapi_resource" "container" {
  type      = "Microsoft.Storage/storageAccounts/blobServices/containers@2023-05-01"
  name      = "uploads"
  parent_id = azapi_resource.service.id

  body = {
    properties = {
      publicAccess = "None"
    }
  }
}

resource "azapi_resource" "access" {
  type      = "Microsoft.Authorization/roleAssignments@2022-04-01"
  name      = uuidv5("url", "${azapi_resource.container.id}/${var.principal_id}/blob-contributor")
  parent_id = azapi_resource.container.id
  body = {
    properties = {
      principalId      = var.principal_id
      principalType    = "ServicePrincipal"
      roleDefinitionId = "/subscriptions/${var.subscription_id}/providers/Microsoft.Authorization/roleDefinitions/ba92f5b4-2d11-453d-a403-e96b0029c9fe"
    }
  }
}

output "environment" {
  value = {
    HEX_FILES_PROVIDER   = "azureblob"
    AZURE_BLOB_ENDPOINT  = "https://${module.storage.name}.blob.core.windows.net/"
    AZURE_BLOB_CONTAINER = azapi_resource.container.name
  }
  depends_on = [azapi_resource.access]
}

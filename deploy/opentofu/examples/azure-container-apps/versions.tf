terraform {
  required_version = ">= 1.11.0, < 2.0.0"
  required_providers {
    azapi = { source = "Azure/azapi", version = "~> 2.12" }
  }
}

provider "azapi" {
  subscription_id = var.subscription_id
  tenant_id       = var.entra_tenant_id
}

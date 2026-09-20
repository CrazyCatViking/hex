variable "subscription_id" {
  type = string
}

variable "entra_tenant_id" {
  type = string
}

variable "entra_client_id" {
  type = string
}

variable "entra_client_secret" {
  type      = string
  sensitive = true
}

variable "server_image" {
  type = string
}

variable "nginx_image" {
  type = string
}

variable "name" {
  type    = string
  default = "hex"

  validation {
    condition     = can(regex("^[a-z][a-z0-9-]{1,14}[a-z0-9]$", var.name))
    error_message = "Use a 3–16 character lowercase deployment name starting with a letter and ending in a letter or digit."
  }
}

variable "location" {
  type    = string
  default = "westeurope"
}

variable "capabilities" {
  type = object({
    sites    = optional(bool, true)
    files    = optional(bool, false)
    database = optional(string, "none")
    realtime = optional(bool, false)
  })
  default = {}

  validation {
    condition     = contains(["none", "managed", "external"], var.capabilities.database)
    error_message = "database must be none, managed, or external."
  }
}

variable "postgres_password" {
  type      = string
  sensitive = true
  default   = null

  validation {
    condition     = var.capabilities.database != "managed" || try(length(var.postgres_password) >= 8, false)
    error_message = "Managed PostgreSQL requires postgres_password of at least 8 characters."
  }
}

variable "database_url" {
  type      = string
  sensitive = true
  default   = null

  validation {
    condition     = var.capabilities.database != "external" || try(length(var.database_url) > 0, false)
    error_message = "External PostgreSQL requires database_url and network access from the Container App."
  }
}

variable "site_quota_gib" {
  type    = number
  default = 100
}

variable "site_access_tier" {
  type    = string
  default = "Hot"
}

variable "container_registry" {
  type = object({
    id     = string
    server = string
  })
  default = null
}

variable "site_base_url" {
  type    = string
  default = null
  validation {
    condition     = !var.capabilities.sites || can(regex("^https://[a-z0-9][a-z0-9.-]*[a-z0-9]/?$", var.site_base_url))
    error_message = "Site hosting requires site_base_url, for example https://hex.example.com, with DNS and certificate bindings configured separately."
  }
}

variable "custom_domains" {
  type = list(object({
    name           = string
    certificate_id = string
  }))
  default = []
}

variable "platform_url" {
  type    = string
  default = null
}

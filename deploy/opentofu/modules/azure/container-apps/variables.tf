variable "name" { type = string }
variable "resource_group_id" { type = string }
variable "location" { type = string }
variable "subnet_id" { type = string }
variable "identity_id" { type = string }
variable "server_image" { type = string }
variable "nginx_image" { type = string }
variable "entra_tenant_id" { type = string }
variable "entra_client_id" { type = string }
variable "entra_client_secret" {
  type      = string
  sensitive = true
}
variable "server_environment" {
  type    = map(string)
  default = {}
}
variable "server_secrets" {
  type      = map(string)
  default   = {}
  sensitive = true
  validation {
    condition     = alltrue([for name in keys(var.server_secrets) : can(regex("^[A-Z][A-Z0-9_]*$", name)) && !contains(["ENTRA_CLIENT_SECRET", "HEX_ADDR"], name)])
    error_message = "Secret environment names must use uppercase letters, digits and underscores; ENTRA_CLIENT_SECRET and HEX_ADDR are reserved."
  }
}
variable "sites_enabled" {
  type    = bool
  default = false
}
variable "site_mount" {
  type = object({
    account_name = string
    account_key  = string
    share_name   = string
  })
  default   = null
  sensitive = true
}
variable "registries" {
  type    = list(object({ server = string, identity = string }))
  default = []
}

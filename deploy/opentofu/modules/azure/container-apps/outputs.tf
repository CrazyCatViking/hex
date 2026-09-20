locals {
  public_url = "https://${azapi_update_resource.public_ingress.output.properties.configuration.ingress.fqdn}"
}

output "url" {
  value = local.public_url
}

output "redirect_uri" {
  value = "${local.public_url}/.auth/login/aad/callback"
}

output "custom_domain_redirect_uris" {
  value = [for domain in var.custom_domains : "https://${domain.name}/.auth/login/aad/callback"]
}

output "environment_id" {
  value = azapi_resource.environment.id
}

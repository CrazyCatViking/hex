resource "azapi_resource" "authentication" {
  type      = "Microsoft.App/containerApps/authConfigs@2024-03-01"
  name      = "current"
  parent_id = azapi_resource.app.id

  body = {
    properties = {
      platform     = { enabled = true }
      httpSettings = { requireHttps = true }

      globalValidation = {
        unauthenticatedClientAction = "RedirectToLoginPage"
        redirectToProvider          = "azureactivedirectory"
        excludedPaths               = []
      }

      identityProviders = {
        azureActiveDirectory = {
          enabled = true
          registration = {
            clientId                = var.entra_client_id
            clientSecretSettingName = "entra-client-secret"
            openIdIssuer            = "https://login.microsoftonline.com/${var.entra_tenant_id}/v2.0"
          }
          validation = {
            allowedAudiences = [var.entra_client_id, "api://${var.entra_client_id}"]
          }
        }
      }
    }
  }
}

resource "azapi_update_resource" "public_ingress" {
  type        = "Microsoft.App/containerApps@2024-03-01"
  resource_id = azapi_resource.app.id

  body = {
    properties = {
      configuration = {
        ingress = {
          external      = true
          targetPort    = 8080
          transport     = "auto"
          allowInsecure = false
          customDomains = [for domain in var.custom_domains : {
            name          = domain.name
            bindingType   = "SniEnabled"
            certificateId = domain.certificate_id
          }]
        }
      }
    }
  }

  response_export_values = ["properties.configuration.ingress.fqdn"]
  depends_on             = [azapi_resource.authentication]
}

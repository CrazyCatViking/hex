mock_provider "azapi" {
  mock_resource "azapi_resource" {
    defaults = {
      id = "/subscriptions/00000000-0000-0000-0000-000000000001/resourceGroups/test/providers/Microsoft.App/containerApps/test"
    }
  }

  mock_resource "azapi_update_resource" {
    defaults = {
      output = {
        properties = {
          configuration = {
            ingress = { fqdn = "hex.example.invalid" }
          }
        }
      }
    }
  }
}

variables {
  name                = "hex-test"
  location            = "westeurope"
  resource_group_id   = "/subscriptions/00000000-0000-0000-0000-000000000001/resourceGroups/test"
  subnet_id           = "/subscriptions/00000000-0000-0000-0000-000000000001/resourceGroups/test/providers/Microsoft.Network/virtualNetworks/test/subnets/apps"
  identity_id         = "/subscriptions/00000000-0000-0000-0000-000000000001/resourceGroups/test/providers/Microsoft.ManagedIdentity/userAssignedIdentities/test"
  entra_tenant_id     = "00000000-0000-0000-0000-000000000002"
  entra_client_id     = "00000000-0000-0000-0000-000000000003"
  entra_client_secret = "test-only-secret"
  server_image        = "example.invalid/hex-server:test"
  nginx_image         = "example.invalid/hex-nginx:test"
}

run "gateway_security_contract" {
  command = plan

  module {
    source = "../../modules/azure/container-apps"
  }

  assert {
    condition = (
      azapi_resource.app.body.properties.template.scale.minReplicas == 1 &&
      azapi_resource.app.body.properties.template.scale.maxReplicas == 1
    )
    error_message = "The example must keep one replica running."
  }

  assert {
    condition = (
      azapi_resource.app.body.properties.configuration.ingress.external == false &&
      azapi_update_resource.public_ingress.body.properties.configuration.ingress.external == true
    )
    error_message = "The gateway must start internal; public ingress is a separate post-authentication step."
  }

  assert {
    condition = (
      azapi_resource.authentication.body.properties.platform.enabled &&
      azapi_resource.authentication.body.properties.globalValidation.unauthenticatedClientAction == "RedirectToLoginPage" &&
      length(azapi_resource.authentication.body.properties.globalValidation.excludedPaths) == 0
    )
    error_message = "All ingress paths must require provider-managed authentication."
  }

  assert {
    condition = (
      local.environment.HEX_ADDR == "127.0.0.1:8081" &&
      length(azapi_resource.mount) == 0 &&
      length(local.volumes) == 0
    )
    error_message = "The backend must be loopback-only and an API-only host must not mount site storage."
  }
}

run "site_mounts" {
  command = plan

  module {
    source = "../../modules/azure/container-apps"
  }

  variables {
    sites_enabled = true
    site_mount = {
      account_name = "teststorage"
      account_key  = "test-only-key"
      share_name   = "sites"
    }
  }

  assert {
    condition = (
      azapi_resource.mount["sites-readonly"].body.properties.azureFile.accessMode == "ReadOnly" &&
      azapi_resource.mount["sites"].body.properties.azureFile.accessMode == "ReadOnly"
    )
    error_message = "NGINX and the site-discovery API must both get read-only mounts."
  }
}

run "subdomain_bindings" {
  command = plan

  module {
    source = "../../modules/azure/container-apps"
  }

  variables {
    site_domain = "hex.example.com"
    custom_domains = [{
      name           = "demo.hex.example.com"
      certificate_id = "/subscriptions/00000000-0000-0000-0000-000000000001/resourceGroups/test/providers/Microsoft.App/managedEnvironments/test/certificates/sites"
    }]
  }

  assert {
    condition = (
      azapi_resource.app.body.properties.template.containers[0].env[0].value == "hex.example.com" &&
      azapi_resource.app.body.properties.configuration.ingress.customDomains[0].name == "demo.hex.example.com" &&
      azapi_update_resource.public_ingress.body.properties.configuration.ingress.customDomains[0].name == "demo.hex.example.com"
    )
    error_message = "NGINX and Azure ingress must agree on site hostnames, including after public ingress is enabled."
  }
}

mock_provider "azapi" {
  mock_resource "azapi_resource" {
    defaults = {
      id = "/subscriptions/00000000-0000-0000-0000-000000000001/resourceGroups/test"
      output = {
        properties = {
          clientId                 = "00000000-0000-0000-0000-000000000004"
          principalId              = "00000000-0000-0000-0000-000000000005"
          fullyQualifiedDomainName = "test.postgres.database.azure.com"
        }
      }
    }
  }
  mock_data "azapi_resource_action" {
    defaults = { sensitive_output = { keys = [{ value = "test-only-storage-key" }] } }
  }
  mock_resource "azapi_update_resource" {
    defaults = { output = { properties = { configuration = { ingress = { fqdn = "hex.example.invalid" } } } } }
  }
}

variables {
  subscription_id     = "00000000-0000-0000-0000-000000000001"
  entra_tenant_id     = "00000000-0000-0000-0000-000000000002"
  entra_client_id     = "00000000-0000-0000-0000-000000000003"
  entra_client_secret = "test-only-secret"
  server_image        = "example.invalid/hex-server:test"
  nginx_image         = "example.invalid/hex-nginx:test"
}

override_resource {
  target = azapi_resource.identity
  values = {
    id = "/subscriptions/00000000-0000-0000-0000-000000000001/resourceGroups/test/providers/Microsoft.ManagedIdentity/userAssignedIdentities/test"
    output = { properties = {
      clientId    = "00000000-0000-0000-0000-000000000004"
      principalId = "00000000-0000-0000-0000-000000000005"
    } }
  }
}

run "sites_only" {
  command = plan

  assert {
    condition     = length(module.sites) == 1 && length(module.files) == 0 && length(module.database) == 0
    error_message = "The default example must provision site storage without uploads or PostgreSQL."
  }
  assert {
    condition     = local.server_environment.HEX_SITES_PROVIDER == "filesystem" && local.server_environment.HEX_FILES_PROVIDER == "none" && local.server_environment.HEX_DATABASE_PROVIDER == "none" && local.server_environment.HEX_REALTIME_PROVIDER == "none"
    error_message = "Disabled capabilities must be explicitly disabled in the reference server."
  }
  assert {
    condition     = length(local.server_secrets) == 0
    error_message = "Site-only hosting must not carry database credentials."
  }
}

run "full_platform" {
  command = plan
  variables {
    capabilities      = { sites = true, files = true, database = "managed", realtime = true }
    postgres_password = "test-only-password"
  }
  assert {
    condition     = length(module.sites) == 1 && length(module.files) == 1 && length(module.database) == 1
    error_message = "Full configuration must provision every selected capability."
  }
  assert {
    condition     = local.server_environment.HEX_FILES_PROVIDER == "azureblob" && local.server_environment.HEX_DATABASE_PROVIDER == "postgres" && local.server_environment.HEX_REALTIME_PROVIDER == "memory"
    error_message = "Selected capability providers must be wired into the server."
  }
}

run "api_only_external_database" {
  command = plan
  variables {
    capabilities = { sites = false, files = false, database = "external", realtime = false }
    database_url = "postgres://user:password@private-db.example/hex?sslmode=verify-full"
  }
  assert {
    condition     = length(module.sites) == 0 && length(module.files) == 0 && length(module.database) == 0
    error_message = "External database composition must not create managed storage or a database."
  }
  assert {
    condition     = local.server_environment.HEX_SITES_PROVIDER == "none" && local.server_environment.HEX_DATABASE_PROVIDER == "postgres" && local.server_secrets.DATABASE_URL == var.database_url
    error_message = "An external PostgreSQL connection must be passed as a secret."
  }
}

run "missing_external_database_url" {
  command = plan
  variables { capabilities = { database = "external" } }
  expect_failures = [var.database_url]
}

run "missing_managed_database_password" {
  command = plan
  variables { capabilities = { database = "managed" } }
  expect_failures = [var.postgres_password]
}

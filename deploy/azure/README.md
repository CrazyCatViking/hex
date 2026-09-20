# Azure hosting example

The Azure deployment has moved to [the composable OpenTofu example](../opentofu/examples/azure-container-apps/README.md). Bicep is no longer used.

The initial hosting mechanism remains Container Apps with Azure-managed Entra authentication, direct NGINX static serving from Azure Files, and an internal Go API. The example defaults to site hosting; Blob uploads, managed or existing PostgreSQL, and realtime are selectable capabilities.

See [OpenTofu modules and composition](../opentofu/README.md) for extending the example or bringing existing resources, and [the hosting contract](../../docs/hosting.md) for deploying Hex on another platform.

# Subdomain hosting

Each published site has its own browser origin:

```text
https://one.hex.example.com/ → public/sites/one/
https://two.hex.example.com/ → public/sites/two/
```

NGINX selects the directory from the hostname, never from a client-supplied filesystem path. Names must be DNS labels: 1–63 lowercase letters, digits or hyphens, starting and ending with a letter or digit. Existing directories with uppercase letters or underscores need renaming before they can be used as site hostnames.

The gateway does not provide the old shared `/sites/<name>/` web routes. A site can use root-relative URLs such as `/app.js`. `/api/`, `/healthz`, and the hosting platform's authentication endpoints are reserved. API and WebSocket requests stay on the site's origin and are proxied to the shared backend. Keep the browser client's default same-origin base URL.

## Configuration

| Component | Setting | Example |
| --- | --- | --- |
| NGINX image | `HEX_SITE_DOMAIN` | `hex.example.com` |
| Reference server | `HEX_SITE_BASE_URL` | `https://hex.example.com` |
| Embedded Go server | `Config.SiteBaseURL` | `https://hex.example.com` |
| CLI project | `siteBaseURL` | `https://hex.example.com` |

The CLI falls back to its `server` origin if `siteBaseURL` is absent. The server defaults to `http://localhost:8080` for development. Public site URLs are always derived from this configured base and the directory name, not from an untrusted Host header.

The NGINX Docker image generates its configuration from `internal/cli/assets/nginx.conf.template` at startup. The Go CLI embeds and uses the same template, so it works outside the Hex checkout. The image's domain validation/escaping script runs before the official envsubst step. Unrecognized hosts cannot serve static assets. The configured base domain forwards to Go for the built-in landing page, with API access under `/api/`; each site subdomain still serves its own files directly.

## Browser boundaries

Different hostnames isolate localStorage, sessionStorage, IndexedDB and service-worker scopes. NGINX also sends `Origin-Agent-Cluster: ?1` for static responses. Sibling subdomains can still be same-site for cookie policy, and a cookie deliberately scoped to the parent domain can be shared. Use host-scoped cookies for app-specific state.

Origin separation is not backend authorization. The current shared APIs allow admitted users to access all site namespaces. Hosting authentication must cover every site's assets, API calls and WebSocket upgrades, and the backend must have no bypassing public listener.

## Local development

`hex dev` serves the landing page at `http://localhost:8080/`, sites at `http://<name>.localhost:8080/`, and the management API at `http://localhost:8080/api/`. `npm run dev` in the framework repository delegates to the same tooling. Many browsers resolve the entire `.localhost` suffix to loopback; environments that do not need local DNS/hosts entries. Keep all sites on the same development port to test hostname isolation, rather than relying on different ports.

Run the actual browser-isolation check with:

```sh
npx playwright install chromium
npm run test:browser
```

It starts NGINX and Go, opens two site origins in the same Chromium browser context, and verifies independent localStorage, sessionStorage and IndexedDB data. The ordinary end-to-end test checks host-based file routing, unknown-host rejection, same-origin API/WebSocket access, and direct publication while Go is stopped.

## Azure hosting setup

NGINX routing alone does not make a hostname reachable through Container Apps. Configure DNS, a Container Apps custom-domain binding, a matching TLS certificate, and Entra redirect handling. A wildcard DNS record or certificate alone does not perform the other steps.

The OpenTofu example accepts:

```hcl
site_base_url = "https://hex.example.com"
custom_domains = [{
  name           = "demo.hex.example.com"
  certificate_id = "/subscriptions/SUB/resourceGroups/RG/providers/Microsoft.App/managedEnvironments/ENV/certificates/sites"
}]
```

Certificate IDs must refer to certificates in that Container Apps environment. The example outputs `environment_id` so operators can upload their certificate, and preserves bindings when enabling public ingress. Configure required DNS ownership verification before applying bindings. Certificates and DNS are operator-managed in this example. An initial apply with no custom domains can establish the environment; custom site URLs will not work until their bindings are configured.

For the standard Easy Auth flow, register each site's exact `https://<site-host>/.auth/login/aad/callback` Web redirect URI in the Entra application. The example outputs `custom_domain_redirect_uris` as a configuration aid. Entra documents wildcard redirect URIs for work/school-only registrations, but strongly recommends exact URIs; do not assume wildcard DNS supplies wildcard authentication. A wildcard-domain hosting design needs separate validation of ingress support and authentication behavior. No custom authentication broker or broad parent-domain session cookie is added to Hex.

Domain/TLS/Entra onboarding is hosting configuration, not a site catalogue or part of `hex publish`. Publishing only writes files. The directory-discovery API can enumerate a site before its hostname is configured, so operators must arrange hosting coverage for the names they intend to publish.

Live Azure DNS, certificate bindings and Entra sign-in have not been tested in this repository. Verify anonymous rejection on every bound hostname before production use.

References: [Container Apps custom domains](https://learn.microsoft.com/en-us/azure/container-apps/custom-domains-certificates), [Entra redirect URI restrictions](https://learn.microsoft.com/en-us/entra/identity-platform/reply-url#restrictions-on-wildcards-in-redirect-uris).

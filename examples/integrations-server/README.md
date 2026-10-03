# Curated integration platform example

This executable exposes two **fixture** integrations through one company Hex
connection. It makes no Sentry/BigQuery requests and needs no vendor credentials.

From the repository root:

```sh
go install ./cmd/hex
hex dev --package ./examples/integrations-server --data-dir .hex-integrations
```

In another terminal:

```sh
hex setup http://localhost:8080 --name company
hex integrations list --platform company
hex tools list --platform company --bundle engineering
hex tools describe sentry_top_issues --platform company --bundle engineering
```

Save `{"project":"web","days":7,"limit":10}` as `issues.json`, then:

```sh
hex tools run sentry_top_issues --platform company --bundle engineering --input @issues.json
hex integrations audit --platform company
hex mcp config --platform company --bundle engineering
```

The last command prints a non-secret MCP client configuration launching
`hex mcp serve --platform company --bundle engineering`. Sign in with `hex login`
first for a production platform; MCP stdout is reserved for protocol messages.

`engineering` exposes issue summaries and usage aggregates; `product` exposes
only usage aggregates. The default local user is explicitly granted both. In
production, replace fixture handlers and the `local-dev` grants with approved
adapters and actual group/role IDs. Site owners and Hex admins receive no implicit
integration access. Use a PostgreSQL `IntegrationStateStore` for persistent shared
budgets and audit logs; the example reuses PostgreSQL when selected in `dev.Open`.

An optional loopback remote-MCP demonstration can be enabled with
`HEX_MCP_RESOURCE_URL=http://localhost:8080/mcp` in the dev configuration. It uses
the development static identity and accepts a nonempty demonstration bearer;
it is not production authentication. See [integrations](../../docs/integrations.md)
for real gateway audience/scope validation, OAuth resource discovery and rollout.

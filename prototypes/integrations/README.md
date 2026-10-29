# Integrations catalog prototype

Design review only. The production portal is unchanged. This is a standalone,
interactive catalog using the **unchanged production `portal.css`**, plus a small
layout stylesheet. All integrations, grants, connections and endpoint contracts
are fictional examples, not live platform discovery. No external requests,
credentials, SDK or runtime dependencies are used.

## Private preview

[Open the integrations prototype](https://hxpg8olk8n.thehivemind.seidir.app/)
(visible only to the publishing user).

To update it after building, publish the output folder with
`hex publish /tmp/opencode/hex-integrations-prototype --update hxpg8olk8n`.

## Proposed interaction

- A grid (or compact desktop list) replaces the expanded integration inventory
  and site-lookup form. Every card exposes site restrictions, automation and agent
  support, personal endpoint access, and linked-login state.
- Clicking a card opens a native modal, retaining your place in the catalog.
  Escape, Close and clicking the backdrop dismiss it; keyboard focus is trapped
  and restored by the browser. Integration hashes support direct detail links.
- Feature disclosures show read/write effects, personal permission status,
  endpoint identifiers and sample input/output schemas.
- Site approval is explicitly separate from personal endpoint grants. The catalog
  does not pretend to know whether a particular site permits a call. Site-specific
  approvals remain in existing per-site management, outside this prototype.
- Connected-account integrations distinguish "granted" from "login needed".
  **Preview linking login** updates in-memory sample state only, never real OAuth.
- Agents use the caller's access, including a linked account where needed.
  Unattended automations cannot use connected-account integrations; supported
  automations still need site-principal permissions and approval where required.
- Catalog filters, grid/list, loading/empty/error states, mobile and dark mode
  are interactive. Header navigation only explains the unchanged destinations.

## Build and review

From the repository root:

```sh
npm ci --ignore-scripts
node prototypes/integrations/build.mjs
node prototypes/integrations/test.mjs
```

Build output: `/tmp/opencode/hex-integrations-prototype` (four static files).
Screenshots: `/tmp/opencode/hex-integrations-review`.
Serve the output folder with a static server or publish it as a **private Hex
artifact**, not a discoverable app. Do not publish this source folder, which also
contains documentation and build/test tooling.

No backend support or production API changes are included. Production work would
need to load connection state for catalog viewers, not just selected sites, and
derive permission summaries from authoritative endpoint grants.

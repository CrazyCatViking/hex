# Portal interaction prototypes

Design review only. Production portal code is unchanged. All records, identities,
traffic and history are fictional. The prototypes make no API calls and carry no
SDK, credentials, external images, fonts or runtime dependencies. They are not
evidence that the proposed backend is fast: filtering is deliberately local to
make the interaction review self-contained.

## Private Hex artifacts

Published to the configured `thehivemind.seidir.app` workspace, visible only to the
publishing user:

1. [Personal launchpad](https://w87rqyr6ar.thehivemind.seidir.app/)
2. [Unified app library](https://p6wgan1jy8.thehivemind.seidir.app/)
3. [Your sites](https://qcvbuqvk3y.thehivemind.seidir.app/)

## Revision 2 — familiar design, tighter scope

The prototypes now use **the unchanged production `portal.css`** for the actual
Hex header, logo, page width, theme, typography, cards, fields, rows, badges and
buttons. The small supplemental stylesheet adds only review chrome and proposed
discovery layouts. There is no new sidebar or replacement management table.

Pinning is retained as a core home interaction: four shortcuts on Home, a full
Pinned scope in the library, and pin/unpin controls on cards and rows. Pins are
personal and do not affect access. They persist only in this browser for review;
production should store user preferences per authenticated user/workspace, filter
them through current access checks, and never let a pin preserve revoked access.

**The current per-app management page is explicitly preserved.** Site titles,
Share and Analytics would still open `/manage/<site>` and its existing tabs. The
old tabbed modal is removed; a review-only note explains the existing destination
when clicking a fictional site. No replacement per-app layout is proposed.

Platform-admin design and role changes are deferred. The current Admin header
link is shown for continuity only; the proposed admin inventory/scope is removed.

Every artifact includes a direction selector and **Review notes** explaining
the tradeoffs. Home and the library now form one discovery flow; Your sites
retains the current management entry point. Search replaces the home sections with one result set,
including shared and owned records. Scope, sorting, pagination, grid/list and
back/forward navigation work. Pinning uses namespaced browser storage, falling
back to session state when unavailable. Recent items are fixed demo records.

Try `release maya`, `budget`, an unmatched query, **Shared with you**, the next
page, the list toggle, pin/unpin, and the **Preview state** control in the footer.
Select a site title, Share or Analytics to review the unchanged-page handoff.
App-opening and mutation flows explain
their intended behavior rather than opening real apps or changing permissions.

## Build and verify

From the repository root:

```sh
npm ci --ignore-scripts
node prototypes/portal/build.mjs
node prototypes/portal/test.mjs
```

Builds produce three independent, five-file folders under
`/tmp/opencode/hex-portal-prototypes`. No `hex.json` is used: these are artifact
publications, not discoverable apps. Tests start a loopback HTTP server and check
search across pages, shared/owned scopes, pin persistence, empty and error recovery,
preservation of per-app destinations, history, mobile overflow, and JavaScript
errors. Tests also verify exact reuse of production CSS and its light/dark tokens.
They capture desktop,
mobile and dark-mode screenshots under `/tmp/opencode/hex-portal-review`.

To update the existing artifacts after review, publish the respective built
folder with `--update` and its existing site name. Do not publish the source
folder, which contains review documentation and test tooling.

See [the performance review](../../docs/portal-performance-review.md) for the
backend findings and staged implementation proposal. No production redesign or
performance fix has been implemented yet.

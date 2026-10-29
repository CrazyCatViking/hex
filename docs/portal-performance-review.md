# Portal performance and experience review

## Implementation status — approved design

The approved direction is now implemented in this checkout (not deployed):
bounded pin-first Home, `/library` with unified search/scopes/grid/list/paging,
and paginated **Your sites** using the existing components. Manage, Analytics
and Copy link remain on manageable cards independently of traffic availability.
The current per-app management page and platform-admin roles remain intact.

Underlying changes include a five-second descriptive inventory cache with
coalesced, eight-worker refreshes; fresh batched policies on PostgreSQL/memory;
no analytics on ordinary browsing; bounded optional activity queries; no cache
mutex held during PostgreSQL aggregates; selected-tab-only management data;
cached icon file bytes with fresh policy checks; and batched admin owner labels.
Pins and recent launches use guarded, per-user database preferences rather than
prototype localStorage. See [portal behavior and operational limits](portal.md).

### Post-change measurements

Same synthetic all-visible/all-owned fixture as the historical baseline below,
but now explicitly warming the descriptive cache before timing (20 iterations).
As in the baseline, analytics, icon file serving and personal preference data
are excluded from this narrow handler benchmark:

| Request            | Apps | Inventory calls | Metadata reads | Single-policy reads | Fresh policy batches | HTML bytes | Approximate local handler time |
| ------------------ | ---: | --------------: | -------------: | ------------------: | -------------------: | ---------: | -----------------------------: |
| Home               |  100 |               0 |              0 |                   0 |                    1 |     11,883 |                         1.8 ms |
| Home               |  500 |               0 |              0 |                   0 |                    1 |     11,883 |                         8.0 ms |
| Search, one match  |  100 |               0 |              0 |                   0 |                    1 |      1,375 |                         1.6 ms |
| Search, no matches |  100 |               0 |              0 |                   0 |                    1 |        617 |                         1.7 ms |
| Your sites         |  100 |               0 |              0 |                   0 |                    1 |     21,643 |                         2.1 ms |
| Your sites         |  500 |               0 |              0 |                   0 |                    1 |     21,644 |                         8.3 ms |

Cold home requires **one** inventory pass, one metadata read per site in groups
of at most eight, and one fresh policy batch. A separate cold benchmark injecting
5 ms per metadata read measured about 70 ms at 100 sites and 338 ms at 500 sites
(three iterations, synthetic storage latency only). It does not simulate real
inventory or PostgreSQL latency. Cache reuse removes those metadata reads from
subsequent searches; policy projection still scales linearly and is never
replica-locally cached. These are not deployment latency/SLA claims.

Verified with the complete Go suite, server/provider race tests, live temporary
PostgreSQL tests (including cross-pool pin persistence and immediate revocation),
and Playwright against real embedded portal handlers. Concurrent refresh tests
verify one scan, at most eight readers, cancellation isolation, expiry and local
publication invalidation. Browser tests cover 156 sites, shortcuts, pins,
search across pages, paging, history, no-JS, expired-login HTML rejection, and
mobile/dark layouts. Existing NGINX/CLI/browser end-to-end tests and the external
consumer's analytics browser tests also pass with the approved library flow.
Main-content swaps use a zero settle delay so new form controls are bound before
another quick input/change event; those existing suites caught and verified this
timing regression. The global format check still reports the pre-existing,
unchanged `server/portal/start.html`; changed assets pass targeted formatting.

**Remaining boundaries:** cold descriptive discovery still enumerates storage;
warm filtering/policy projection is O(number of published sites). Legacy CLI
list APIs retain their current complete-array contracts. Query-shaped persistent
directory indexes, fully query-shaped admin/AI reports, and secondary data/file
inventories remain follow-up work rather than being claimed as solved here.
No production deployment or change to admin role semantics was performed.

## Initial conclusion

The portal treats app discovery as a fresh storage inventory, rather than as a
queryable directory. Listings repeatedly enumerate every published site, fetch
policies one at a time, and read metadata one site at a time. Search repeats that
work before it can decide that there are no matches. The home page does it twice.
Several other screens have similar request-time fan-out.

This is primarily a data-access architecture problem, amplified by an unbounded
interface. A new layout alone, client-side filtering, or pagination applied after
loading all records will not fix it. A SPA rewrite or external search service is
not needed to address the identified problems.

**Scope:** source review of this checkout and local synthetic benchmarks/browser
tests. No access to the reported company deployment, production traces, SQL
execution plans or mounted-storage timings was assumed. Findings below are code
facts; attributing a precise percentage of production latency to each one still
requires measurement on that deployment.

## Reviewed design artifacts

**Review revision 2:** keep the current Hex visual design and per-app management
page. Pinning is an endorsed direction for the home experience. Platform-admin
role/design changes are deferred to a separate discussion. The artifacts now
reuse the exact production stylesheet, remove the new sidebar/admin inventory,
and retain the current Your sites row layout. The original per-app tabbed modal
is removed; a note identifies the unchanged full-page management destination.

Published privately to the configured Hex workspace:

| Prototype                                                           | Purpose                                                                                                            |
| ------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------ |
| [01 Personal launchpad](https://w87rqyr6ar.thehivemind.seidir.app/) | Bounded pins, recent tools, shared and owned previews; a single search replaces all sections with unified results. |
| [02 Unified library](https://p6wgan1jy8.thehivemind.seidir.app/)    | Home’s full browsing destination, with pinned/shared/owned/company scopes, finite pages and URL state.             |
| [03 Your sites](https://qcvbuqvk3y.thehivemind.seidir.app/)         | Existing ownership list styling with complete search and pagination; existing per-app management preserved.        |

All 156 example apps/files are fictional. These are interaction prototypes, not
live backend implementations or performance demonstrations. **Review notes**
explains the choices; the footer previews loading, empty and failure states.
Source, rebuild instructions and browser tests are in `prototypes/portal/`.

Current direction: combine a bounded, pin-first home with a linked library and
the familiar Your sites list. Pins are a personal convenience, not a new access
model. Production persistence must be per authenticated user/workspace and reads
must respect current permissions; prototype persistence is browser-local only.
Recent history still requires a storage/privacy decision. Neither feature is a
prerequisite for fixing the backend listing costs.

## Measured baseline

`server/portal_performance_test.go` adds a repeatable benchmark with current v2
policies and an in-memory directory/access store. Every test app is visible and
owned by the viewer; analytics and favicon file serving are excluded. Numbers
represent handler HTML only, before compression; the benchmarks do not run a
browser or simulate network/database latency.

```sh
go test ./server -run '^$' -bench BenchmarkPortalInventoryAudit -benchtime=3x -count=1 -benchmem
```

| Request            | Apps | Inventory calls | Policy reads | Metadata reads | HTML bytes | Local handler time, approximate |
| ------------------ | ---: | --------------: | -----------: | -------------: | ---------: | ------------------------------: |
| Home               |   10 |               2 |           20 |             20 |     17,331 |                         0.65 ms |
| Home               |  100 |               2 |          200 |            200 |     69,174 |                          4.6 ms |
| Home               |  500 |               2 |        1,000 |          1,000 |    299,575 |                           23 ms |
| Search, one match  |  100 |               1 |          100 |            100 |      1,091 |                          1.5 ms |
| Search, no matches |  100 |               1 |          100 |            100 |        748 |                          1.7 ms |
| Management list    |  100 |               1 |          100 |            100 |    134,439 |                          4.4 ms |
| Management list    |  500 |               1 |          500 |            500 |    653,639 |                           21 ms |

These are short diagnostic runs, not statistically stable speed comparisons.
The operation counts are the useful result: cheap in-memory access conceals the
latency multiplier. At a hypothetical 5 ms per policy/metadata read, the
100-app home fixture spends about **2 seconds** on those 400 sequential reads
alone. That is an illustration, not a measured production latency. Inventory
scanning, storage file operations, traffic aggregation and icon requests add
work beyond this fixture. With fewer owned/shared apps, home metadata reads are
lower; both full policy passes remain when management is enabled.

## Initial findings (before the implementation above)

### 1. Home and search do repeated sequential I/O — critical

Sources: `server/portal.go:188–210`, `:246–284`, `:311–359`;
`server/sites.go:82–127`; `server/providers/postgres/access.go:14–32`.

- Home builds the discoverable catalogue, then separately builds personal sites.
  Each lists all names and reads policies for all valid sites. The personal
  preview limit of six is applied **after** all work and sorting.
- `discoverSites` authorizes every site, then reads metadata for each admitted
  site. `personalSites` independently reads policies and eligible metadata.
- PostgreSQL `GetSiteAccess` issues a separate `SELECT` for each site. Validation,
  normalization and sometimes legacy migration also happen on the read path.
- Each catalogue search/sort repeats discovery. A selective or empty search is
  not a selective storage query; it filters only after inventory and metadata I/O.
- A catalogue metadata failure currently fails the entire request. A malformed
  publication should not make all healthy apps impossible to discover.

### 2. Site enumeration is not cheap on remote storage — critical

Sources: `server/providers/local/sites.go:16–98`;
`server/providers/s3/sites.go:101–136`.

- Local storage reads the full directory and `Lstat`s every `index.html`.
  Metadata requires an additional `Lstat`, open and read. A network-mounted
  filesystem makes these remote operations even though Go calls them “local”.
- S3 enumeration lists **all objects** under `public/sites/`, then finds root
  `index.html` keys. Cost therefore grows with app assets, not just app count.
  Metadata then adds one remote object read per selected site.
- In the adjacent Magi checkout, `internal/storagebroker/client.go:181–211` uses
  the same all-objects listing and individual metadata-read pattern. This is a
  separate deployment path to include in testing, not an assumption about which
  provider the reported company deployment uses.

### 3. Search has inconsistent scope and weak relevance — high

Sources: `server/portal/home.html:22–36`, `:103–145`;
`server/portal.go:267–284`; `server/portal/manage.js:69–87`.

- The hero search submits the whole home page; it only filters `Catalog`.
  Shared and owned sections remain unchanged above the results.
- A second search controls only the catalogue fragment. Two search fields have
  similar promises but different behavior. The fragment form debounces 250 ms
  and replaces in-flight requests, which is good, but does not remove server I/O
  already performed. Local file calls are not always cancellable mid-operation.
- Search is a single case-insensitive substring over concatenated fields, with
  no token-based matching, field weighting or relevance ranking. An ordinary
  multiword query can fail despite both words being present in different fields.
- “Apps for everyone” is inaccurate: discovery includes visible restricted apps
  too. Some appear both there and in a personal section. Artifacts are excluded
  from catalogue discovery, so the apparent global search cannot find those
  owned/shared items either.
- `/manage` additionally filters already-rendered DOM rows locally. Once a GET
  request has narrowed that list, clearing the local search cannot restore rows
  not present in the document. It has no complete-result contract or pagination.
- “And N more” in Shared with you provides no destination to see those items.

### 4. Unbounded output increases browser and secondary-request work — high

Sources: `server/portal/catalog.html`, `server/portal/manage.html:86–130`;
`server/site_icons.go:46–112`; `server/portal/layout.html:98–110`.

- Catalogue and management listings render every matching app, increasing HTML,
  DOM, layout and keyboard-navigation work. There is no meaningful finite page.
- Icons are already lazy-loaded and decoded asynchronously: do not misdiagnose
  this as eagerly downloading every icon at initial paint. However, icons near
  the viewport and those revealed while scrolling trigger separate requests.
- Every icon request reauthorizes the site, opens/parses its index and tries
  candidate files. Missing icons can try four conventional paths before reading
  fallback metadata. There is no explicit content cache/ETag or negative lookup
  cache in this handler. Browser heuristic caching is not a reliable strategy.
- This makes pages feel unfinished after HTML arrives and adds shared backend
  work. Retain lazy loading, bound results and avoid repeated discovery of an
  unchanged icon. Preserve per-path authorization when serving cached bytes.

### 5. Analytics adds a synchronous dependency to basic browsing — high

Sources: `server/card_traffic.go:17–47`;
`server/providers/postgres/analytics.go:281–348`.

- Traffic is already batched per listing, not one traffic query per card.
  PostgreSQL already caches traffic totals for one minute. Preserve those wins.
- Nevertheless, catalogue and personal sites both request totals, and personal
  sites do so before truncating home previews. Browsing waits for completion.
- A cold cache aggregates all historical traffic for the missing sites, including
  exact distinct-user counts. Cost grows with traffic history as well as apps.
- A global traffic-cache mutex is held during the database query and iteration.
  One slow miss therefore queues unrelated cache hits and misses in that process.
  Cache entries cap at 1,024; oversized listings stop benefiting from retention.
- Failures are logged and browsing continues, but there is no dedicated short
  analytics deadline here. A slow response can still hold up an otherwise usable
  page. Traffic-unavailable and zero-traffic must remain distinguishable.

### 6. Admin pagination bounds HTML, not upstream work — high

Sources: `server/analytics_portal.go:206–283`, `:306–392`;
`server/providers/postgres/analytics.go:190–243`.

- Every admin overview, sites/people list and site drill-down rebuilds the whole
  inventory, with per-site metadata, policy and owner-name lookup. Repeated
  owners are looked up repeatedly across sites.
- On first inventory access per process, `analyticsBootstrap` locks and imports
  publication metadata/history and writes analytics events for every site.
  This happens inside the request; concurrent requests wait. Failed imports
  leave the flag unset and can cause subsequent full import attempts.
- `QueryAnalytics` builds a complete report including grouped site/user rows
  and, for global views, all remembered analytics people. The UI slices to 25
  rows only after inventory/report construction and in-memory filtering/sorting.
- The previous-period summary is another query. A single-site admin drill-down
  should not need a full current platform inventory.

### 7. Per-site pages load unrelated sections — medium/high

Source: `server/manage.go:210–277`; `server/permission_portal.go:105–146`.

Every site tab loads publication history, sharing view/principal labels and
action summaries, even if the selected tab does not use them. Sharing itself
does two principal-description passes. Analytics and integration tabs may then
add their own requests. Switching tabs reloads the full page and repeats the
common work. Load metadata/header plus only what the active tab needs. Keep the
existing policy editor and permission semantics rather than rewriting them for
visual consistency.

### 8. Other inventories need bounded query contracts — medium

- `server/manage_ai.go:531–628`: admin AI first gets grouped site usage, then
  queries `AIBudgetSpend` separately for each site with usage, an override or an
  outstanding reservation. Person overrides also resolve names individually.
  Batch settled/reserved totals without changing enforcement/accounting logic.
- `server/manage.go:573–600`: uploaded-file browsing lists, sorts and renders all
  files. Document browsing already uses bounded pages; retain that behavior.
- `server/portal_connections.go:108–135`: admin connections list all credentials;
  names are already batched, but the output is not paged.
- `server/portal_integrations.go:130–185`, `:209–237`: per-site integration cards
  read approval/connection state sequentially; admin approval review loads the
  full list. These scale with integrations/approvals rather than every published
  app directly, so they are secondary priorities, not the main home-page cause.
- The site visitor analytics path already supports summary and paged visitor
  provider interfaces (`server/site_analytics.go:49–88`); use that as a pattern.

## Proposed technical direction

### First: measure and remove duplicate work without weakening security

1. Add request-phase timings for inventory, policy load, metadata, traffic,
   principal labels and rendering; capture rows scanned/returned and SQL counts.
   Compare cold/warm p50/p95/p99, first useful paint and search-to-result latency.
2. Build one request-scoped inventory for home, reuse loaded policies/metadata,
   then select bounded previews. Use an optional bulk-policy provider interface
   backed by one current primary-database read and the existing policy evaluator.
   Keep safe fallbacks for third-party providers.
3. Make active-tab loading explicit. Batch principal resolution. Move analytics
   history recovery out of first-page latency into a resumable bounded worker,
   preserving idempotency, progress and failure reporting.
4. Remove optional traffic from the critical discovery path. Use bounded
   enrichment for visible results unless traffic affects ordering/filtering;
   those cases require a queryable aggregate before paging, not page-local sort.
   Release cache locks during I/O and coalesce same-key work only.

### Then: make discovery a query, not a filesystem scan

Introduce an optional catalogue repository with name, title, description,
verified attribution, type, discoverability, publication status/version and
timestamps. PostgreSQL is sufficient where already deployed. Non-Postgres/local
providers can use a bounded metadata snapshot and the same query contract.
Avoid introducing another external service before measurements justify it.

- Update catalogue records from successful publication/unpublication paths.
  Preserve the existing non-transactional upload semantics; expose only completed
  publications, retry reconciliation and detect out-of-band file changes through
  a periodic inventory. Backfill existing apps, including missing metadata.
- Store publication metadata for querying, not a stale per-replica authorization
  decision. `server/authorization.go:287–338` explicitly forbids replica-local
  policy caches because another replica may revoke access immediately before
  private files are uploaded. Retain that guarantee.
- Query **current policies** consistently with catalogue candidates. Apply access
  filtering before counts, ranking and paging. An unauthorized app must not leak
  through titles, excerpts, authors, suggested queries, totals or empty states.
  Never page raw candidates then filter 12 rows and declare the page complete.
- Provide one query contract: text, scope, kind, sort, bounded page/cursor and
  visible-result count. Share it across HTML fragments/full-page GET requests;
  preserve existing CLI array APIs unless a separately versioned change is made.
- Search normalized tokens across title/name/description/verified author; boost
  exact titles and prefixes over incidental description matches. Retain a stable
  name/ID tie-breaker, reset page on filter changes, and rank before paging.
  Start with deterministic token search; add indexed full text/trigram typo
  tolerance only with language/quality tests and query-plan evidence.
- Scope definitions must be explicit: accessible discoverable company apps,
  directly/group-shared items and owned items are not interchangeable. Hidden
  apps may remain available in their owner's management view and explicitly
  shared private files must not become company catalogue entries. Preserve
  ownership/admission/grant independence and anonymous/local behavior.
- Cache versioned descriptive metadata and parsed icon candidates, including
  missing-icon results. Reauthorize each response using the current policy;
  revocation, changed protected paths and republishing must invalidate unsafe
  previews. Do not add a long-lived private-image browser cache casually.
- For popularity ordering at scale, maintain bounded aggregate summaries;
  exact distinct visitors require a maintained site/user presence relation or
  equivalent correct aggregate, not summing daily distinct-user counts.

### Experience contract

- One visible search; on home a query replaces all previews with a complete,
  finite result set. No unfiltered panels above results, duplicate searches or
  misleading catalogue labels. Make Shared with you a linked destination.
- Default home is small; browsing the whole library is explicit. Preserve
  filters/query/page in URLs, browser history and no-JavaScript GET navigation.
- Lists support a finite result total, pagination, clear-all, stable ordering,
  keyboard focus and result announcements. Do not render every app and hide it.
- Keep previous results usable while refreshing, show pending/error state, cancel
  superseded requests and ignore late responses. A gateway login page must not
  be mistaken for successful HTML. Failed traffic is not “0 visitors”.
- Separate opening an app from managing it. Keep familiar shared tokens,
  light/dark modes, small-screen behavior, existing permissions and installers.
  Retain the existing top navigation and deep-linkable per-app management pages;
  no replacement modal or platform-admin redesign is proposed. Active-tab loading
  optimizations can happen behind the unchanged per-app interface.

## Delivery order and acceptance gates

| Order | Deliverable                                                                            | Gate                                                                                                       |
| ----- | -------------------------------------------------------------------------------------- | ---------------------------------------------------------------------------------------------------------- |
| 1     | Instrumentation, operation-count/latency fixtures, active-tab and duplicate-read fixes | No policy or API regressions; one inventory snapshot per home request.                                     |
| 2     | Catalogue backfill/reconciliation, fresh batched policy access and query paging        | Search/first page does not rescan publication files; correct authorization before counts/paging.           |
| 3     | Approved home/library/management design                                                | Finite output, unified search and working shared destination; mobile/keyboard/no-JS/browser-history tests. |
| 4     | Aggregate/query-shaped admin and AI reports, icon caching, secondary inventories       | No full inventory for a single-site drill-down; optional analytics cannot block basic browsing.            |

Admin/AI findings above remain diagnostic evidence, not authorization to change
platform-admin roles or interfaces. Defer that part of delivery until the separate
admin discussion; icon/traffic performance work does not require a role redesign.

Initial performance budgets to validate, **not achieved results**: at 100, 500
and 1,000 apps, warm core listing/search p95 below 300 ms server-side, ordinary
search interaction-to-result below 500 ms on a representative company connection,
and no more than 12–24 rendered results per page. Record separate cold-start,
slow-storage and analytics-outage budgets; do not hide them in warm averages.

Test mixed public/private/hidden apps and files, group ownership, revocation
across two replicas, duplicate names, legacy policy migration, malformed metadata,
publish/unpublish during paging, deep links, sort/filter changes and search with
zero matches. Add concurrent-user tests, realistic asset counts, simulated
5–20 ms provider latency, large traffic history and actual PostgreSQL EXPLAIN
ANALYZE before claiming the company deployment is fixed.

## Verification and remaining limits

- Prototype browser tests passed for all three directions, including 390px
  mobile layouts, dark mode, search across pages, scopes, sorting, pagination,
  history and error recovery. Screenshots were visually reviewed.
- `go test ./server ./server/providers/local ./server/providers/postgres` passed.
  Database integration tests requiring external configuration may skip; this is
  not a substitute for deployment SQL/load testing.
- Existing `npm ci` reports a high-severity advisory on the pinned development
  dependency `undici@7.29.1`. It is not shipped by these standalone prototypes.
  Dependency remediation is separate from the portal performance change; no
  automatic broad dependency update was made.
- No production portal source or access policy was changed. Only review assets,
  documentation and a diagnostic benchmark were added. Proceed with a production
  redesign only after prototype review.

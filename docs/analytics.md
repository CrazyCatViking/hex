# Platform analytics

Hex includes an admin dashboard at `/admin`, site and people inventories at
`/admin/sites` and `/admin/users`, and an **Analytics** tab on each site's
management page. Admin pages require the configured platform-admin identity on
the platform host. Site owners/publishers can see their site's traffic and named
page visitors. Platform admins can view cross-site and per-user reports.

## Pages

The pages are written for everyone who looks after a site, not only developers:

- Quick periods (7 days, 30 days, 90 days, 12 months) with custom dates on
  demand. Headline numbers compare with the previous period of the same length.
- A daily chart of page views (bars) and people (line), with weekends shaded.
  Pointing at, or arrowing through, a day shows its numbers; "Show daily
  numbers" lists them for screen readers and copying.
- The admin overview adds adoption today (active people, sites, creators), the
  most viewed sites and most active people, and a publishing activity feed.
- A site's Analytics tab adds who visited, searchable as you type, with each
  person's page views, visits and last visit. Requests, errors, data sent and
  response time are under "Technical details", with a plain explanation of how
  each number is counted.

## What is measured

- Current inventory: all published apps and files/folders, including hidden
  publications, and distinct verified creators. Names supplied in `author` are
  not counted as verified users. Sites with missing creator records are reported
  separately. Inventory remains derived from actual site directories.
- People: first and last observed activity, known users, active users in the last
  7/30 days, and direct user ownership versus creation of current sites. Groups
  are not expanded into their members. User activity is refreshed at most hourly
  and is platform activity, not an identity-provider sign-in audit.
- Publishing: creation, successful publication and unpublishing events, with
  actor and timestamp. These records survive taking down the site. When an admin
  first opens the dashboard, existing metadata and available publication history
  are imported idempotently. Previously deleted sites/history cannot be recovered.
- Traffic: requests, page views, unique authenticated visitors, visits, HTTP
  errors, bytes transferred, mean request duration and last visited. Date filters
  use inclusive UTC calendar days, for up to 366 days at a time. Daily trends,
  site/user drill-downs, search, sorting and paginated inventories are included.

The JSON endpoint `/api/hex/admin/analytics` accepts `from`, `until`, `site` and
`user`. It requires platform-admin access and is unavailable from app origins.
`/api/hex/admin/analytics.csv` exports the same filtered daily statistics as CSV.
`/api/hex/manage/sites/<site>/analytics` renders an owner-authorized HTML fragment
with traffic, daily trends and a site-scoped visitor breakdown.

## Browsing and publisher reports

Browsing cards and **Your sites** show all recorded page views, including a zero
count for sites with no page loads yet. Catalogue and management lists can sort
by **Most popular (page views)**, **Most visitors**, **Recently visited**, latest
publication or name, and filter by **Has page views** / **No page views yet**.
Counts and popularity use traffic since collection began, rather than an implicit
rolling window. Only aggregates for sites the viewer can already browse are
loaded; visitor identities are not exposed on public cards.

The site's **Analytics** tab has an all-time summary and a date-filtered **Who
visited** table. Owners (the people allowed to publish the site) and platform
admins can see names/emails, page views, visits and the last page visit, search
visitors, change ordering and page through results. The table lists only users
with actual page views on that site in the selected period, excluding API/asset-only
activity. Names come from remembered authenticated identities; unknown names fall
back to the verified identity ID. Anonymous views are counted separately without
inventing visitor identities. General viewers/editors cannot access visitor reports.

Both management pages and their HTMX fragments enforce the same owner/admin
permission checks, and ignore query parameters attempting to select another site
or user. Publisher reports contain no cross-site totals or platform sign-in history.

Built-in stores implement the optional `SiteTrafficReader` for efficient bulk
all-time totals. Existing custom `AnalyticsStore` implementations continue to
work; implement this interface to enable card counts and popularity controls.
An unavailable aggregate query omits the optional counters while leaving the
directory usable.

## Storage and configuration

Analytics uses `Config.Analytics`, an optional `AnalyticsStore`, independently of
the app document database. The PostgreSQL implementation uses platform-owned
`hex_analytics_*` tables; apps cannot read or edit them through the document API.
The in-memory implementation is intended for local development and resets on
restart. No custom-provider metrics are currently implemented; the separate
analytics interface leaves room for additional sources later.

Reference executable settings:

| Variable | Purpose |
| --- | --- |
| `HEX_ANALYTICS_PROVIDER` | `none`, `memory`, or `postgres`. Defaults to the app database selection (`memory`, `postgres`, or `none`). |
| `HEX_ANALYTICS_DATABASE_URL` | Optional separate PostgreSQL connection. Otherwise analytics uses `DATABASE_URL`, sharing its pool when the app database uses the same connection. |
| `HEX_ANALYTICS_ADDR` | Loopback-only UDP syslog listener, for example `127.0.0.1:8082`. Omitted disables the local traffic collector, while user/publishing analytics remain enabled. |
| `HEX_ADMIN_GROUPS` | Existing platform-admin principals, required to access global reports. |

For example, durable analytics with app documents disabled:

```text
HEX_DATABASE_PROVIDER=none
HEX_ANALYTICS_PROVIDER=postgres
HEX_ANALYTICS_DATABASE_URL=postgres://.../hex?sslmode=verify-full
HEX_ANALYTICS_ADDR=127.0.0.1:8082
HEX_IDENTITY_PROVIDER=easyauth
HEX_ADMIN_GROUPS=<admin-group-id>
```

With PostgreSQL configured for either documents or analytics, the reference
executable can also persist access policies and the people picker. Existing
people are imported with an unknown first-seen date, rather than pretending their
last-seen timestamp was their first use.
When a document database already holds access policies, choosing a separate
analytics connection preserves that policy/directory database.

The Azure example enables durable analytics and traffic when a managed/external
document database is selected. Its site-only profile has analytics disabled until
storage is configured. A consuming deployment can select a separate analytics
connection using the hosting module's environment/secrets bindings. Deploy both
the updated server and nginx images. The hosting module configures the collector
and nginx logging together when `HEX_ANALYTICS_PROVIDER` is enabled.

`server/dev.Open` defaults to in-memory analytics (PostgreSQL when the local
document database is PostgreSQL). `hex dev` starts the collector automatically on
the gateway port using **UDP**; nginx serves HTTP on the same port using **TCP**.
Set `HEX_ANALYTICS_PROVIDER=none` in `hex.dev.json`/the env file to disable both.
Direct runs of `dev.Open` enable traffic collection only with `HEX_ANALYTICS_ADDR`.

Embedders retain lifecycle control:

```go
collector, err := hex.StartTrafficCollector(ctx, "127.0.0.1:8082", analytics)
if err != nil {
    return err
}
defer collector.Close()
config.Analytics = analytics
config.TrafficCollector = collector
handler := hex.New(config)
```

## nginx collection

The shipped template emits versioned JSON through loopback syslog. Set
`HEX_ANALYTICS_ENABLED=1` on the nginx container and enable the corresponding Go
collector. The container template defaults logging off; its default destination
is `127.0.0.1:8082`. Adapt that destination when using a different local listener.

nginx still serves files directly. The existing authorization subrequest returns
an encoded, server-resolved user identifier. `auth_request_set` captures it for
static requests; proxied app APIs supply the same internal response header, which
nginx hides from clients. The collector never uses a browser-supplied user ID.
Only completed main requests for site hostnames are collected; platform pages,
health checks and authorization subrequests are excluded.

Page views are successful GET document loads (2xx or identifiable 304), using
Fetch Metadata and HTML response types. Redirects, errors, assets, HEAD and API
requests do not become page views. Unique visitors are deduplicated across the
whole query range, not summed from daily counts. Visits use authenticated user +
site and a 30-minute inactivity timeout; session state persists across server
restarts and midnight. Late out-of-order records do not move session time backward.
Anonymous page loads count toward page views, but not identified visitors/visits.

Browser-cached loads and SPA/hash-route transitions that do not reach nginx are
not observed. A later optional browser event API can supplement these statistics.
Bytes and request counts include asset and app API traffic reaching nginx; HTTP
errors mean 4xx/5xx. A WebSocket request is logged when it finishes.

The collector batches database writes in the background and retries failed
batches. Request IDs deduplicate retries for seven days. Daily per-site/per-user
buckets and lifecycle events are retained; raw paths, IPs, query strings, cookies,
tokens and request/response bodies are not stored. The seven-day receipt window
is pruned during ingestion. Older traffic messages are rejected.

Loopback UDP is **best effort**: traffic is not a billing-grade audit log. The
bounded collector queue can drop requests during prolonged storage failures, and
messages can be lost before receipt during restarts or socket pressure. The admin
overview ends with a collection status line (collecting, waiting for the first
visit, quiet for over two days, or stopped); opening it shows received/recorded/
rejected counts, queue/shutdown drops, write failures and the latest collector
error.
Process counters reset on restart; persisted totals do not. User/publishing write
failures are logged and counted without failing an already-completed publication.

## Verification

`go test -race ./...` exercises authorization, hidden-site inventory, lifecycle
retention, unique visitors, sessions across midnight, and a real nginx collector.
Set `HEX_TEST_POSTGRES_URL` to exercise migrations, first/last-seen persistence,
idempotent concurrent ingestion and reopening the database. Local/browser
integration checks exercise the dashboard, filters and site Analytics tab.
Run `npm run test:analytics` for HTMX date/search filters, mobile layout and
no-JavaScript checks against a consuming server launched by `hex dev`.

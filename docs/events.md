# Durable events

Events are schema-validated messages delivered to deployed automations. Enable
the normal database, action and automation providers and run `RunBackground`.
PostgreSQL persists event envelopes, subscriptions, leases and delivery results.
No additional broker or credentials are needed. Memory storage is for development.

## Define, subscribe, publish

The producer owner defines an immutable versioned schema:

```ts
await hex.events.define("order-approved", {
  version: 1,
  description: "An order was approved",
  schema: {
    type: "object",
    properties: { orderId: { type: "string" } },
    required: ["orderId"],
    additionalProperties: false,
  },
});
```

Grant `event/publish` on `order-approved` to the producer users/automations that
may publish. Grant `event/subscribe` on that name to the consumer's opaque site
principal (`hex.sites.identity()`). The consumer owner deploys an automation,
then subscribes it:

```ts
await hex.events.subscribe("orders", {
  producer: "sales", event: "order-approved", version: 1,
  automation: "process-order",
});
```

Optional `where` uses the existing record condition syntax against event
`data.*`. Set `disabled: true` to stop a subscription. A topic supports 32
subscriptions. Subscription IDs are scoped to consumer and producer topic.
Updates rotate the subscription generation and pin the current automation
revision; refresh a subscription after redeploying its handler.

Publish from a browser or automation:

```js
await hex.events.publish("order-approved", { orderId: "order-123" }, {
  version: 1, idempotencyKey: "approved-order-123",
});
```

Handlers receive a frozen `hex.event` containing `id`, `site`, `name`, `version`,
`actor`, `data` and `createdAt`. They execute with their own automation's grants,
never the event publisher's authority. Use `hex.event.id` for downstream
idempotency keys and deterministic document IDs.

## Delivery guarantees

Publication atomically stores the event and its complete subscriber fan-out in
one database mutation. Reusing a publication key with the same actor/topic and
payload returns the original event; different payloads return 409. Payloads are
limited to 64 KiB. An event only includes subscriptions active at publication.

Delivery is **at least once**, without ordering guarantees. Workers claim leases
atomically across replicas. Expired leases can be recovered after a restart;
completion is fenced by the claim token. Execution shares automation concurrency
and runtime limits. Failed handlers retry with exponential backoff, up to eight
attempts. Revoked access, removed/disabled subscriptions and changed automation
revisions stop delivery. Current grants are checked again at execution.

Publication is not atomic with an independent `db.save` or action call. Prefer an
event as the durable command/source of truth and perform state changes in its
idempotent consumer; do not use save-then-publish as a transactional outbox.

## Inspect and replay

Producer owners can inspect all deliveries through `hex.events.inspect(id)`;
`hex.events.list(after)` lists their published events, while `hex.events.types(after)`
lists contracts and consuming subscriptions. These owner-only lists return
`{items,after}` pages of 25; continue until empty.
consumer owners see only their deliveries, subject to current subscription
permission. Each delivery includes status, attempts, next attempt, run ID and
last error. Automation run history retains the event context and operation trace.

After correcting the problem, the consumer owner calls
`hex.events.replay(eventId, "orders")`. Only dead deliveries can be replayed.
Replay selects the current subscription generation/revision and rechecks grants;
it preserves the event ID, so downstream deduplication still works.

HTTP equivalents live beneath `/api/sites/<site>/events`:
`PUT types/<name>`, `GET types`, `PUT subscriptions/<id>`, `GET subscriptions`,
`POST publish/<name>`, `GET records`, `GET records/<id>`,
`POST records/<id>/replay/<subscription>`.

`GET subscriptions` lists the consumer's own subscriptions across producers,
each with `allowed` (the producer still grants `event/subscribe`) and `stale`
(the handler automation was redeployed since subscribing, so deliveries fail
until the subscription is refreshed).

The CLI wraps these routes; `--site` defaults to the project:

```sh
hex events define order-approved --input @event.json
hex events types
hex events subscribe orders --producer sales --event order-approved --version 1 --automation process-order [--where @filter.json] [--disabled]
hex events subscriptions
hex events publish order-approved --version 1 --input @data.json --idempotency-key approved-order-123
hex events list
hex events inspect <event-id>
hex events replay <event-id> <subscription-id>
```

`hex actions catalog` lists events other sites share, with every registered
version and its payload schema.
Bodies match the SDK arguments. Schemas and subscriptions are owner-managed;
ordinary publishers do not need management authority.

Events and deduplication state are retained until operator archival. The worker
uses bounded, cursor-paginated scans of pending records. Archive completed event
records only after the application's replay and publication-key retention window.
# Sharing events with the workspace

In the site's **Overview → Actions & events**, choose the event's **Details &
access** and set **Other apps → Available to** to **All workspace apps** to allow
any site in the current workspace to subscribe, or add selected apps. The setting
applies to all registered versions of that event name. It creates an `event` /
`subscribe` grant for a permission group with a workspace audience. It does not
grant event publishing or change the consumer automation's data permissions.

Subscriptions still bind to persistent site identities and a registered event
version. Delivery rechecks the producer's current policy, so restricting sharing
stops unauthorized delivery. Other explicit subscription grants remain valid.
Consumers can use event-triggered automations to compose workflows; workflow
execution and management keep their own permissions.

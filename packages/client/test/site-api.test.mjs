import assert from "node:assert/strict";
import test from "node:test";
import { createHexClient } from "../dist/index.js";

test("site APIs, events and workflows retain the source origin and request identity", async () => {
  const requests = [];
  const hex = createHexClient({
    site: "dashboard",
    fetch: async (url, options) => {
      requests.push({ url, options });
      return new Response(JSON.stringify({ id: "result" }), {
        headers: { "Content-Type": "application/json" },
      });
    },
  });
  await hex.sites
    .api("inventory", { version: 2 })
    .call("reserve", { quantity: 3 }, { idempotencyKey: "reservation" });
  assert.equal(requests[0].url, "/api/sites/dashboard/site-api/call");
  assert.deepEqual(JSON.parse(requests[0].options.body), {
    target: "inventory",
    action: "reserve",
    version: 2,
    input: { quantity: 3 },
    idempotencyKey: "reservation",
  });
  await hex.events.publish(
    "reserved",
    { quantity: 3 },
    {
      version: 1,
      idempotencyKey: "event",
    },
  );
  await hex.events.subscribe("sales", {
    producer: "inventory",
    event: "reserved",
    version: 1,
    automation: "receive",
  });
  await hex.workflows.start(
    "fulfill",
    { order: "one" },
    {
      idempotencyKey: "fulfillment",
    },
  );
  await hex.workflows.cancel("job");
  for (const { url, options } of requests) {
    assert.ok(url.startsWith("/api/sites/dashboard/"));
    assert.equal(options.credentials, "same-origin");
    assert.equal(options.redirect, "error");
    assert.equal(options.headers.get("X-Hex-Request"), "1");
  }
  assert.throws(() => hex.sites.api("../other", { version: 1 }));
  assert.throws(() => hex.events.replay("../event", "subscription"));
});

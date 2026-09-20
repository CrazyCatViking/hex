import { test } from "node:test";
import assert from "node:assert/strict";
import { createHexClient, HexError } from "../dist/index.js";

test("client encodes keys, sends the request marker and preserves binary data", async () => {
  const calls = [];
  const hex = createHexClient({
    site: "demo",
    baseURL: "https://hex.example/",
    fetch: async (url, init) => {
      calls.push({ url, init });
      return new Response(JSON.stringify({ key: "folder/a b.txt", size: 3 }), {
        headers: { "Content-Type": "application/json" },
      });
    },
  });
  const bytes = new Uint8Array([0, 128, 255]);
  await hex.files.upload("folder/a b.txt", bytes);
  assert.equal(
    calls[0].url,
    "https://hex.example/api/sites/demo/files/folder/a%20b.txt",
  );
  assert.equal(calls[0].init.headers.get("X-Hex-Request"), "1");
  assert.equal(calls[0].init.credentials, "same-origin");
  assert.equal(calls[0].init.redirect, "error");
  assert.deepEqual(calls[0].init.body, bytes);
  assert.throws(() => hex.files.url("../secret"));
});

test("database operations preserve envelopes and report structured failures", async () => {
  const calls = [];
  const hex = createHexClient({
    site: "demo",
    fetch: async (url, init) => {
      calls.push({ url, init });
      if (init.method === "DELETE") return new Response(null, { status: 204 });
      if (url.endsWith("/missing"))
        return new Response('{"error":"not found"}', { status: 404 });
      return new Response('{"id":"one","data":{"done":false}}');
    },
  });
  const tasks = hex.db.collection("tasks");
  assert.deepEqual(await tasks.create({ done: false }), {
    id: "one",
    data: { done: false },
  });
  assert.equal(calls[0].init.body, '{"done":false}');
  assert.equal(calls[0].init.method, "POST");
  await tasks.delete("one");
  await assert.rejects(
    tasks.get("missing"),
    (error) => error instanceof HexError && error.status === 404,
  );
  await tasks.list({ after: "last", limit: 20 });
  assert.equal(
    calls.at(-1).url,
    "/api/sites/demo/db/tasks?after=last&limit=20",
  );
});

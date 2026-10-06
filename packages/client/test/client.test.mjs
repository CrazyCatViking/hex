import { test } from "node:test";
import assert from "node:assert/strict";
import {
  createHexClient,
  HexConnectionRequiredError,
  HexError,
} from "../dist/index.js";

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

test("identity resolves the caller and degrades to null when unavailable", async () => {
  const responses = new Map([
    [
      200,
      new Response(
        '{"provider":"aad","id":"user-id","email":"alex@example.com","groups":["sales"]}',
        { headers: { "Content-Type": "application/json" } },
      ),
    ],
    [404, new Response('{"error":"not found"}', { status: 404 })],
    [401, new Response('{"error":"not authenticated"}', { status: 401 })],
    [500, new Response('{"error":"broken"}', { status: 500 })],
  ]);
  let status = 200;
  const hex = createHexClient({
    site: "demo",
    fetch: async (url) => {
      assert.equal(url, "/api/hex/me");
      return responses.get(status).clone();
    },
  });

  assert.deepEqual(await hex.identity(), {
    provider: "aad",
    id: "user-id",
    email: "alex@example.com",
    groups: ["sales"],
  });
  status = 404;
  assert.equal(await hex.identity(), null);
  status = 401;
  assert.equal(await hex.identity(), null);
  status = 500;
  await assert.rejects(
    hex.identity(),
    (error) => error instanceof HexError && error.status === 500,
  );
});

test("permissions describe the viewer for the configured site", async () => {
  const body = {
    role: "viewer",
    admin: false,
    publish: false,
    paths: [{ prefix: "/admin/", allowed: false }],
    collections: { "*": { read: "all", write: "none" } },
    files: { "*": { read: "all", write: "none" } },
    channels: { "*": { read: "all", write: "none" } },
  };
  const hex = createHexClient({
    site: "demo",
    fetch: async (url) => {
      assert.equal(url, "/api/hex/sites/demo/permissions");
      return new Response(JSON.stringify(body), {
        headers: { "Content-Type": "application/json" },
      });
    },
  });

  assert.deepEqual(await hex.permissions(), body);
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

function sseResponse(chunks, { signal } = {}) {
  const encoder = new TextEncoder();
  let index = 0;
  const body = new ReadableStream({
    pull(controller) {
      if (signal?.aborted) {
        controller.error(signal.reason);
        return;
      }
      if (index >= chunks.length) {
        controller.close();
        return;
      }
      controller.enqueue(encoder.encode(chunks[index++]));
    },
  });
  return new Response(body, {
    headers: { "Content-Type": "text/event-stream" },
  });
}

function sse(events) {
  return events
    .map(
      (event) =>
        `event: ${event.type}\r\ndata: ${JSON.stringify(event)}\r\n\r\n`,
    )
    .join(": keep-alive\n\n");
}

// Splits text into chunks at awkward places, including inside CRLF pairs
// and multi-byte characters.
function chop(text, size = 7) {
  const bytes = new TextEncoder().encode(text);
  const chunks = [];
  for (let start = 0; start < bytes.length; start += size) {
    chunks.push(bytes.slice(start, start + size));
  }
  return chunks;
}

function byteResponse(chunks) {
  let index = 0;
  return new Response(
    new ReadableStream({
      pull(controller) {
        if (index >= chunks.length) {
          controller.close();
          return;
        }
        controller.enqueue(chunks[index++]);
      },
    }),
  );
}

const assistant = (content) => ({ role: "assistant", content });

test("AI streams parse events split across chunks", async () => {
  const events = [
    { type: "thinking", text: "Hmm… ✓" },
    { type: "text", text: "Hei" },
    { type: "text", text: " på deg" },
    {
      type: "message",
      message: assistant([{ type: "text", text: "Hei på deg" }]),
    },
    {
      type: "done",
      stopReason: "end_turn",
      usage: { inputTokens: 3, outputTokens: 2 },
    },
  ];
  const calls = [];
  const hex = createHexClient({
    site: "demo",
    fetch: async (url, init) => {
      calls.push({ url, init });
      return byteResponse(chop(sse(events) + 'data: {"type":\n', 5).concat([]));
    },
  });

  const request = {
    model: "general",
    messages: [{ role: "user", content: [{ type: "text", text: "Hei" }] }],
    thinking: { effort: "high", show: true },
  };
  const seen = [];
  for await (const event of hex.ai.stream(request)) {
    seen.push(event);
    if (seen.length === events.length) {
      break;
    }
  }
  assert.deepEqual(seen, events);
  assert.equal(calls[0].url, "/api/sites/demo/ai/stream");
  assert.equal(calls[0].init.headers.get("Accept"), "text/event-stream");
  assert.equal(calls[0].init.headers.get("X-Hex-Request"), "1");
  assert.deepEqual(JSON.parse(calls[0].init.body), request);

  const text = await createHexClient({
    site: "demo",
    fetch: async () => byteResponse(chop(sse(events), 3)),
  })
    .ai.stream(request)
    .text();
  assert.equal(text, "Hei på deg");
});

test("multi-line data and comments follow the event stream format", async () => {
  const hex = createHexClient({
    site: "demo",
    fetch: async () =>
      sseResponse([
        ": hello\r",
        '\nevent: text\ndata: {"type":"text",\n',
        'data: "text":"a"}\n\n',
        'data: {"type":"done","stopReason":"end_turn"}\r\r',
      ]),
  });
  const seen = [];
  for await (const event of hex.ai.stream({ model: "m", messages: [] })) {
    seen.push(event);
  }
  assert.deepEqual(seen, [
    { type: "text", text: "a" },
    { type: "done", stopReason: "end_turn" },
  ]);
});

test("stream error events reach iterators and reject the helpers", async () => {
  const events = [
    { type: "text", text: "partial" },
    { type: "error", error: "the model request failed" },
  ];
  const hex = createHexClient({
    site: "demo",
    fetch: async () => sseResponse([sse(events)]),
  });
  const seen = [];
  for await (const event of hex.ai.stream({ model: "m", messages: [] })) {
    seen.push(event.type);
  }
  assert.deepEqual(seen, ["text", "error"]);
  await assert.rejects(
    hex.ai.stream({ model: "m", messages: [] }).finalMessages(),
    (error) => error instanceof HexError && error.status === 502,
  );
});

test("aborting a stream stops reading", async () => {
  const controller = new AbortController();
  const hex = createHexClient({
    site: "demo",
    fetch: async (url, init) => {
      assert.equal(init.signal, controller.signal);
      return sseResponse(
        [
          sse([{ type: "text", text: "a" }]),
          sse([{ type: "text", text: "b" }]),
        ],
        { signal: controller.signal },
      );
    },
  });
  const seen = [];
  await assert.rejects(async () => {
    for await (const event of hex.ai.stream(
      { model: "m", messages: [] },
      { signal: controller.signal },
    )) {
      seen.push(event.text);
      controller.abort(new Error("stopped"));
    }
  });
  assert.deepEqual(seen, ["a"]);
});

test("integration calls report accounts that need connecting", async () => {
  const calls = [];
  const hex = createHexClient({
    site: "demo",
    fetch: async (url, init) => {
      calls.push({ url, init });
      if (url.endsWith("/docs/me")) {
        return new Response(
          JSON.stringify({
            error: "connect your Docs account to use this",
            connect: {
              connector: "docs",
              title: "Docs",
              url: "https://hex.example/api/hex/connections/docs/start",
            },
          }),
          { status: 409 },
        );
      }
      return new Response('{"deals":[]}');
    },
  });

  assert.deepEqual(
    await hex.integrations.call("crm", "deals", { stage: "won" }),
    { deals: [] },
  );
  assert.equal(calls[0].url, "/api/sites/demo/integrations/crm/deals");
  assert.deepEqual(JSON.parse(calls[0].init.body), { stage: "won" });
  await assert.rejects(
    hex.integrations.call("CRM", "deals"),
    /Invalid integration/,
  );

  await assert.rejects(hex.integrations.call("docs", "me"), (error) => {
    assert.ok(error instanceof HexConnectionRequiredError);
    assert.ok(error instanceof HexError);
    assert.equal(error.status, 409);
    assert.equal(error.connect.connector, "docs");
    return true;
  });
});

test("connect opens a popup and resolves when it closes", async () => {
  const popup = { closed: false };
  const opened = [];
  globalThis.open = (url) => {
    opened.push(url);
    setTimeout(() => {
      popup.closed = true;
    }, 10);
    return popup;
  };
  try {
    const hex = createHexClient({
      site: "demo",
      fetch: async () => new Response("{}"),
    });
    await hex.integrations.connect({
      connector: "docs",
      title: "Docs",
      url: "https://hex.example/api/hex/connections/docs/start",
    });
    assert.deepEqual(opened, [
      "https://hex.example/api/hex/connections/docs/start",
    ]);
  } finally {
    delete globalThis.open;
  }
});

test("conversations run app tools and merge their results with server results", async () => {
  const requests = [];
  const turns = [
    [
      { type: "text", text: "Checking" },
      {
        type: "message",
        message: assistant([
          {
            type: "tool_call",
            toolCallId: "s1",
            name: "crm__deals",
            input: {},
          },
          {
            type: "tool_call",
            toolCallId: "a1",
            name: "pick_color",
            input: { from: ["red", "blue"] },
          },
          { type: "tool_call", toolCallId: "a2", name: "fails", input: {} },
        ]),
      },
      {
        type: "tool_result",
        content: { type: "tool_result", toolCallId: "s1", text: "[]" },
      },
      {
        type: "message",
        message: {
          role: "user",
          content: [{ type: "tool_result", toolCallId: "s1", text: "[]" }],
        },
      },
      {
        type: "done",
        stopReason: "tool_use",
        usage: { inputTokens: 10, outputTokens: 4 },
      },
    ],
    [
      {
        type: "message",
        message: assistant([{ type: "text", text: "Blue it is." }]),
      },
      {
        type: "done",
        stopReason: "end_turn",
        usage: { inputTokens: 20, outputTokens: 3 },
      },
    ],
  ];
  const hex = createHexClient({
    site: "demo",
    fetch: async (url, init) => {
      requests.push(JSON.parse(init.body));
      return sseResponse([sse(turns.shift())]);
    },
  });

  const picked = [];
  const chat = hex.ai.conversation({
    model: "general",
    integrationTools: ["crm.*"],
    tools: {
      pick_color: {
        description: "Pick a color.",
        inputSchema: { type: "object" },
        run: async (input) => {
          picked.push(input);
          return { color: "blue" };
        },
      },
      fails: {
        description: "Always fails.",
        inputSchema: { type: "object" },
        run: () => {
          throw new Error("no luck");
        },
      },
    },
  });
  const deltas = [];
  const turn = await chat.send("Pick a color", {
    onEvent: (event) => event.type === "text" && deltas.push(event.text),
  });

  assert.deepEqual(turn, {
    text: "Blue it is.",
    stopReason: "end_turn",
    usage: { inputTokens: 30, outputTokens: 7 },
  });
  assert.deepEqual(deltas, ["Checking"]);
  assert.deepEqual(picked, [{ from: ["red", "blue"] }]);
  assert.deepEqual(
    requests[0].tools.map((tool) => tool.name),
    ["pick_color", "fails"],
  );
  assert.deepEqual(requests[0].integrationTools, ["crm.*"]);

  const second = requests[1].messages;
  assert.equal(second.length, 3);
  assert.deepEqual(
    second[2].content.map((content) => [
      content.toolCallId,
      content.isError ?? false,
    ]),
    [
      ["s1", false],
      ["a1", false],
      ["a2", true],
    ],
  );
  assert.equal(second[2].content[1].text, '{"color":"blue"}');
  assert.equal(second[2].content[2].text, "no luck");
  assert.equal(chat.messages.length, 4);
});

test("AI results preserve cache usage and estimation fields", async () => {
  const usage = {
    inputTokens: 20,
    outputTokens: 3,
    cachedInputTokens: 12,
    cacheWriteTokens: 0,
    estimated: false,
  };
  const hex = createHexClient({
    site: "demo",
    fetch: async (url) =>
      url.endsWith("/complete")
        ? new Response(
            JSON.stringify({
              messages: [],
              text: "",
              stopReason: "end_turn",
              usage,
            }),
          )
        : sseResponse([sse([{ type: "done", stopReason: "end_turn", usage }])]),
  });
  const request = { model: "general", messages: [] };
  assert.deepEqual((await hex.ai.complete(request)).usage, usage);
  assert.deepEqual((await hex.ai.stream(request).done()).usage, usage);
});

test("conversations aggregate cache tokens and estimation across tool rounds", async () => {
  for (const { rounds, expected } of [
    {
      rounds: [
        {
          inputTokens: 10,
          outputTokens: 4,
          cachedInputTokens: 8,
          cacheWriteTokens: 6,
          estimated: true,
        },
        {
          inputTokens: 20,
          outputTokens: 3,
          cachedInputTokens: 12,
          estimated: false,
        },
        { inputTokens: 1, outputTokens: 2, cacheWriteTokens: 2 },
      ],
      expected: {
        inputTokens: 31,
        outputTokens: 9,
        cachedInputTokens: 20,
        cacheWriteTokens: 8,
        estimated: true,
      },
    },
    {
      rounds: [
        undefined,
        {
          inputTokens: 20,
          outputTokens: 3,
          cachedInputTokens: 0,
          cacheWriteTokens: 0,
          estimated: false,
        },
      ],
      expected: {
        inputTokens: 20,
        outputTokens: 3,
        cachedInputTokens: 0,
        cacheWriteTokens: 0,
        estimated: false,
      },
    },
    {
      rounds: [{ inputTokens: 10, outputTokens: 4 }, undefined],
      expected: { inputTokens: 10, outputTokens: 4 },
    },
  ]) {
    let round = 0;
    const hex = createHexClient({
      site: "demo",
      fetch: async () => {
        const usage = rounds[round];
        const last = ++round === rounds.length;
        return sseResponse([
          sse([
            {
              type: "message",
              message: assistant(
                last
                  ? [{ type: "text", text: "Finished" }]
                  : [
                      {
                        type: "tool_call",
                        toolCallId: `call-${round}`,
                        name: "noop",
                        input: {},
                      },
                    ],
              ),
            },
            { type: "done", stopReason: last ? "end_turn" : "tool_use", usage },
          ]),
        ]);
      },
    });
    const chat = hex.ai.conversation({
      model: "general",
      tools: {
        noop: {
          description: "Continue",
          inputSchema: { type: "object" },
          run: () => "ok",
        },
      },
    });
    assert.deepEqual((await chat.send("Start")).usage, expected);
    assert.equal(round, rounds.length);
    // A new send starts fresh rather than reusing the previous turn's totals.
    round = 0;
    assert.deepEqual((await chat.send("Again")).usage, expected);
  }
});

# @crazycatviking/hex

The JavaScript and TypeScript browser client for Hex file storage, JSON documents, and realtime channels.

## Install

```sh
npm install @crazycatviking/hex
```

Use your project's package manager and commit its lockfile. Bundle the package with your application using tools such as Vite. The Hex CLI initializes configuration and agent skills; it does not install or copy this client.

```ts
import { createHexClient } from "@crazycatviking/hex";

const hex = createHexClient({ site: "my-app" });
const tasks = hex.db.collection("tasks");
const task = await tasks.create({ title: "Review the report" });

// Who is viewing, as resolved by the hosting gateway; null when the
// platform has no identity resolver.
const identity = await hex.identity();
```

Deployed applications use same-origin `/api/` requests. The hosting gateway handles authentication. `identity()` is for personalization and convenience branching; authorization is enforced by the platform's site access entries, never by frontend checks.

## Integrations

When the platform has integrations, apps call their typed endpoints through the server. Credentials never reach the browser, and every call is checked against the viewer's permissions and, for some integrations, an admin's approval of the site.

```ts
const integrations = await hex.integrations.list(); // endpoints with `allowed` flags and JSON Schemas
const deals = await hex.integrations.call("hubspot", "deals", {
  pipeline: "default",
});
```

Some integrations call with the viewer's own account, such as Azure DevOps or Google Tasks, so that system's permissions apply. Until the viewer connects that account, calls throw `HexConnectionRequiredError` (status 409). `connect()` opens the platform's connect page in a popup and resolves when it closes. If the popup is blocked, the page navigates there and comes back to `returnURL` (the current page by default).

```ts
import { HexConnectionRequiredError } from "@crazycatviking/hex";

try {
  await hex.integrations.call("google", "tasks", { tasklist: "@default" });
} catch (error) {
  if (error instanceof HexConnectionRequiredError) {
    await hex.integrations.connect(error); // then retry
  }
}

// Or connect on demand and retry once:
const tasks = await hex.integrations.callWithConnect("google", "tasks", {
  tasklist: "@default",
});
```

### Typed wrappers (optional)

`hex integrations codegen` generates a TypeScript module with input and output types for the platform's endpoints, from the same JSON Schemas the server validates against. It imports nothing and wraps `hex.integrations.call`, which keeps working alongside it:

```sh
hex integrations codegen --only hubspot.*,slack.users --out src/hex-integrations.ts
hex integrations codegen --only hubspot.*,slack.users --out src/hex-integrations.ts --check # in CI
```

```ts
import { typedIntegrations } from "./hex-integrations";

const integrations = typedIntegrations(hex.integrations);
const { deals } = await integrations.hubspot.deals({ pipeline: "default" });
```

Exclude the generated file from formatters so `--check` can compare it. Without platform access, as in CI, generate from a saved `hex integrations catalog > catalog.json` with `--catalog catalog.json`.

## AI

`hex.ai` streams model responses with text, readable thinking and tool calls in one shape, whichever model answers. `models()` lists the models the viewer may use.

```ts
const stream = hex.ai.stream({
  model: "claude-opus-5-5",
  thinking: { effort: "medium", show: true },
  messages: [
    { role: "user", content: [{ type: "text", text: "Summarize this week" }] },
  ],
});
for await (const event of stream) {
  if (event.type === "thinking") reasoning.append(event.text);
  if (event.type === "text") answer.append(event.text);
}
```

Iterate the stream, or await `stream.text()`, `stream.finalMessages()` or `stream.done()`, which read the rest of it. An `error` event is yielded to iterators, and the helpers reject with a `HexError`. Pass `{ signal }` to cancel. `hex.ai.complete(request)` answers without streaming.

`conversation()` keeps the history and runs tools for you. Integration endpoints listed in `integrationTools` run on the server with the viewer's own permissions. Tools under `tools` run in the browser, and their results are sent back automatically.

```ts
const chat = hex.ai.conversation({
  model: "claude-opus-5-5",
  system: "You help the sales team.",
  integrationTools: ["hubspot.*"],
  tools: {
    current_selection: {
      description: "The deals the user has selected on screen.",
      inputSchema: { type: "object", properties: {} },
      run: () => selectedDeals(),
    },
  },
});
const turn = await chat.send("Which selected deals are at risk?", {
  onEvent: (event) => event.type === "text" && answer.append(event.text),
});
console.log(turn.stopReason, turn.usage, chat.messages);
```

Thinking blocks in `messages` carry a signature. Send them back unchanged, as `conversation()` does.

See the [Hex documentation](https://github.com/crazycatviking/hex) and the skill installed by `hex init` or `hex skills` for API examples.

## Package from a checkout

Until a version is published to your registry, create a tarball from the repository root:

```sh
npm ci
just pack-client
```

The prepack script compiles the TypeScript client. Install the resulting tarball into the consuming application:

```sh
npm install /path/to/hex/dist/npm/crazycatviking-hex-0.1.0.tgz
```

Maintainers use `just publish-client 0.10.0` from a clean, committed checkout to update the version, install dependencies, build, test, verify the packed package, commit and push the version changes, and publish to npm. Omit the version with `just publish-client` to release the next minor version. Prerelease versions automatically use the `next` npm tag. See [release recipes](../../docs/releases.md). The package includes compiled JavaScript and TypeScript declarations and has no runtime dependencies.

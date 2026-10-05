# AI

The AI capability gives apps and automations a built-in, streaming interface to language models. The platform plugs in a model provider; the server owns authorization, budgets, the tool loop and the wire format, so apps get the same text, thinking and tool events whichever model answers. Apps never hold model credentials.

## Configuration

```go
provider, err := foundry.New(foundry.Config{
    Endpoint:   "https://my-foundry.services.ai.azure.com",
    Credential: managedIdentityCredential, // or APIKey
    Models:     models,                    // foundry.ModelsFromJSON(os.Getenv("HEX_AI_MODELS"))
})
config.AI = &hex.AIConfig{Provider: provider, DailyTokenLimit: 2_000_000}
```

Included providers:

| Package | Talks to |
| --- | --- |
| `server/providers/foundry` | Azure AI Foundry with Entra ID (managed identity) or a key; routes each model to the Anthropic or OpenAI protocol |
| `server/providers/anthropic` | The Anthropic Messages API (also used for Claude models in Foundry) |
| `server/providers/openai` | OpenAI-compatible Chat Completions (Azure OpenAI, other Foundry models) |

A platform can implement `hex.AIProvider` itself: `Models` lists models and `Stream` returns provider-neutral events (below). Models are configured as JSON, for example `HEX_AI_MODELS`:

```json
[
  { "id": "claude-opus-5-5", "name": "Claude Opus 5.5", "protocol": "anthropic", "deployment": "claude-opus-5-5", "thinking": true, "tools": true, "images": true, "maxOutputTokens": 64000 },
  { "id": "gpt-mini", "name": "GPT mini", "protocol": "openai", "deployment": "gpt-mini-prod", "tools": true, "permission": "ai.premium" }
]
```

Using AI needs the `ai` permission (`AIConfig.Permission`), granted like any [integration permission](integrations.md#grants); a model's `permission` can require more. `MaxOutputTokens` (default 16000) caps each model call, `MaxToolRounds` (default 8) the server-side tool loop, and `DailyTokenLimit` each person's tokens per UTC day (counted in process).

## Requests and events

`POST /api/sites/{site}/ai/stream` answers with server-sent events; `POST /api/sites/{site}/ai/complete` waits and returns every message the turn added. `GET /api/sites/{site}/ai/models` lists the models the caller may use.

```json
{
  "model": "claude-opus-5-5",
  "system": "You summarize sales pipelines.",
  "thinking": { "effort": "high", "show": true },
  "messages": [{ "role": "user", "content": [{ "type": "text", "text": "How is the pipeline this week?" }] }],
  "tools": [{ "name": "pick_chart", "description": "Ask the app to render a chart.", "inputSchema": { "type": "object" } }],
  "integrationTools": ["hubspot.deals", "hubspot.pipelines"]
}
```

Content blocks are `text`, `image` (`mediaType`, base64 `data`), `thinking` (`text` plus an opaque `signature` or `redacted` data — send them back unchanged), `tool_call` (`toolCallId`, `name`, `input`) and `tool_result` (`toolCallId`, `text`, `isError`). `thinking.effort` is `low`…`max`; `show` streams readable reasoning (a summary on models that only summarize).

Each SSE event is `event: <type>` with a JSON `data:` line; `: keep-alive` comments keep idle connections open.

| Event | Data |
| --- | --- |
| `text`, `thinking` | `text` delta |
| `tool_call` | A complete tool call in `content` |
| `tool_result` | The server ran an integration tool; `content` is its result |
| `message` | A complete message to append to the conversation: the assistant's output, or the user message holding server tool results |
| `done` | `stopReason` (`end_turn`, `max_tokens`, `tool_use`, `refusal`) and total `usage` |
| `error` | `error` describes a failure after the stream started |

Validation and permission errors before the stream starts are ordinary JSON errors (400, 403, 429).

## Tools

**Integration tools** run on the server. `integrationTools` names endpoints or patterns; the server offers those the caller may use (unapproved or ungranted ones are left out) as tools named `<integration>__<endpoint>`, runs each call with the caller's grants, streams `tool_result` events and continues until the model finishes. A model therefore never sees more than the person could. Failures, missing connections and oversized results (over 256 KiB) are returned to the model as error results.

**App tools** (`tools`) run in the app. When the model calls one, the turn ends with `stopReason: "tool_use"`; the app runs it and continues the conversation with a user message of `tool_result` blocks. If the server also ran integration tools in that turn, the app appends its results to the server's results message rather than adding another one. The client's `conversation()` helper does all of this:

```ts
const chat = hex.ai.conversation({
  model: 'claude-opus-5-5',
  thinking: { effort: 'medium', show: true },
  integrationTools: ['hubspot.*'],
  tools: {
    pick_chart: { description: 'Render a chart in the app.', inputSchema: { type: 'object' }, run: async input => renderChart(input) },
  },
});
await chat.send('Chart this quarter’s won deals', {
  onEvent: event => {
    if (event.type === 'thinking') showThinking(event.text);
    if (event.type === 'text') appendAnswer(event.text);
  },
});
```

## Hosting notes

Streaming responses carry `X-Accel-Buffering: no`, which NGINX honors. Verify that any other proxy in front of the server (for example Container Apps ingress with Easy Auth) streams responses rather than buffering them. Usage is logged per call with site, caller, model and token counts.

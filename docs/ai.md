# AI

The AI capability gives apps and automations a built-in, streaming interface to language models. The platform plugs in a model provider; the server owns authorization, budgets, the tool loop and the wire format, so apps get the same text, thinking and tool events whichever model answers. Apps never hold model credentials.

## Configuration

```go
provider, err := foundry.New(foundry.Config{
    Endpoint:   "https://my-foundry.services.ai.azure.com",
    Credential: managedIdentityCredential, // or APIKey
    Models:     models,                    // foundry.ModelsFromJSON(os.Getenv("HEX_AI_MODELS"))
})
config.AI = &hex.AIConfig{Provider: provider, Limits: hex.AILimits{PlatformMonthly: 1000, SiteMonthly: 100, PersonMonthly: 20}}
config.AIUsage = database // PostgreSQL or memory.NewAIUsageStore()
```

Included providers:

| Package | Talks to |
| --- | --- |
| `server/providers/foundry` | Azure AI Foundry with Entra ID (managed identity) or a key; routes each model to the Anthropic Messages API, OpenAI Chat Completions or the OpenAI Responses API |
| `server/providers/anthropic` | The Anthropic Messages API (also used for Claude models in Foundry) |
| `server/providers/openai` | OpenAI-compatible Chat Completions or, with `API: openai.APIResponses`, the Responses API (Azure OpenAI, other Foundry models) |

A platform can implement `hex.AIProvider` itself: `Models` lists models and `Stream` returns provider-neutral events (below). Models are configured as JSON, for example `HEX_AI_MODELS`:

```json
[
  { "id": "claude-sonnet-5-5", "name": "Claude Sonnet 5.5", "protocol": "anthropic", "deployment": "claude-sonnet-5-5", "thinking": true, "tools": true, "images": true, "maxOutputTokens": 64000, "restricted": true,
    "price": { "input": 2, "cachedInput": 0.2, "cacheWrite": 2.5, "output": 10 } },
  { "id": "gpt-6-luna", "name": "GPT-6 Luna", "protocol": "openai-responses", "deployment": "gpt-6-luna", "thinking": true, "tools": true, "images": true,
    "price": { "input": 0.11, "cachedInput": 0.011, "output": 0.55 } },
  { "id": "gpt-mini", "name": "GPT mini", "protocol": "openai", "deployment": "gpt-mini-prod", "tools": true,
    "price": { "input": 0.25, "output": 2 } }
]
```

Use `openai-responses` for OpenAI reasoning models (GPT-5, GPT-6 and later). Over Chat Completions (`openai`) they refuse function tools together with a reasoning effort, stream no readable reasoning, and lose their reasoning between tool rounds. The Responses API streams reasoning summaries as `thinking` when the app sets `show`, and carries the model's encrypted reasoning in `thinking` blocks so it continues after tool calls; requests are sent with `store: false`, so the service keeps nothing between turns. `openai` remains for models and services that offer only Chat Completions.

Using AI needs the `ai` permission (`AIConfig.Permission`), granted like any [integration permission](integrations.md#grants). `MaxOutputTokens` (default 16000) caps each model call and `MaxToolRounds` (default 8) the server-side tool loop.

## Restricted models

A model marked `restricted`, typically an expensive one, works only on sites a platform admin has enabled it for in the site's **AI** tab. On an enabled site it works for everyone who may use AI there and for the site's automations, so an app built around it behaves the same for every visitor. Elsewhere it is left out of `/ai/models`, and a request for it is refused with 403. The budgets below still cap what the site and each person spend. The enabled models are stored with the site's budget, so restricted models need `Config.AIUsage`; without it they are unavailable.

## Usage, cost and budgets

With `Config.AIUsage`, every model call — each round of a tool loop is one — is stored with its exact token counts as the provider reports them: uncached input, cache reads, cache writes and output (reasoning tokens are part of output). Its cost comes from the model's `price` in US dollars per million tokens; cache reads default to a tenth of the input price and cache writes to 1.25 times it. Records name the site and the person (`user:<id>`) or automation (`automation:<site>/<name>`). When a stream ends without the provider's usage, for example because the app disconnected, usage is estimated from the request and the streamed text and marked as estimated. A model without a price is recorded but not counted against budgets, and the server logs a warning.

Monthly budgets in US dollars (calendar months, UTC) are checked before every model call:

| Budget | Applies to |
| --- | --- |
| Platform | All AI spending |
| Site default, or a site's own limit | Each site, including its automations; a site can also have AI turned off |
| Person default, or the highest matching override | Each person across all sites; overrides name a `role:`, `group:` or `user:` principal, such as `group:<object id>` |

A call is refused with 429 (403 when AI is off for the site) once any budget that applies is used up; the message says which. A call that starts below a limit can end slightly above it. `AIConfig.Limits` sets the starting budgets; platform admins change them, set each site's limit, turn AI off for a site, enable restricted models for it and add overrides in the portal, where saved budgets take precedence.

The portal shows spending:

- **Admin → AI spend** (`/admin/ai`): the platform's month against its budget, daily spending, every site against its limit, spending by model and the top spenders, and the budget forms.
- Each site's **AI** tab: the site's month against its limit, daily spending, and spending by person, automation and model. Owners see it and which restricted models are enabled; platform admins can also change the site's limit, turn AI off and enable restricted models.

Costs are what the configured prices say. Reconcile them monthly with Azure Cost Management (Claude in Foundry appears as Marketplace charges); a difference of more than a few percent means a price is out of date. Azure cannot attribute spending to sites or people, which is what these records are for.

## Prompt caching

The Anthropic provider (and Claude through Foundry) caches prompts: one breakpoint on the system prompt and automatic caching of the growing conversation, so each turn reads the earlier ones from the cache at a fraction of the input price. Short prompts below the model's minimum (512 to 4,096 tokens) are simply not cached. Turn it off with `DisablePromptCaching`. OpenAI-compatible models cache automatically on the provider's side; their cached tokens are recorded and priced the same way.

## Requests and events

`POST /api/sites/{site}/ai/stream` answers with server-sent events; `POST /api/sites/{site}/ai/complete` waits and returns every message the turn added. `GET /api/sites/{site}/ai/models` lists the models the caller may use on the site.

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

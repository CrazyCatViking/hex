import { HexError, jsonBody, type Requester } from "./http.js";
import type { JSONSchema } from "./integrations.js";
import { readServerSentEvents } from "./sse.js";

export interface AIModel {
  id: string;
  name: string;
  description?: string;
  thinking: boolean;
  tools: boolean;
  images: boolean;
  contextTokens?: number;
  maxOutputTokens?: number;
  permission?: string;
}

/**
 * One block of a message. Send thinking blocks back unchanged, including
 * their signature, so the model can continue its reasoning.
 */
export type AIContent =
  | { type: "text"; text: string }
  | { type: "image"; mediaType: string; data: string }
  | { type: "thinking"; text?: string; signature?: string; redacted?: string }
  | {
      type: "tool_call";
      toolCallId: string;
      name: string;
      input?: unknown;
    }
  | {
      type: "tool_result";
      toolCallId: string;
      name?: string;
      text?: string;
      isError?: boolean;
    };

export interface AIMessage {
  role: "user" | "assistant";
  content: AIContent[];
}

export interface AITool {
  name: string;
  description: string;
  inputSchema: JSONSchema;
}

export interface AIThinking {
  /** low, medium, high, xhigh or max; mapped to what the model supports. */
  effort?: string;
  /** Stream readable reasoning as thinking events. */
  show?: boolean;
}

export interface AIRequest {
  model: string;
  system?: string;
  messages: AIMessage[];
  /** Tools the app runs itself; see conversation() for an automatic loop. */
  tools?: AITool[];
  thinking?: AIThinking;
  maxTokens?: number;
  /**
   * Integration endpoints the server offers to the model and runs with the
   * viewer's own permissions, as "integration.endpoint" or "integration.*".
   */
  integrationTools?: string[];
}

export type AIStopReason = "end_turn" | "max_tokens" | "tool_use" | "refusal";

export interface AIUsage {
  inputTokens: number;
  outputTokens: number;
}

/**
 * One streamed event: text and thinking deltas, complete tool calls, the
 * results of tools the server ran, complete messages to append to the
 * conversation, the final outcome, or an error after streaming started.
 */
export type AIEvent =
  | { type: "text"; text: string }
  | { type: "thinking"; text: string }
  | {
      type: "tool_call";
      content: Extract<AIContent, { type: "tool_call" }>;
    }
  | {
      type: "tool_result";
      content: Extract<AIContent, { type: "tool_result" }>;
    }
  | { type: "message"; message: AIMessage }
  | { type: "done"; stopReason: AIStopReason; usage?: AIUsage }
  | { type: "error"; error: string };

export interface AICompletion {
  /** Every message the turn added, in order. */
  messages: AIMessage[];
  text: string;
  stopReason: AIStopReason;
  usage: AIUsage;
}

export interface StreamOptions {
  signal?: AbortSignal;
}

/**
 * A streamed turn. Iterate it for events, or await one of the helpers,
 * which read the rest of the stream. An "error" event is yielded to
 * iterators, and the helpers reject with a HexError.
 */
export class AIStream implements AsyncIterable<AIEvent> {
  readonly #source: AsyncIterator<AIEvent>;
  readonly #events: AIEvent[] = [];
  #finished = false;

  constructor(source: AsyncIterable<AIEvent>) {
    this.#source = source[Symbol.asyncIterator]();
  }

  async *[Symbol.asyncIterator](): AsyncIterator<AIEvent> {
    while (!this.#finished) {
      const next = await this.#source.next();
      if (next.done) {
        this.#finished = true;
        return;
      }
      this.#events.push(next.value);
      yield next.value;
    }
  }

  async #drain(): Promise<AIEvent[]> {
    for await (const event of this) {
      void event;
    }
    const failure = this.#events.find((event) => event.type === "error");
    if (failure?.type === "error") {
      throw new HexError(502, failure.error);
    }
    return this.#events;
  }

  /** Every complete message the turn added to the conversation. */
  async finalMessages(): Promise<AIMessage[]> {
    const events = await this.#drain();
    return events.flatMap((event) =>
      event.type === "message" ? [event.message] : [],
    );
  }

  /** The text of the turn's last assistant message. */
  async text(): Promise<string> {
    const messages = await this.finalMessages();
    const assistant = messages.filter(
      (message) => message.role === "assistant",
    );
    const last = assistant[assistant.length - 1];
    return last ? messageText(last) : "";
  }

  /** The final event, with the stop reason and token usage. */
  async done(): Promise<Extract<AIEvent, { type: "done" }>> {
    const events = await this.#drain();
    const done = events.find((event) => event.type === "done");
    if (done?.type !== "done") {
      throw new HexError(502, "the stream ended without a result");
    }
    return done;
  }
}

function messageText(message: AIMessage): string {
  return message.content
    .map((content) => (content.type === "text" ? content.text : ""))
    .join("");
}

async function* streamEvents(response: Response): AsyncGenerator<AIEvent> {
  if (!response.body) {
    throw new HexError(502, "the response has no body");
  }
  for await (const event of readServerSentEvents(response.body)) {
    yield JSON.parse(event.data) as AIEvent;
  }
}

/** A tool the app runs in the browser when the model calls it. */
export interface ConversationTool {
  description: string;
  inputSchema: JSONSchema;
  run: (input: unknown) => Promise<unknown> | unknown;
}

export interface ConversationOptions {
  model: string;
  system?: string;
  thinking?: AIThinking;
  maxTokens?: number;
  integrationTools?: string[];
  tools?: Record<string, ConversationTool>;
  /** Rounds of app tools per send(); default 8. */
  maxToolRounds?: number;
}

export interface SendOptions {
  signal?: AbortSignal;
  /** Called with every event, for rendering text and thinking as it streams. */
  onEvent?: (event: AIEvent) => void;
}

export interface ConversationTurn {
  text: string;
  stopReason: AIStopReason;
  usage: AIUsage;
}

const defaultToolRounds = 8;

export function createAI(request: Requester, root: string) {
  async function models(): Promise<AIModel[]> {
    const response = await request(`${root}/ai/models`);
    return response.json() as Promise<AIModel[]>;
  }

  function stream(body: AIRequest, options: StreamOptions = {}): AIStream {
    const events = (async function* () {
      const init = jsonBody("POST", body);
      const headers = new Headers(init.headers);
      headers.set("Accept", "text/event-stream");
      const response = await request(`${root}/ai/stream`, {
        ...init,
        headers,
        signal: options.signal,
      });
      // Buffered events must not outlive an abort, whatever the transport.
      for await (const event of streamEvents(response)) {
        options.signal?.throwIfAborted();
        yield event;
      }
    })();
    return new AIStream(events);
  }

  async function complete(body: AIRequest): Promise<AICompletion> {
    const response = await request(
      `${root}/ai/complete`,
      jsonBody("POST", body),
    );
    return response.json() as Promise<AICompletion>;
  }

  /**
   * Keeps a conversation's history and runs the app's own tools: when the
   * model calls one, its result is sent back and the turn continues.
   * Integration tools run on the server within the same stream.
   */
  function conversation(options: ConversationOptions) {
    const messages: AIMessage[] = [];
    const tools = options.tools ?? {};
    const toolDefinitions: AITool[] = Object.entries(tools).map(
      ([name, tool]) => ({
        name,
        description: tool.description,
        inputSchema: tool.inputSchema,
      }),
    );

    async function streamTurn(sendOptions: SendOptions) {
      const turn = stream(
        {
          model: options.model,
          system: options.system,
          thinking: options.thinking,
          maxTokens: options.maxTokens,
          integrationTools: options.integrationTools,
          tools: toolDefinitions.length > 0 ? toolDefinitions : undefined,
          messages,
        },
        { signal: sendOptions.signal },
      );
      const start = messages.length;
      let done: Extract<AIEvent, { type: "done" }> | undefined;
      for await (const event of turn) {
        sendOptions.onEvent?.(event);
        if (event.type === "message") {
          messages.push(event.message);
        } else if (event.type === "done") {
          done = event;
        } else if (event.type === "error") {
          throw new HexError(502, event.error);
        }
      }
      if (!done) {
        throw new HexError(502, "the stream ended without a result");
      }
      return { done, added: messages.slice(start) };
    }

    async function runAppTools(added: AIMessage[]): Promise<AIContent[]> {
      const answered = new Set<string>();
      const calls: Extract<AIContent, { type: "tool_call" }>[] = [];
      for (const message of added) {
        for (const content of message.content) {
          if (content.type === "tool_result") {
            answered.add(content.toolCallId);
          } else if (content.type === "tool_call" && tools[content.name]) {
            calls.push(content);
          }
        }
      }
      const pending = calls.filter((call) => !answered.has(call.toolCallId));

      return Promise.all(
        pending.map(async (call): Promise<AIContent> => {
          const tool = tools[call.name];
          try {
            const output = await tool!.run(call.input ?? {});
            const text =
              typeof output === "string"
                ? output
                : JSON.stringify(output ?? null);
            return {
              type: "tool_result",
              toolCallId: call.toolCallId,
              name: call.name,
              text,
            };
          } catch (error) {
            const text = error instanceof Error ? error.message : String(error);
            return {
              type: "tool_result",
              toolCallId: call.toolCallId,
              name: call.name,
              text,
              isError: true,
            };
          }
        }),
      );
    }

    // All results for one assistant turn belong in a single user message,
    // so app results join the server's results when it sent some.
    function appendResults(results: AIContent[]) {
      const last = messages[messages.length - 1];
      const lastHoldsResults =
        last?.role === "user" &&
        last.content.length > 0 &&
        last.content.every((content) => content.type === "tool_result");
      if (lastHoldsResults) {
        last.content.push(...results);
        return;
      }
      messages.push({ role: "user", content: results });
    }

    async function send(
      content: string | AIContent[],
      sendOptions: SendOptions = {},
    ): Promise<ConversationTurn> {
      messages.push({
        role: "user",
        content:
          typeof content === "string"
            ? [{ type: "text", text: content }]
            : content,
      });
      const usage: AIUsage = { inputTokens: 0, outputTokens: 0 };
      const rounds = options.maxToolRounds ?? defaultToolRounds;
      let stopReason: AIStopReason = "end_turn";

      for (let round = 0; round < rounds; round++) {
        const { done, added } = await streamTurn(sendOptions);
        usage.inputTokens += done.usage?.inputTokens ?? 0;
        usage.outputTokens += done.usage?.outputTokens ?? 0;
        stopReason = done.stopReason;
        if (stopReason !== "tool_use") {
          break;
        }

        const results = await runAppTools(added);
        if (results.length > 0) {
          appendResults(results);
          continue;
        }
        // The server stopped at its own tool round limit with results last;
        // continuing lets the model use them.
        if (messages[messages.length - 1]?.role !== "user") {
          break;
        }
      }

      const assistant = messages.filter(
        (message) => message.role === "assistant",
      );
      const last = assistant[assistant.length - 1];
      return { text: last ? messageText(last) : "", stopReason, usage };
    }

    return { messages, send };
  }

  return { models, stream, complete, conversation };
}

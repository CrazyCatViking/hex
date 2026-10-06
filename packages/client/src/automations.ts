/** Editor-only contracts for JavaScript executed by Hex's automation sandbox. */
export type JSONValue =
  null | boolean | number | string | JSONValue[] | { [key: string]: JSONValue };

export interface AutomationClock {
  readonly iso: string;
  readonly date: string;
  readonly time: string;
  readonly year: number;
  readonly month: number;
  readonly day: number;
  readonly monthDay: string;
  readonly weekday: string;
  readonly week: number;
  readonly weekYear: number;
  readonly yesterday: string;
  readonly weekStart: string;
  readonly previousWeekStart: string;
  readonly previousWeekEnd: string;
  readonly unix: number;
}

export interface WouldCall {
  wouldCall: string;
  input: JSONValue;
}

export interface WouldRun {
  wouldRun: string;
  input: JSONValue;
}

export interface WouldSave {
  wouldSave: string;
  id: string;
  data: Record<string, JSONValue>;
}

export interface AutomationAIRequest {
  model: string;
  prompt: string;
  system?: string;
  maxTokens?: number;
  thinking?: { effort?: string; show?: boolean };
  /** Integration names or patterns; resolved with the site's grants. */
  tools?: string[];
}

export interface AutomationAIResult {
  text: string;
  stopReason: "end_turn" | "max_tokens" | "tool_use" | "refusal";
  usage: {
    inputTokens: number;
    outputTokens: number;
    cachedInputTokens?: number;
    cacheWriteTokens?: number;
    estimated?: boolean;
  };
}

export interface AutomationDocument<T = Record<string, JSONValue>> {
  id: string;
  data: T;
}

/**
 * Passed to a script's default export; also available as globalThis.hex.
 * Operations are authorized server-side as the automation's site. Values and
 * API objects are frozen. Calls execute sequentially, including Promise.all.
 */
export interface AutomationContext {
  readonly site: string;
  readonly automation: string;
  readonly run: Readonly<{
    id: string;
    trigger: "schedule" | "manual" | "test";
    dryRun: boolean;
  }>;
  readonly now: AutomationClock;
  /** Read endpoints return T; dry-run write endpoints return a preview. */
  call<T = JSONValue>(
    endpoint: `${string}.${string}`,
    input?: Record<string, JSONValue>,
  ): Promise<T | WouldCall>;

  /** Actions are always simulated in dry runs, including read-only actions. */
  action<T = JSONValue>(
    name: string,
    input?: Record<string, JSONValue>,
  ): Promise<T | WouldRun>;

  readonly ai: {
    /** Executes in dry runs too, subject to AI grants and budgets. */
    complete(request: AutomationAIRequest): Promise<AutomationAIResult>;
  };

  readonly db: {
    /** Reads this site's documents; default limit 100, maximum 1000. */
    query<T = Record<string, JSONValue>>(
      collection: string,
      options?: { where?: Record<string, JSONValue>; limit?: number },
    ): Promise<AutomationDocument<T>[]>;

    /** Creates a document, or replaces the document with the specified id. */
    save(
      collection: string,
      data: Record<string, JSONValue>,
      options?: { id?: string },
    ): Promise<{ id: string; collection: string } | WouldSave>;
  };

  /** Records bounded, structured data in run history. */
  log(message: string, data?: JSONValue): void;
}

export type AutomationHandler = (
  hex: AutomationContext,
) => JSONValue | void | Promise<JSONValue | void>;

declare global {
  /** Present only inside the automation JavaScript sandbox. */
  var hex: AutomationContext;

  interface Console {
    log(...data: unknown[]): void;
    info(...data: unknown[]): void;
    warn(...data: unknown[]): void;
    error(...data: unknown[]): void;
  }
  var console: Console;
}

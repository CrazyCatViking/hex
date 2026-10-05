import {
  HexConnectionRequiredError,
  HexError,
  jsonBody,
  type ConnectionRequired,
  type Requester,
} from "./http.js";

/** A JSON Schema document. */
export type JSONSchema = Record<string, unknown>;

/** One endpoint's contract and whether the viewer may call it. */
export interface IntegrationEndpoint {
  name: string;
  description: string;
  permission: string;
  write: boolean;
  allowed: boolean;
  inputSchema?: JSONSchema;
  outputSchema?: JSONSchema;
}

/** The viewer's own account with a connector-backed integration. */
export interface ConnectionStatus {
  name: string;
  title: string;
  description?: string;
  connected: boolean;
  account?: string;
  connectedAt?: string;
  connectURL: string;
}

/** An integration as the viewer sees it on this site. */
export interface Integration {
  name: string;
  title: string;
  description?: string;
  requiresApproval: boolean;
  /** "approved" or "requested" when approval is required; empty otherwise. */
  approval?: string;
  /** Present when endpoints call with the viewer's own connected account. */
  connection?: ConnectionStatus;
  endpoints: IntegrationEndpoint[];
}

export interface ConnectOptions {
  /** Where to return after connecting when a popup cannot be opened. */
  returnURL?: string;
}

const integrationNamePattern = /^[a-z0-9][a-z0-9-]{0,39}$/;
const popupPollMilliseconds = 500;

function validateIntegrationName(value: string): string {
  if (!integrationNamePattern.test(value)) {
    throw new Error(`Invalid integration or endpoint name: ${value}`);
  }

  return value;
}

/**
 * Opens the platform's connect page in a popup and resolves when it closes.
 * When the browser blocks the popup, the current page navigates there
 * instead and returns afterwards, so the promise never resolves.
 */
function openConnectPage(url: string, options: ConnectOptions): Promise<void> {
  const popup = globalThis.open?.(
    url,
    "hex-connect",
    "popup,width=520,height=720",
  );
  if (!popup) {
    const target = new URL(url);
    target.searchParams.set(
      "return",
      options.returnURL ?? globalThis.location.href,
    );
    globalThis.location.assign(target.toString());
    return new Promise(() => undefined);
  }

  return new Promise((resolve) => {
    const timer = setInterval(() => {
      if (popup.closed) {
        clearInterval(timer);
        resolve();
      }
    }, popupPollMilliseconds);
  });
}

export function createIntegrations(request: Requester, root: string) {
  async function list(): Promise<Integration[]> {
    const response = await request(`${root}/integrations`);
    return response.json() as Promise<Integration[]>;
  }

  async function call<T = unknown>(
    integration: string,
    endpoint: string,
    input: unknown = {},
  ): Promise<T> {
    const path = `${root}/integrations/${validateIntegrationName(integration)}/${validateIntegrationName(endpoint)}`;
    const response = await request(path, jsonBody("POST", input));
    return response.json() as Promise<T>;
  }

  /**
   * Connects the viewer's account, given the error a call threw or a
   * connector name. Resolves once the connect window closes; check
   * `list()` or retry the call to see whether it succeeded.
   */
  async function connect(
    connector: string | HexConnectionRequiredError | ConnectionRequired,
    options: ConnectOptions = {},
  ): Promise<void> {
    if (typeof connector !== "string") {
      const details =
        connector instanceof HexConnectionRequiredError
          ? connector.connect
          : connector;
      return openConnectPage(details.url, options);
    }

    const integrations = await list();
    const status = integrations
      .map((integration) => integration.connection)
      .find((connection) => connection?.name === connector);
    if (!status) {
      throw new HexError(404, `No integration connects with ${connector}`);
    }
    return openConnectPage(status.connectURL, options);
  }

  /** Calls an endpoint, connecting the viewer's account first when needed. */
  async function callWithConnect<T = unknown>(
    integration: string,
    endpoint: string,
    input: unknown = {},
    options: ConnectOptions = {},
  ): Promise<T> {
    try {
      return await call<T>(integration, endpoint, input);
    } catch (error) {
      if (!(error instanceof HexConnectionRequiredError)) {
        throw error;
      }
      await connect(error, options);
      return call<T>(integration, endpoint, input);
    }
  }

  return { list, call, connect, callWithConnect };
}

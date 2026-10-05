export class HexError extends Error {
  constructor(
    public readonly status: number,
    message: string,
    options?: ErrorOptions,
  ) {
    super(message, options);
    this.name = "HexError";
  }
}

/** Where to connect the account an integration endpoint needs. */
export interface ConnectionRequired {
  connector: string;
  title: string;
  url: string;
}

/**
 * Thrown with status 409 when an integration endpoint calls with the
 * viewer's own account and they have not connected it yet. Pass it to
 * `hex.integrations.connect()` and retry.
 */
export class HexConnectionRequiredError extends HexError {
  constructor(
    message: string,
    public readonly connect: ConnectionRequired,
  ) {
    super(409, message);
    this.name = "HexConnectionRequiredError";
  }
}

/** Sends one API request and throws HexError for error responses. */
export type Requester = (path: string, init?: RequestInit) => Promise<Response>;

export function jsonBody(method: string, data: unknown): RequestInit {
  return {
    method,
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(data),
  };
}

export async function responseError(response: Response): Promise<HexError> {
  try {
    const body = (await response.json()) as {
      error?: string;
      connect?: ConnectionRequired;
    };
    const message = body.error ?? response.statusText;
    if (response.status === 409 && body.connect?.url) {
      return new HexConnectionRequiredError(message, body.connect);
    }
    return new HexError(response.status, message);
  } catch (cause) {
    return new HexError(response.status, response.statusText, { cause });
  }
}

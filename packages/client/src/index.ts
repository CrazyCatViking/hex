export interface Capabilities {
  version: number;
  files: boolean;
  database: boolean;
  realtime: boolean;
  sites: boolean;
  identity?: boolean;
  accessControl?: boolean;
  maxUploadBytes: number;
}

export interface Identity {
  provider?: string;
  id: string;
  name?: string;
  groups?: string[];
  roles?: string[];
}

export interface StoredFile {
  key: string;
  size: number;
}

export interface Document<T> {
  id: string;
  data: T;
}

export interface ClientOptions {
  site: string;
  baseURL?: string;
  fetch?: typeof fetch;
}

export interface ListOptions {
  after?: string;
  limit?: number;
}

export interface RealtimeOptions<T> {
  onMessage: (message: T) => void;
  onClose?: (event: CloseEvent) => void;
  onError?: (event: Event) => void;
}

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

function validateName(value: string): string {
  if (!/^[a-zA-Z0-9][a-zA-Z0-9_-]{0,63}$/.test(value)) {
    throw new Error(`Invalid Hex identifier: ${value}`);
  }

  return value;
}

function encodeFilePath(value: string): string {
  const parts = value.split("/");
  const invalid = parts.some(
    (part) => !part || part.startsWith(".") || /[\\\0]/.test(part),
  );
  if (invalid) {
    throw new Error("Invalid file key");
  }

  return parts.map(encodeURIComponent).join("/");
}

function jsonBody(method: string, data: unknown): RequestInit {
  return {
    method,
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(data),
  };
}

async function responseError(response: Response): Promise<HexError> {
  try {
    const body = (await response.json()) as { error?: string };
    return new HexError(response.status, body.error ?? response.statusText);
  } catch (cause) {
    return new HexError(response.status, response.statusText, { cause });
  }
}

function connectChannel<T>(url: URL, options: RealtimeOptions<T>) {
  url.protocol = url.protocol === "https:" ? "wss:" : "ws:";
  const socket = new WebSocket(url);
  const ready = new Promise<void>((resolve, reject) => {
    socket.addEventListener("open", () => resolve(), { once: true });
    socket.addEventListener(
      "error",
      () => reject(new Error("WebSocket connection failed")),
      { once: true },
    );
    socket.addEventListener(
      "close",
      () => reject(new Error("WebSocket closed before opening")),
      { once: true },
    );
  });

  socket.addEventListener("message", (event) => {
    const message = JSON.parse(String(event.data)) as T;
    options.onMessage(message);
  });
  if (options.onClose) {
    socket.addEventListener("close", options.onClose);
  }
  if (options.onError) {
    socket.addEventListener("error", options.onError);
  }

  return {
    ready,
    async send(message: T) {
      await ready;
      if (socket.readyState !== WebSocket.OPEN) {
        throw new Error("WebSocket is not open");
      }

      socket.send(JSON.stringify(message));
    },
    close() {
      socket.close(1000, "client closed");
    },
  };
}

export function createHexClient(options: ClientOptions) {
  const baseURL = (options.baseURL ?? "").replace(/\/$/, "");
  const transport = options.fetch ?? globalThis.fetch.bind(globalThis);
  const root = `/api/sites/${validateName(options.site)}`;

  async function request(
    path: string,
    init: RequestInit = {},
  ): Promise<Response> {
    const headers = new Headers(init.headers);
    headers.set("X-Hex-Request", "1");

    const response = await transport(baseURL + path, {
      ...init,
      headers,
      credentials: "same-origin",
      redirect: "error",
    });
    if (!response.ok) {
      throw await responseError(response);
    }

    return response;
  }

  async function requestJSON<T>(path: string, init?: RequestInit): Promise<T> {
    const response = await request(path, init);
    return response.json() as Promise<T>;
  }

  function collection<T extends object = Record<string, unknown>>(
    name: string,
  ) {
    const path = `${root}/db/${validateName(name)}`;

    return {
      list(options: ListOptions = {}) {
        const query = new URLSearchParams();
        if (options.after !== undefined) {
          query.set("after", options.after);
        }
        if (options.limit !== undefined) {
          query.set("limit", String(options.limit));
        }

        return requestJSON<Document<T>[]>(`${path}?${query}`);
      },
      get(id: string) {
        return requestJSON<Document<T>>(`${path}/${validateName(id)}`);
      },
      create(data: T) {
        return requestJSON<Document<T>>(path, jsonBody("POST", data));
      },
      set(id: string, data: T) {
        return requestJSON<Document<T>>(
          `${path}/${validateName(id)}`,
          jsonBody("PUT", data),
        );
      },
      async delete(id: string) {
        await request(`${path}/${validateName(id)}`, { method: "DELETE" });
      },
    };
  }

  return {
    capabilities() {
      return requestJSON<Capabilities>("/api/hex/capabilities");
    },
    async identity(): Promise<Identity | null> {
      try {
        return await requestJSON<Identity>("/api/hex/me");
      } catch (error) {
        const unavailable =
          error instanceof HexError &&
          (error.status === 404 || error.status === 401);
        if (unavailable) {
          return null;
        }

        throw error;
      }
    },
    files: {
      list() {
        return requestJSON<StoredFile[]>(`${root}/files`);
      },
      upload(key: string, data: Blob | ArrayBuffer | Uint8Array<ArrayBuffer>) {
        return requestJSON<StoredFile>(`${root}/files/${encodeFilePath(key)}`, {
          method: "PUT",
          body: data,
        });
      },
      async download(key: string) {
        const response = await request(`${root}/files/${encodeFilePath(key)}`);
        return response.blob();
      },
      url(key: string) {
        return `${baseURL}${root}/files/${encodeFilePath(key)}`;
      },
      async delete(key: string) {
        await request(`${root}/files/${encodeFilePath(key)}`, {
          method: "DELETE",
        });
      },
    },
    db: { collection },
    realtime: {
      connect<T = unknown>(channel: string, options: RealtimeOptions<T>) {
        const path = `${root}/realtime/${validateName(channel)}`;
        const url = new URL(baseURL + path, globalThis.location?.href);
        return connectChannel(url, options);
      },
    },
  };
}

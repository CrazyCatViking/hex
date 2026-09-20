export interface Capabilities {
  version: number;
  files: boolean;
  database: boolean;
  realtime: boolean;
  sites: boolean;
  maxUploadBytes: number;
}

export interface StoredFile { key: string; size: number }
export interface Document<T> { id: string; data: T }
export interface ClientOptions {
  site: string;
  baseURL?: string;
  fetch?: typeof fetch;
}

export class HexError extends Error {
  constructor(public readonly status: number, message: string) {
    super(message);
    this.name = "HexError";
  }
}

function name(value: string): string {
  if (!/^[a-zA-Z0-9][a-zA-Z0-9_-]{0,63}$/.test(value)) throw new Error(`Invalid Hex identifier: ${value}`);
  return value;
}

function filePath(value: string): string {
  const parts = value.split("/");
  if (parts.some(part => !part || part.startsWith(".") || /[\\\0]/.test(part))) throw new Error("Invalid file key");
  return parts.map(encodeURIComponent).join("/");
}

export function createHexClient(options: ClientOptions) {
  const baseURL = (options.baseURL ?? "").replace(/\/$/, "");
  const transport = options.fetch ?? globalThis.fetch.bind(globalThis);
  const root = `/api/sites/${name(options.site)}`;

  async function request(path: string, init: RequestInit = {}): Promise<Response> {
    const headers = new Headers(init.headers);
    headers.set("X-Hex-Request", "1");
    const response = await transport(baseURL + path, { ...init, headers, credentials: "same-origin", redirect: "error" });
    if (!response.ok) {
      let message = response.statusText;
      try { message = (await response.json() as { error?: string }).error ?? message; } catch {}
      throw new HexError(response.status, message);
    }
    return response;
  }

  async function json<T>(path: string, init?: RequestInit): Promise<T> {
    return (await request(path, init)).json() as Promise<T>;
  }

  const body = (data: unknown, method: string): RequestInit => ({ method, headers: { "Content-Type": "application/json" }, body: JSON.stringify(data) });

  return {
    capabilities: () => json<Capabilities>("/api/hex/capabilities"),
    files: {
      list: () => json<StoredFile[]>(`${root}/files`),
      upload: (key: string, data: Blob | ArrayBuffer | Uint8Array<ArrayBuffer>) => json<StoredFile>(`${root}/files/${filePath(key)}`, { method: "PUT", body: data }),
      download: async (key: string) => (await request(`${root}/files/${filePath(key)}`)).blob(),
      url: (key: string) => `${baseURL}${root}/files/${filePath(key)}`,
      delete: async (key: string) => { await request(`${root}/files/${filePath(key)}`, { method: "DELETE" }); },
    },
    db: {
      collection<T extends object = Record<string, unknown>>(collection: string) {
        const path = `${root}/db/${name(collection)}`;
        return {
          list: (options: { after?: string; limit?: number } = {}) => {
            const query = new URLSearchParams();
            if (options.after !== undefined) query.set("after", options.after);
            if (options.limit !== undefined) query.set("limit", String(options.limit));
            return json<Document<T>[]>(`${path}?${query}`);
          },
          get: (id: string) => json<Document<T>>(`${path}/${name(id)}`),
          create: (data: T) => json<Document<T>>(path, body(data, "POST")),
          set: (id: string, data: T) => json<Document<T>>(`${path}/${name(id)}`, body(data, "PUT")),
          delete: async (id: string) => { await request(`${path}/${name(id)}`, { method: "DELETE" }); },
        };
      },
    },
    realtime: {
      connect<T = unknown>(channel: string, options: { onMessage: (message: T) => void; onClose?: (event: CloseEvent) => void; onError?: (event: Event) => void }) {
        const url = new URL(`${baseURL}${root}/realtime/${name(channel)}`, globalThis.location?.href);
        url.protocol = url.protocol === "https:" ? "wss:" : "ws:";
        const socket = new WebSocket(url);
        const ready = new Promise<void>((resolve, reject) => {
          socket.addEventListener("open", () => resolve(), { once: true });
          socket.addEventListener("error", () => reject(new Error("WebSocket connection failed")), { once: true });
          socket.addEventListener("close", () => reject(new Error("WebSocket closed before opening")), { once: true });
        });
        socket.addEventListener("message", event => options.onMessage(JSON.parse(String(event.data)) as T));
        if (options.onClose) socket.addEventListener("close", options.onClose);
        if (options.onError) socket.addEventListener("error", options.onError);
        return {
          ready,
          async send(message: T) { await ready; if (socket.readyState !== WebSocket.OPEN) throw new Error("WebSocket is not open"); socket.send(JSON.stringify(message)); },
          close: () => socket.close(1000, "client closed"),
        };
      },
    },
  };
}

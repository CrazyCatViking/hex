import { createAI } from "./ai.js";
import type { SiteActionDefinition, SiteInvocation } from "./site-api.js";
export * from "./site-api.js";
import { HexError, jsonBody, responseError } from "./http.js";
import { createIntegrations } from "./integrations.js";

export * from "./ai.js";
export * from "./http.js";
export * from "./integrations.js";

export interface Capabilities {
  siteAPI?: boolean;
  analytics?: boolean;
  /** Third-party integrations are configured. */
  integrations?: boolean;
  /** Integrations can call with the viewer's own connected accounts. */
  connections?: boolean;
  ai?: boolean;
  automations?: boolean;
  version: number;
  files: boolean;
  database: boolean;
  realtime: boolean;
  sites: boolean;
  identity?: boolean;
  accessControl?: boolean;
  permissionVersion?: number;
  publishing?: boolean;
  artifacts?: boolean;
  actions?: boolean;
  maxUploadBytes: number;
}

export interface Identity {
  provider?: string;
  id: string;
  name?: string;
  email?: string;
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
  /** Identity ID of the caller that created the document, when known. */
  createdBy?: string;
}

/** "all" covers everything, "own" only the caller's own documents. */
export type Grant = "all" | "own" | "conditional" | "none";

export interface PermissionSubjects {
  signedIn?: boolean;
  owners?: boolean;
  principals?: string[];
  roles?: string[];
}

export type RecordCondition =
  | { all: RecordCondition[] }
  | { any: RecordCondition[] }
  | { field: string; op: "eq" | "ne" | "in" | "contains"; value: unknown };

export interface PermissionPolicy {
  version: 2;
  owners?: string[];
  admission?: Omit<PermissionSubjects, "roles">;
  roles?: Record<
    string,
    { name?: string; description?: string; signedIn?: boolean }
  >;
  bindings?: Record<
    string,
    {
      principal: string;
      role: string;
      scope?: { kind: string; id: string };
    }
  >;
  grants?: Record<
    string,
    {
      kind:
        | "collection"
        | "file"
        | "channel"
        | "path"
        | "action"
        | "integration"
        | "site-action"
        | "action-consumer"
        | "event";
      resource: string;
      except?: string[];
      operations: string[];
      subjects: PermissionSubjects;
      scope?: { kind: string; field: string };
      where?: RecordCondition;
      check?: RecordCondition;
      fields?: string[] | null;
    }
  >;
}

export interface DataPermission {
  read: Grant;
  write: Grant;
  create?: Grant;
  update?: Grant;
  delete?: Grant;
  upload?: Grant;
  send?: Grant;
}

/**
 * What the current viewer may do on the site. It is advisory, for showing or
 * hiding links and controls; the platform checks every request itself.
 * Rule maps include "*", the rule for names without their own entry.
 */
export interface Permissions {
  version: number;
  roles: string[];
  role: "owner" | "editor" | "viewer" | "none";
  admin: boolean;
  publish: boolean;
  paths: { prefix: string; allowed: boolean }[];
  collections: Record<string, DataPermission>;
  files: Record<string, DataPermission>;
  channels: Record<string, DataPermission>;
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

  async function requirePermissionVersion(): Promise<void> {
    const upgradeMessage =
      "hex.can() requires version 2 permission support. Ask the platform operator to upgrade the Hex server/workspace runtime. Updating the CLI or browser SDK alone will not fix this. No permission check was sent.";
    let capabilities: Capabilities;
    try {
      capabilities = await requestJSON<Capabilities>("/api/hex/capabilities");
    } catch (error) {
      if (error instanceof HexError && error.status === 404) {
        throw new HexError(501, upgradeMessage, { cause: error });
      }
      throw error;
    }
    if (
      typeof capabilities.permissionVersion !== "number" ||
      capabilities.permissionVersion < 2
    ) {
      throw new HexError(501, upgradeMessage);
    }
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
    sites: {
      identity() {
        return requestJSON<Identity>(`${root}/site-api/identity`);
      },
      api(target: string, options: { version: number }) {
        validateName(target);
        return {
          list() {
            return requestJSON<SiteActionDefinition[]>(
              `${root}/site-api/actions/${target}`,
            );
          },
          call<T = unknown>(
            action: string,
            input: unknown = {},
            callOptions: { idempotencyKey?: string } = {},
          ) {
            return requestJSON<T>(
              `${root}/site-api/call`,
              jsonBody("POST", {
                target,
                action: validateName(action),
                version: options.version,
                input,
                ...callOptions,
              }),
            );
          },
        };
      },
      invocation(id: string) {
        return requestJSON<SiteInvocation>(
          `${root}/site-api/invocations/${validateName(id)}`,
        );
      },
    },
    capabilities() {
      return requestJSON<Capabilities>("/api/hex/capabilities");
    },
    permissions() {
      return requestJSON<Permissions>(
        `/api/hex/sites/${validateName(options.site)}/permissions`,
      );
    },
    /** Requires permissionVersion >= 2. Advisory only: mutations always recheck. */
    async can(input: {
      collection: string;
      operation: "read" | "create" | "update" | "delete";
      id?: string;
      data?: Record<string, unknown>;
    }) {
      await requirePermissionVersion();
      return requestJSON<{ allowed: boolean; version: number }>(
        `${root}/permissions/check`,
        jsonBody("POST", input),
      );
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
    integrations: createIntegrations(request, root),
    ai: createAI(request, root),
    realtime: {
      connect<T = unknown>(channel: string, options: RealtimeOptions<T>) {
        const path = `${root}/realtime/${validateName(channel)}`;
        const url = new URL(baseURL + path, globalThis.location?.href);
        return connectChannel(url, options);
      },
    },
  };
}

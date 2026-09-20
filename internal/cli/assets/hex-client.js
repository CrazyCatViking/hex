export class HexError extends Error {
    status;
    constructor(status, message, options) {
        super(message, options);
        this.status = status;
        this.name = "HexError";
    }
}
function validateName(value) {
    if (!/^[a-zA-Z0-9][a-zA-Z0-9_-]{0,63}$/.test(value)) {
        throw new Error(`Invalid Hex identifier: ${value}`);
    }
    return value;
}
function encodeFilePath(value) {
    const parts = value.split("/");
    const invalid = parts.some((part) => !part || part.startsWith(".") || /[\\\0]/.test(part));
    if (invalid) {
        throw new Error("Invalid file key");
    }
    return parts.map(encodeURIComponent).join("/");
}
function jsonBody(method, data) {
    return {
        method,
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify(data),
    };
}
async function responseError(response) {
    try {
        const body = (await response.json());
        return new HexError(response.status, body.error ?? response.statusText);
    }
    catch (cause) {
        return new HexError(response.status, response.statusText, { cause });
    }
}
function connectChannel(url, options) {
    url.protocol = url.protocol === "https:" ? "wss:" : "ws:";
    const socket = new WebSocket(url);
    const ready = new Promise((resolve, reject) => {
        socket.addEventListener("open", () => resolve(), { once: true });
        socket.addEventListener("error", () => reject(new Error("WebSocket connection failed")), { once: true });
        socket.addEventListener("close", () => reject(new Error("WebSocket closed before opening")), { once: true });
    });
    socket.addEventListener("message", (event) => {
        const message = JSON.parse(String(event.data));
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
        async send(message) {
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
export function createHexClient(options) {
    const baseURL = (options.baseURL ?? "").replace(/\/$/, "");
    const transport = options.fetch ?? globalThis.fetch.bind(globalThis);
    const root = `/api/sites/${validateName(options.site)}`;
    async function request(path, init = {}) {
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
    async function requestJSON(path, init) {
        const response = await request(path, init);
        return response.json();
    }
    function collection(name) {
        const path = `${root}/db/${validateName(name)}`;
        return {
            list(options = {}) {
                const query = new URLSearchParams();
                if (options.after !== undefined) {
                    query.set("after", options.after);
                }
                if (options.limit !== undefined) {
                    query.set("limit", String(options.limit));
                }
                return requestJSON(`${path}?${query}`);
            },
            get(id) {
                return requestJSON(`${path}/${validateName(id)}`);
            },
            create(data) {
                return requestJSON(path, jsonBody("POST", data));
            },
            set(id, data) {
                return requestJSON(`${path}/${validateName(id)}`, jsonBody("PUT", data));
            },
            async delete(id) {
                await request(`${path}/${validateName(id)}`, { method: "DELETE" });
            },
        };
    }
    return {
        capabilities() {
            return requestJSON("/api/hex/capabilities");
        },
        files: {
            list() {
                return requestJSON(`${root}/files`);
            },
            upload(key, data) {
                return requestJSON(`${root}/files/${encodeFilePath(key)}`, {
                    method: "PUT",
                    body: data,
                });
            },
            async download(key) {
                const response = await request(`${root}/files/${encodeFilePath(key)}`);
                return response.blob();
            },
            url(key) {
                return `${baseURL}${root}/files/${encodeFilePath(key)}`;
            },
            async delete(key) {
                await request(`${root}/files/${encodeFilePath(key)}`, {
                    method: "DELETE",
                });
            },
        },
        db: { collection },
        realtime: {
            connect(channel, options) {
                const path = `${root}/realtime/${validateName(channel)}`;
                const url = new URL(baseURL + path, globalThis.location?.href);
                return connectChannel(url, options);
            },
        },
    };
}

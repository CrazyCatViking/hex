(() => {
  const invoke = globalThis.__hexInvoke;
  delete globalThis.__hexInvoke;
  const stringify = JSON.stringify;
  const metadata = globalThis.__hexMetadata;
  delete globalThis.__hexMetadata;

  function request(operation, input) {
    const response = invoke(operation, stringify(input));
    if (response.error) throw new Error(response.error);
    return response.output;
  }

  function freeze(value) {
    if (value && typeof value === "object") {
      for (const child of Object.values(value)) freeze(child);
      Object.freeze(value);
    }
    return value;
  }

  const hex = freeze({
    ...metadata,
    call: async (name, input = {}) => request("call", { name, input }),
    action: async (name, input = {}) => request("action", { name, input }),
    ai: {
      complete: async (requestInput) => request("ai", requestInput),
    },
    db: {
      query: async (collection, options = {}) =>
        request("query", { ...options, collection }),
      save: async (collection, data, options = {}) =>
        request("save", { ...options, collection, data }),
    },
    log: (message, data = null) => request("log", { message, data }),
  });

  Object.defineProperty(globalThis, "hex", { value: hex });
  Object.defineProperty(globalThis, "console", {
    value: Object.freeze({
      log: (...args) => hex.log("console.log", args),
      info: (...args) => hex.log("console.info", args),
      warn: (...args) => hex.log("console.warn", args),
      error: (...args) => hex.log("console.error", args),
    }),
  });
})();

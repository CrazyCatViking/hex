export interface ServerSentEvent {
  event: string;
  data: string;
}

/**
 * Reads server-sent events from a response body. Handles LF, CR and CRLF
 * line endings, multi-line data fields, comments and events split across
 * chunks. Events without data are skipped, as the specification requires.
 */
export async function* readServerSentEvents(
  body: ReadableStream<Uint8Array>,
): AsyncGenerator<ServerSentEvent> {
  const reader = body.getReader();
  const decoder = new TextDecoder();
  let buffer = "";
  let event = "";
  let data: string[] = [];

  function* dispatchLines(final: boolean): Generator<ServerSentEvent> {
    for (;;) {
      const end = buffer.search(/\r\n|\r|\n/);
      if (end < 0) {
        break;
      }
      // A lone CR at the end of a chunk may be the first half of CRLF.
      if (!final && buffer[end] === "\r" && end === buffer.length - 1) {
        break;
      }
      const line = buffer.slice(0, end);
      const separatorLength = buffer.startsWith("\r\n", end) ? 2 : 1;
      buffer = buffer.slice(end + separatorLength);

      if (line === "") {
        if (data.length > 0) {
          yield { event: event || "message", data: data.join("\n") };
        }
        event = "";
        data = [];
        continue;
      }
      if (line.startsWith(":")) {
        continue;
      }
      const colon = line.indexOf(":");
      const field = colon < 0 ? line : line.slice(0, colon);
      let value = colon < 0 ? "" : line.slice(colon + 1);
      if (value.startsWith(" ")) {
        value = value.slice(1);
      }
      if (field === "event") {
        event = value;
      } else if (field === "data") {
        data.push(value);
      }
    }
  }

  let finished = false;
  try {
    for (;;) {
      const { done, value } = await reader.read();
      if (done) {
        finished = true;
        buffer += decoder.decode();
        yield* dispatchLines(true);
        return;
      }
      buffer += decoder.decode(value, { stream: true });
      yield* dispatchLines(false);
    }
  } finally {
    // Stop the download when the consumer leaves early.
    if (!finished) {
      await reader.cancel().catch(() => undefined);
    }
    reader.releaseLock();
  }
}

import { spawn } from "node:child_process";
import { once } from "node:events";
import { setTimeout as delay } from "node:timers/promises";
import { createServer } from "node:net";

export async function checkPortsAvailable(ports) {
  const listeners = [];
  try {
    for (const port of ports) {
      const listener = createServer();
      try {
        listener.listen(port, "127.0.0.1");
        await once(listener, "listening");
        listeners.push(listener);
      } catch (cause) {
        throw new Error(`Development port ${port} is unavailable`, { cause });
      }
    }
  } finally {
    await Promise.all(
      listeners.map(
        (listener) =>
          new Promise((resolve, reject) => {
            listener.close((error) => {
              if (error) {
                reject(error);
                return;
              }
              resolve();
            });
          }),
      ),
    );
  }
}

export async function startProcess(command, args, options) {
  const child = spawn(command, args, options);
  await once(child, "spawn");
  return child;
}

export async function waitForHTTP(url, child, signal) {
  const deadline = Date.now() + 30_000;
  let lastError;

  while (Date.now() < deadline) {
    signal?.throwIfAborted();
    if (child.exitCode !== null || child.signalCode !== null) {
      throw new Error(`Process exited while waiting for ${url}`, {
        cause: lastError,
      });
    }

    try {
      const requestSignal = signal
        ? AbortSignal.any([signal, AbortSignal.timeout(1000)])
        : AbortSignal.timeout(1000);
      const response = await fetch(url, { signal: requestSignal });
      await response.arrayBuffer();
      if (response.ok) {
        return;
      }
      lastError = new Error(`Health check returned HTTP ${response.status}`);
    } catch (error) {
      lastError = error;
    }
    await delay(100, undefined, { signal });
  }

  throw new Error(`Timed out waiting for ${url}`, { cause: lastError });
}

export async function stopProcess(child, timeoutMilliseconds = 5000) {
  if (!child?.pid || child.exitCode !== null || child.signalCode !== null) {
    return;
  }

  const exited = once(child, "exit");
  const timeout = setTimeout(() => child.kill("SIGKILL"), timeoutMilliseconds);
  timeout.unref();
  try {
    child.kill("SIGTERM");
    await exited;
  } finally {
    clearTimeout(timeout);
  }
}

export async function waitForStop(children, signal) {
  const listeners = [];
  try {
    await new Promise((resolve, reject) => {
      if (signal.aborted) {
        resolve();
        return;
      }
      const stop = () => resolve();
      signal.addEventListener("abort", stop, { once: true });
      listeners.push(() => signal.removeEventListener("abort", stop));

      for (const [name, child] of Object.entries(children)) {
        const exited = (code, reason) =>
          reject(new Error(`${name} exited unexpectedly (${reason ?? code})`));
        const failed = (error) =>
          reject(
            new Error(`${name} failed: ${error.message}`, { cause: error }),
          );
        child.once("exit", exited);
        child.once("error", failed);
        listeners.push(() => {
          child.removeListener("exit", exited);
          child.removeListener("error", failed);
        });
        if (child.exitCode !== null || child.signalCode !== null) {
          exited(child.exitCode, child.signalCode);
        }
      }
    });
  } finally {
    for (const remove of listeners) {
      remove();
    }
  }
}

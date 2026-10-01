import { spawn } from "node:child_process";
import { once } from "node:events";
import { setTimeout as delay } from "node:timers/promises";

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

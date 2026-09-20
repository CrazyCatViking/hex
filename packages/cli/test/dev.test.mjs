import assert from "node:assert/strict";
import { test } from "node:test";
import { mkdtemp, writeFile, rm } from "node:fs/promises";
import { join } from "node:path";
import { tmpdir } from "node:os";
import { createServer } from "node:net";
import { once } from "node:events";
import { developmentSettings } from "../src/dev/settings.mjs";
import { composeArguments, serviceEnvironment } from "../src/dev/services.mjs";
import {
  startProcess,
  stopProcess,
  waitForHTTP,
  checkPortsAvailable,
} from "../src/dev/processes.mjs";

test("occupied ports are rejected instead of probing another running platform", async (t) => {
  const listener = createServer().listen(0, "127.0.0.1");
  await once(listener, "listening");
  t.after(() => new Promise((resolve) => listener.close(resolve)));
  await assert.rejects(
    checkPortsAvailable([listener.address().port]),
    /unavailable/,
  );
});

test("external server configuration resolves paths and lets flags override defaults", async (t) => {
  const directory = await mkdtemp(join(tmpdir(), "hex-dev-settings-"));
  t.after(() => rm(directory, { recursive: true, force: true }));
  await writeFile(
    join(directory, "hex.dev.json"),
    JSON.stringify({
      package: "./cmd/platform",
      services: ["postgres", "azurite"],
      envFile: ".env.local",
      port: 8088,
    }),
  );
  await writeFile(
    join(directory, ".env.local"),
    'CUSTOM_VALUE="hello world"\nHEX_REALTIME_PROVIDER=none\n',
  );

  const settings = await developmentSettings(directory, {
    services: "none",
    port: "8089",
  });
  assert.equal(settings.package, "./cmd/platform");
  assert.equal(settings.serverDirectory, directory);
  assert.equal(settings.dataDirectory, join(directory, ".hex-dev"));
  assert.equal(settings.port, 8089);
  assert.deepEqual(settings.services, []);
  assert.equal(settings.environment.CUSTOM_VALUE, "hello world");
  assert.equal(settings.environment.HEX_REALTIME_PROVIDER, "none");
  assert.deepEqual(serviceEnvironment(settings), {});
});

test("invalid development settings fail before launching processes", async (t) => {
  const directory = await mkdtemp(join(tmpdir(), "hex-dev-invalid-"));
  t.after(() => rm(directory, { recursive: true, force: true }));
  await assert.rejects(developmentSettings(directory, { port: "0" }), /port/);
  await assert.rejects(
    developmentSettings(directory, { port: "8081" }),
    /distinct/,
  );
  await assert.rejects(
    developmentSettings(directory, { services: "redis" }),
    /Supported services/,
  );
  await assert.rejects(
    developmentSettings(directory, { "env-file": "missing" }),
    /environment/,
  );
  await writeFile(join(directory, "hex.dev.json"), "invalid JSON");
  await assert.rejects(developmentSettings(directory, {}), /configuration/);
});

test("Compose selects only requested services and retains data on shutdown", async (t) => {
  const directory = await mkdtemp(join(tmpdir(), "hex-dev-services-"));
  t.after(() => rm(directory, { recursive: true, force: true }));
  const settings = await developmentSettings(directory, {
    services: "postgres",
    "postgres-port": "55432",
  });
  const environment = serviceEnvironment(settings);
  assert.equal(environment.HEX_DATABASE_PROVIDER, "postgres");
  assert.match(environment.DATABASE_URL, /127\.0\.0\.1:55432/);
  assert.equal(environment.AZURE_BLOB_CONNECTION_STRING, undefined);
  const up = composeArguments(settings, "up");
  assert.deepEqual(up.slice(-6), [
    "up",
    "--detach",
    "--wait",
    "--wait-timeout",
    "120",
    "postgres",
  ]);
  const down = composeArguments(settings, "down");
  assert.equal(down.at(-1), "down");
  assert.ok(!down.includes("--volumes"));

  const other = await developmentSettings(directory, {
    "data-dir": "other",
    services: "azurite",
    "blob-port": "11000",
  });
  assert.notEqual(
    up[up.indexOf("--project-name") + 1],
    composeArguments(other, "up")[up.indexOf("--project-name") + 1],
  );
  assert.match(
    serviceEnvironment(other).AZURE_BLOB_CONNECTION_STRING,
    /127\.0\.0\.1:11000\/hexlocal/,
  );
});

test("process failures are reported and readiness can be cancelled", async () => {
  await assert.rejects(
    startProcess("hex-nonexistent-executable", [], {}),
    /ENOENT/,
  );
  const child = await startProcess(
    process.execPath,
    ["-e", "setInterval(() => {}, 1000)"],
    { stdio: "ignore" },
  );
  try {
    const controller = new AbortController();
    controller.abort();
    await assert.rejects(
      waitForHTTP("http://127.0.0.1:1", child, controller.signal),
      /abort/i,
    );
  } finally {
    await stopProcess(child);
  }
  assert.notEqual(child.signalCode, null);
});

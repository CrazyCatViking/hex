import { execFile } from "node:child_process";
import { promisify, parseArgs } from "node:util";
import { mkdir, mkdtemp, rm } from "node:fs/promises";
import { join } from "node:path";
import { tmpdir } from "node:os";
import { developmentSettings } from "./settings.mjs";
import { manageServices, serviceEnvironment } from "./services.mjs";
import { startNginx } from "./nginx.mjs";
import {
  startProcess,
  stopProcess,
  waitForHTTP,
  waitForStop,
  checkPortsAvailable,
} from "./processes.mjs";

const exec = promisify(execFile);

function serverEnvironment(settings) {
  return {
    ...process.env,
    DATABASE_URL: "",
    AZURE_BLOB_ENDPOINT: "",
    AZURE_BLOB_CONNECTION_STRING: "",
    HEX_SITES_PROVIDER: "filesystem",
    HEX_FILES_PROVIDER: "memory",
    HEX_DATABASE_PROVIDER: "memory",
    HEX_REALTIME_PROVIDER: "memory",
    ...settings.environment,
    ...serviceEnvironment(settings),
    HEX_DEV: "1",
    HEX_ADDR: `127.0.0.1:${settings.apiPort}`,
    HEX_DEV_DATA_DIR: settings.dataDirectory,
    HEX_SITES_DIR: join(settings.dataDirectory, "sites"),
    HEX_FILES_DIR: join(settings.dataDirectory, "files"),
    HEX_SITE_BASE_URL: `http://localhost:${settings.port}`,
  };
}

export async function runDevelopment(settings) {
  const temporaryDirectory = await mkdtemp(join(tmpdir(), "hex-dev-"));
  const controller = new AbortController();
  const stop = () => controller.abort();
  process.once("SIGINT", stop);
  process.once("SIGTERM", stop);
  let server;
  let nginx;

  try {
    await checkPortsAvailable([settings.port, settings.apiPort]);
    await mkdir(join(settings.dataDirectory, "sites/public/sites"), {
      recursive: true,
    });
    const environment = serverEnvironment(settings);
    let binary = settings.binary;
    if (!binary) {
      binary = join(temporaryDirectory, "hex-server");
      console.log(
        `Building ${settings.package} in ${settings.serverDirectory}`,
      );
      await exec("go", ["build", "-o", binary, settings.package], {
        cwd: settings.serverDirectory,
        env: environment,
        signal: controller.signal,
        maxBuffer: 16 * 1024 * 1024,
      });
    }

    if (settings.services.length > 0) {
      console.log(`Starting local services: ${settings.services.join(", ")}`);
      await manageServices(settings, "up", controller.signal);
    }
    controller.signal.throwIfAborted();

    server = await startProcess(binary, settings.args, {
      cwd: settings.serverDirectory,
      env: environment,
      stdio: "inherit",
    });
    await waitForHTTP(
      `http://127.0.0.1:${settings.apiPort}/api/hex/capabilities`,
      server,
      controller.signal,
    );

    nginx = await startNginx({
      directory: join(temporaryDirectory, "nginx"),
      sitesDirectory: join(settings.dataDirectory, "sites"),
      port: settings.port,
      backendPort: settings.apiPort,
    });
    await waitForHTTP(
      `http://127.0.0.1:${settings.port}/healthz`,
      nginx,
      controller.signal,
    );

    console.log(
      `Hex development environment ready: http://localhost:${settings.port}`,
    );
    console.log(`Sites: http://<name>.localhost:${settings.port}/`);
    console.log(
      `Publishing root: ${join(settings.dataDirectory, "sites/public/sites")}`,
    );
    console.log("Local gateway and API are loopback-only and unauthenticated.");
    if (settings.services.length > 0) {
      console.log(
        "Service containers persist after exit. Use hex dev --stop-services with the same configuration to stop them.",
      );
    }

    await waitForStop({ server, nginx }, controller.signal);
  } catch (error) {
    if (!controller.signal.aborted) {
      throw error;
    }
  } finally {
    process.removeListener("SIGINT", stop);
    process.removeListener("SIGTERM", stop);
    const results = await Promise.allSettled([
      stopProcess(nginx),
      stopProcess(server),
    ]);
    await rm(temporaryDirectory, { recursive: true, force: true });
    const failures = results.filter((result) => result.status === "rejected");
    if (failures.length > 0) {
      throw new AggregateError(
        failures.map((result) => result.reason),
        "Failed to stop development processes",
      );
    }
  }
}

export async function devCommand(args) {
  const { positionals, values } = parseArgs({
    args,
    allowPositionals: true,
    options: {
      config: { type: "string" },
      package: { type: "string" },
      binary: { type: "string" },
      arg: { type: "string", multiple: true },
      "env-file": { type: "string" },
      "data-dir": { type: "string" },
      port: { type: "string" },
      "api-port": { type: "string" },
      "postgres-port": { type: "string" },
      "blob-port": { type: "string" },
      services: { type: "string" },
      "stop-services": { type: "boolean" },
      help: { type: "boolean" },
    },
  });
  if (values.help) {
    console.log(`hex dev [server-directory] [--package ./cmd/server | --binary ./server]
  [--config hex.dev.json] [--env-file .env.local] [--data-dir .hex-dev]
  [--port 8080] [--api-port 8081] [--arg value]
  [--services postgres,azurite | --services none]
  [--postgres-port 54320] [--blob-port 10000] [--stop-services]

Builds and runs YOUR Go server. It must opt into local providers (server/dev)
or consume the same environment variables. Requires native NGINX.
Optional services require Docker Compose v2. No Azure account or .NET required.`);
    return;
  }
  if (positionals.length > 1) {
    throw new Error(
      "Provide one server directory; use --arg for executable arguments",
    );
  }
  const settings = await developmentSettings(positionals[0], values);
  if (values["stop-services"]) {
    await manageServices(settings, "down");
    console.log(
      "Local service containers stopped; persistent volumes retained.",
    );
    return;
  }
  await runDevelopment(settings);
}

import { readFile, realpath } from "node:fs/promises";
import { resolve } from "node:path";
import { parseEnv } from "node:util";

async function readConfiguration(directory, filename) {
  const path = resolve(directory, filename ?? "hex.dev.json");
  try {
    const config = JSON.parse(await readFile(path, "utf8"));
    if (!config || typeof config !== "object" || Array.isArray(config)) {
      throw new Error("Expected an object");
    }
    return config;
  } catch (cause) {
    if (!filename && cause.code === "ENOENT") {
      return {};
    }
    throw new Error(
      `Read development configuration ${path}: ${cause.message}`,
      { cause },
    );
  }
}

function port(value, fallback, name) {
  const selected = value === undefined ? fallback : Number(value);
  if (!Number.isInteger(selected) || selected < 1024 || selected > 65535) {
    throw new Error(`${name} must be an integer between 1024 and 65535`);
  }
  return selected;
}

function services(value) {
  const selected = typeof value === "string" ? value.split(",") : value;
  if (!Array.isArray(selected)) {
    throw new Error("services must be a list of postgres and/or azurite");
  }
  if (selected.length === 1 && selected[0] === "none") {
    return [];
  }
  if (selected.some((name) => !["postgres", "azurite"].includes(name))) {
    throw new Error(
      "Supported services are postgres and azurite; use none for lightweight mode",
    );
  }
  return [...new Set(selected)];
}

export async function developmentSettings(directory, flags) {
  const serverDirectory = await realpath(resolve(directory ?? "."));
  const config = await readConfiguration(serverDirectory, flags.config);
  const settings = {
    serverDirectory,
    package: flags.package ?? config.package ?? ".",
    binary: flags.binary ?? config.binary,
    args: flags.arg ?? config.args ?? [],
    dataDirectory: resolve(
      serverDirectory,
      flags["data-dir"] ?? config.dataDirectory ?? ".hex-dev",
    ),
    envFile: flags["env-file"] ?? config.envFile,
    services: services(flags.services ?? config.services ?? []),
    port: port(flags.port ?? config.port, 8080, "port"),
    apiPort: port(flags["api-port"] ?? config.apiPort, 8081, "apiPort"),
    postgresPort: port(
      flags["postgres-port"] ?? config.postgresPort,
      54320,
      "postgresPort",
    ),
    blobPort: port(flags["blob-port"] ?? config.blobPort, 10000, "blobPort"),
  };
  if (settings.binary && (flags.package || config.package)) {
    throw new Error("Choose a Go package or a prebuilt binary, not both");
  }
  if (
    typeof settings.package !== "string" ||
    settings.package.startsWith("-")
  ) {
    throw new Error(
      "package must be a Go package path, such as . or ./cmd/server",
    );
  }
  if (
    !Array.isArray(settings.args) ||
    settings.args.some((argument) => typeof argument !== "string")
  ) {
    throw new Error("args must be a list of strings");
  }
  if (settings.binary) {
    settings.binary = await realpath(resolve(serverDirectory, settings.binary));
  }

  const ports = [settings.port, settings.apiPort];
  if (settings.services.includes("postgres")) {
    ports.push(settings.postgresPort);
  }
  if (settings.services.includes("azurite")) {
    ports.push(settings.blobPort);
  }
  if (new Set(ports).size !== ports.length) {
    throw new Error("Gateway, API and enabled service ports must be distinct");
  }

  let environment = {};
  if (settings.envFile && !flags["stop-services"]) {
    const path = resolve(serverDirectory, settings.envFile);
    try {
      environment = parseEnv(await readFile(path, "utf8"));
    } catch (cause) {
      throw new Error(
        `Read development environment ${path}: ${cause.message}`,
        { cause },
      );
    }
  }
  settings.environment = environment;
  return settings;
}

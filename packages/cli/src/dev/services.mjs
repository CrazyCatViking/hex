import { createHash } from "node:crypto";
import { execFile } from "node:child_process";
import { fileURLToPath } from "node:url";
import { promisify } from "node:util";

const exec = promisify(execFile);
const composeFile = fileURLToPath(
  new URL("../../assets/compose.yaml", import.meta.url),
);
const blobKey = Buffer.from("hex-local-development-only").toString("base64");

export function serviceEnvironment(settings) {
  const environment = {};
  if (settings.services.includes("postgres")) {
    environment.HEX_DATABASE_PROVIDER = "postgres";
    environment.DATABASE_URL = `postgres://hex:hex-local-only@127.0.0.1:${settings.postgresPort}/hex?sslmode=disable`;
  }
  if (settings.services.includes("azurite")) {
    environment.HEX_FILES_PROVIDER = "azureblob";
    environment.AZURE_BLOB_CONNECTION_STRING = [
      "DefaultEndpointsProtocol=http",
      "AccountName=hexlocal",
      `AccountKey=${blobKey}`,
      `BlobEndpoint=http://127.0.0.1:${settings.blobPort}/hexlocal`,
    ].join(";");
    environment.AZURE_BLOB_CONTAINER = "uploads";
  }
  return environment;
}

export function composeArguments(settings, action) {
  const suffix = createHash("sha256")
    .update(settings.dataDirectory)
    .digest("hex")
    .slice(0, 12);
  const args = [
    "compose",
    "--file",
    composeFile,
    "--project-name",
    `hexdev-${suffix}`,
  ];
  for (const service of ["postgres", "azurite"]) {
    args.push("--profile", service);
  }
  if (action === "up") {
    args.push(
      "up",
      "--detach",
      "--wait",
      "--wait-timeout",
      "120",
      ...settings.services,
    );
  } else {
    args.push("down");
  }
  return args;
}

export async function manageServices(settings, action, signal) {
  const environment = {
    ...process.env,
    HEX_LOCAL_POSTGRES_PORT: String(settings.postgresPort),
    HEX_LOCAL_BLOB_PORT: String(settings.blobPort),
    HEX_LOCAL_BLOB_KEY: blobKey,
  };
  try {
    await exec("docker", composeArguments(settings, action), {
      env: environment,
      signal,
      maxBuffer: 16 * 1024 * 1024,
    });
  } catch (cause) {
    if (signal?.aborted) {
      throw cause;
    }
    throw new Error(
      `Docker Compose ${action} failed. Service-backed development requires a running Docker engine and Compose v2. ${cause.stderr ?? cause.message}`,
      { cause },
    );
  }
}

import { execFile } from "node:child_process";
import { promisify } from "node:util";
import { fileURLToPath } from "node:url";
import { developmentSettings } from "../packages/cli/src/dev/settings.mjs";
import {
  manageServices,
  serviceEnvironment,
} from "../packages/cli/src/dev/services.mjs";

const exec = promisify(execFile);
const root = fileURLToPath(new URL("../", import.meta.url));
const existing = process.argv.includes("--existing");
const settings = await developmentSettings(root, {
  services: "postgres,azurite",
  "data-dir": ".hex-dev/service-tests",
});

let environment = { ...process.env };
if (existing) {
  if (
    !environment.HEX_TEST_POSTGRES_URL ||
    !environment.HEX_TEST_BLOB_CONNECTION_STRING
  ) {
    throw new Error(
      "--existing requires HEX_TEST_POSTGRES_URL and HEX_TEST_BLOB_CONNECTION_STRING",
    );
  }
} else {
  const services = serviceEnvironment(settings);
  environment.HEX_TEST_POSTGRES_URL = services.DATABASE_URL;
  environment.HEX_TEST_BLOB_CONNECTION_STRING =
    services.AZURE_BLOB_CONNECTION_STRING;
}

try {
  if (!existing) {
    await manageServices(settings, "up");
  }
  const providers = await exec(
    "go",
    [
      "test",
      "-race",
      "-count=1",
      "-v",
      "./server/providers/postgres",
      "./server/providers/azureblob",
    ],
    {
      cwd: root,
      env: environment,
      maxBuffer: 16 * 1024 * 1024,
    },
  );
  process.stdout.write(providers.stdout);
  process.stderr.write(providers.stderr);
  const consumer = await exec(
    process.execPath,
    ["scripts/test-external-server.mjs"],
    {
      cwd: root,
      env: environment,
      maxBuffer: 16 * 1024 * 1024,
    },
  );
  process.stdout.write(consumer.stdout);
  process.stderr.write(consumer.stderr);
} finally {
  if (!existing) {
    await manageServices(settings, "down");
  }
}

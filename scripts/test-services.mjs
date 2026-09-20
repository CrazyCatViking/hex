import { execFile } from "node:child_process";
import { promisify } from "node:util";
import { fileURLToPath } from "node:url";

const exec = promisify(execFile);
const root = fileURLToPath(new URL("../", import.meta.url));
const compose = fileURLToPath(
  new URL("../internal/cli/assets/compose.yaml", import.meta.url),
);
const existing = process.argv.includes("--existing");
const blobKey = Buffer.from("hex-local-development-only").toString("base64");
const environment = { ...process.env };

async function composeCommand(action) {
  const args = [
    "compose",
    "--file",
    compose,
    "--project-name",
    "hex-provider-tests",
    "--profile",
    "postgres",
    "--profile",
    "azurite",
    ...action,
  ];
  const result = await exec("docker", args, {
    cwd: root,
    env: {
      ...process.env,
      HEX_LOCAL_POSTGRES_PORT: "54320",
      HEX_LOCAL_BLOB_PORT: "10000",
      HEX_LOCAL_BLOB_KEY: blobKey,
    },
    maxBuffer: 16 * 1024 * 1024,
  });
  process.stdout.write(result.stdout);
  process.stderr.write(result.stderr);
}

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
  environment.HEX_TEST_POSTGRES_URL =
    "postgres://hex:hex-local-only@127.0.0.1:54320/hex?sslmode=disable";
  environment.HEX_TEST_BLOB_CONNECTION_STRING = `DefaultEndpointsProtocol=http;AccountName=hexlocal;AccountKey=${blobKey};BlobEndpoint=http://127.0.0.1:10000/hexlocal`;
}

try {
  if (!existing)
    await composeCommand(["up", "--detach", "--wait", "postgres", "azurite"]);
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
  if (!existing) await composeCommand(["down"]);
}

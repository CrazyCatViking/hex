import assert from "node:assert/strict";
import { execFile } from "node:child_process";
import { promisify } from "node:util";
import { copyFile, mkdir, mkdtemp, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { fileURLToPath } from "node:url";
import { createServer } from "node:net";
import { once } from "node:events";
import { Agent } from "undici";
import { createHexClient } from "../packages/client/dist/index.js";
import {
  startProcess,
  stopProcess,
  waitForHTTP,
} from "../packages/cli/src/dev/processes.mjs";

const exec = promisify(execFile);
const root = fileURLToPath(new URL("../", import.meta.url));
const cli = join(root, "packages/cli/src/cli.mjs");

async function freePort() {
  const server = createServer().listen(0, "127.0.0.1");
  await once(server, "listening");
  const port = server.address().port;
  await new Promise((resolve, reject) => {
    server.close((error) => {
      if (error) {
        reject(error);
        return;
      }
      resolve();
    });
  });
  return port;
}

function loopbackLookup(hostname, options, callback) {
  if (options.all) {
    callback(null, [{ address: "127.0.0.1", family: 4 }]);
  } else {
    callback(null, "127.0.0.1", 4);
  }
}

async function main() {
  const directory = await mkdtemp(join(tmpdir(), "hex-consumer-"));
  const platform = join(directory, "platform");
  const app = join(directory, "app");
  const dataDirectory = join(platform, "data");
  const dispatcher = new Agent({ connect: { lookup: loopbackLookup } });
  let development;
  let logs = "";

  try {
    const port = await freePort();
    let apiPort = await freePort();
    while (port === apiPort) {
      apiPort = await freePort();
    }
    const origin = `http://127.0.0.1:${port}`;
    const siteBaseURL = `http://localhost:${port}`;
    const siteURL = `http://demo.localhost:${port}/`;
    await mkdir(platform);
    await copyFile(
      join(root, "examples/custom-server/main.go"),
      join(platform, "main.go"),
    );
    await writeFile(
      join(platform, "go.mod"),
      `module example.com/my-hex-platform

go 1.25.0

require github.com/hex-platform/hex v0.0.0

replace github.com/hex-platform/hex => ${JSON.stringify(root)}
`,
    );
    await writeFile(
      join(platform, "hex.dev.json"),
      JSON.stringify({
        port,
        apiPort,
        dataDirectory: "data",
        envFile: ".env.local",
      }),
    );

    const localEnvironment = {
      HEX_REALTIME_PROVIDER: "none",
      CONSUMER_SETTING: "from-env-file",
    };
    if (process.env.HEX_TEST_POSTGRES_URL) {
      localEnvironment.HEX_DATABASE_PROVIDER = "postgres";
      localEnvironment.DATABASE_URL = process.env.HEX_TEST_POSTGRES_URL;
    }
    if (process.env.HEX_TEST_BLOB_CONNECTION_STRING) {
      localEnvironment.HEX_FILES_PROVIDER = "azureblob";
      localEnvironment.AZURE_BLOB_CONNECTION_STRING =
        process.env.HEX_TEST_BLOB_CONNECTION_STRING;
      localEnvironment.AZURE_BLOB_CONTAINER = "hex-consumer-test";
    }
    const envContents = Object.entries(localEnvironment)
      .map(([name, value]) => `${name}=${JSON.stringify(value)}`)
      .join("\n");
    await writeFile(join(platform, ".env.local"), envContents);
    await exec("go", ["mod", "tidy"], {
      cwd: platform,
      env: { ...process.env, GOWORK: "off" },
    });

    async function launch(args = []) {
      logs = "";
      const child = await startProcess(
        process.execPath,
        [cli, "dev", platform, ...args],
        {
          cwd: directory,
          env: { ...process.env, GOWORK: "off" },
          stdio: ["ignore", "pipe", "pipe"],
        },
      );
      development = child;
      child.stdout.on("data", (data) => {
        logs += data.toString();
      });
      child.stderr.on("data", (data) => {
        logs += data.toString();
      });
      await waitForHTTP(`${origin}/api/hex/capabilities`, child);
      return child;
    }

    development = await launch();
    const platformInfo = await (await fetch(`${origin}/api/platform`)).json();
    assert.equal(platformInfo.name, "custom-server");
    const client = createHexClient({ site: "consumer-test", baseURL: origin });
    const capabilities = await client.capabilities();
    assert.equal(capabilities.realtime, false);
    assert.equal(capabilities.files, true);
    assert.equal(capabilities.database, true);

    await exec(process.execPath, [
      cli,
      "init",
      app,
      "--name",
      "demo",
      "--server",
      origin,
      "--site-base-url",
      siteBaseURL,
      "--publish-root",
      join(dataDirectory, "sites/public/sites"),
    ]);
    await writeFile(join(app, "public/index.html"), "consumer website");
    await exec(process.execPath, [cli, "publish"], { cwd: app });
    assert.equal(
      await (await fetch(siteURL, { dispatcher })).text(),
      "consumer website",
    );

    await client.files.upload("persist.bin", new Uint8Array([0, 255, 10]));
    const uploadedFile = await client.files.download("persist.bin");
    assert.deepEqual(
      new Uint8Array(await uploadedFile.arrayBuffer()),
      new Uint8Array([0, 255, 10]),
    );
    const notes = client.db.collection("notes");
    const note = await notes.create({ source: "external repository" });
    await stopProcess(development, 15000);
    assert.equal(development.exitCode, 0, logs);
    await assert.rejects(fetch(`${origin}/healthz`));
    await assert.rejects(
      fetch(`http://127.0.0.1:${apiPort}/api/hex/capabilities`),
    );

    const binary = join(platform, "platform-server");
    await exec("go", ["build", "-o", binary, "."], {
      cwd: platform,
      env: { ...process.env, GOWORK: "off" },
    });
    development = await launch(["--binary", binary]);
    assert.equal(
      await (await fetch(siteURL, { dispatcher })).text(),
      "consumer website",
    );
    if (process.env.HEX_TEST_BLOB_CONNECTION_STRING) {
      const file = await client.files.download("persist.bin");
      assert.deepEqual(
        new Uint8Array(await file.arrayBuffer()),
        new Uint8Array([0, 255, 10]),
      );
      await client.files.delete("persist.bin");
    } else {
      await assert.rejects(
        client.files.download("persist.bin"),
        (error) => error.status === 404,
      );
    }
    if (process.env.HEX_TEST_POSTGRES_URL) {
      assert.deepEqual((await notes.get(note.id)).data, {
        source: "external repository",
      });
      await notes.delete(note.id);
    } else {
      await assert.rejects(notes.get(note.id), (error) => error.status === 404);
    }
    console.log(
      "External-server test passed: independent Go module, custom endpoint, env file, binary launch, direct publishing, persistence and process cleanup.",
    );
  } catch (error) {
    console.error(logs);
    throw error;
  } finally {
    await stopProcess(development, 15000);
    await dispatcher.close();
    await rm(directory, { recursive: true, force: true });
  }
}

await main();

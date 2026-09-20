import assert from "node:assert/strict";
import { spawn, execFile } from "node:child_process";
import { promisify } from "node:util";
import { mkdtemp, mkdir, rm, writeFile, symlink } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { fileURLToPath } from "node:url";
import { createServer } from "node:net";
import { once } from "node:events";
import WebSocket from "ws";
import { createHexClient } from "../packages/client/dist/index.js";
import { startNginx, waitForHTTP, stopProcess } from "./nginx.mjs";

globalThis.WebSocket = WebSocket;

const exec = promisify(execFile);
const root = fileURLToPath(new URL("../", import.meta.url));

async function freePort() {
  const listener = createServer().listen(0, "127.0.0.1");
  await once(listener, "listening");

  const port = listener.address().port;
  await new Promise((resolve, reject) => {
    listener.close((error) => {
      if (error) {
        reject(error);
        return;
      }
      resolve();
    });
  });

  return port;
}

async function readText(url) {
  const response = await fetch(url);
  assert.equal(response.status, 200, url);
  return response.text();
}

async function verifyStaticServing(origin, backend) {
  assert.match(await readText(`${origin}/sites/demo/`), /Welcome to Hex/);
  assert.match(
    await readText(`${origin}/sites/demo/hex-client.js`),
    /createHexClient/,
  );
  assert.equal((await fetch(`${backend}/sites/demo/`)).status, 404);

  const redirect = await fetch(`${origin}/sites/demo`, { redirect: "manual" });
  assert.equal(redirect.headers.get("location"), "/sites/demo/");

  const asset = await fetch(`${origin}/sites/demo/hex-client.js`);
  assert.match(asset.headers.get("content-type"), /javascript/);
  assert.equal(asset.headers.get("x-content-type-options"), "nosniff");
  assert.equal((await fetch(`${origin}/sites/demo/missing.js`)).status, 404);
}

async function verifyPrivateFiles(origin, directory, sitesDirectory, release) {
  const privatePaths = [
    "/sites/demo.json",
    `/releases/demo/${release}/index.html`,
    "/public/sites/demo/index.html",
  ];
  for (const path of privatePaths) {
    assert.equal((await fetch(origin + path)).status, 404);
  }

  const hiddenFile = join(sitesDirectory, "public/sites/demo/.hex-test");
  await writeFile(hiddenFile, "temporary file");
  assert.equal((await fetch(`${origin}/sites/demo/.hex-test`)).status, 404);
  await rm(hiddenFile);

  const secret = join(directory, "secret.txt");
  const publicLink = join(sitesDirectory, "public/sites/demo/leak.txt");
  await writeFile(secret, "not published");
  await symlink(secret, publicLink);
  assert.notEqual((await fetch(`${origin}/sites/demo/leak.txt`)).status, 200);
  await rm(publicLink);
}

async function verifyClientStorage(client) {
  const bytes = new Uint8Array([0, 255, 128]);
  await client.files.upload("binary.dat", bytes);

  const downloadedFile = await client.files.download("binary.dat");
  assert.deepEqual(new Uint8Array(await downloadedFile.arrayBuffer()), bytes);
  assert.equal((await client.files.list()).length, 1);

  const notes = client.db.collection("notes");
  const note = await notes.create({ message: "works" });
  assert.deepEqual((await notes.get(note.id)).data, { message: "works" });

  await notes.set(note.id, { message: "updated" });
  assert.equal((await notes.list())[0].data.message, "updated");

  await notes.delete(note.id);
  await client.files.delete("binary.dat");
}

async function verifyRealtime(client) {
  let onMessage;
  const received = new Promise((resolve) => {
    onMessage = resolve;
  });
  let timer;
  const channel = client.realtime.connect("updates", { onMessage });

  try {
    await channel.ready;
    await channel.send({ type: "refresh" });

    const deadline = new Promise((_, reject) => {
      timer = setTimeout(
        () => reject(new Error("Realtime message timed out")),
        5000,
      );
    });
    assert.deepEqual(await Promise.race([received, deadline]), {
      type: "refresh",
    });
  } finally {
    clearTimeout(timer);
    channel.close();
  }
}

async function verifyPublishing(origin, projectDirectory, invoke) {
  await mkdir(join(projectDirectory, "public/nested"));
  await writeFile(
    join(projectDirectory, "public/nested/index.html"),
    "nested page",
  );
  await writeFile(join(projectDirectory, "public/old.js"), "old asset");
  await invoke(["publish"]);
  assert.equal(await readText(`${origin}/sites/demo/nested/`), "nested page");

  await writeFile(join(projectDirectory, "public/index.html"), "updated site");
  await rm(join(projectDirectory, "public/old.js"));
  await invoke(["publish"]);
  assert.equal(await readText(`${origin}/sites/demo/`), "updated site");
  assert.equal((await fetch(`${origin}/sites/demo/old.js`)).status, 404);

  const siteList = await invoke(["sites"]);
  assert.equal(JSON.parse(siteList.stdout).length, 1);

  await invoke(["delete", "--yes"]);
  assert.equal((await fetch(`${origin}/sites/demo/`)).status, 404);
  await invoke(["publish"]);
}

async function main() {
  const directory = await mkdtemp(join(tmpdir(), "hex-e2e-"));
  let server;
  let nginx;

  try {
    const backendPort = await freePort();
    let port = await freePort();
    while (port === backendPort) {
      port = await freePort();
    }

    const origin = `http://127.0.0.1:${port}`;
    const backend = `http://127.0.0.1:${backendPort}`;
    const sitesDirectory = join(directory, "sites");
    const binary = join(directory, "hex-server");
    await exec("go", ["build", "-o", binary, "./cmd/hex-server"], {
      cwd: root,
    });

    server = spawn(binary, [], {
      cwd: directory,
      env: {
        ...process.env,
        HEX_ADDR: `127.0.0.1:${backendPort}`,
        HEX_SITES_DIR: sitesDirectory,
        DATABASE_URL: "",
        AZURE_BLOB_ENDPOINT: "",
      },
      stdio: ["ignore", "ignore", "inherit"],
    });
    await waitForHTTP(`${backend}/api/hex/capabilities`, server);

    nginx = await startNginx({
      directory: join(directory, "nginx"),
      sitesDirectory,
      port,
      backendPort,
    });
    await waitForHTTP(`${origin}/healthz`, nginx);

    const cli = join(root, "packages/cli/src/cli.mjs");
    const projectDirectory = join(directory, "demo");
    await exec(process.execPath, [
      cli,
      "init",
      projectDirectory,
      "--server",
      origin,
    ]);

    const invoke = (args) =>
      exec(process.execPath, [cli, ...args], { cwd: projectDirectory });
    const published = await invoke(["publish"]);
    assert.equal(published.stdout.trim(), `${origin}/sites/demo/`);

    await verifyStaticServing(origin, backend);
    const siteList = await invoke(["sites"]);
    const site = JSON.parse(siteList.stdout)[0];
    await verifyPrivateFiles(origin, directory, sitesDirectory, site.release);

    const client = createHexClient({ site: "demo", baseURL: origin });
    await verifyClientStorage(client);
    await verifyRealtime(client);
    await verifyPublishing(origin, projectDirectory, invoke);

    await stopProcess(server);
    assert.equal((await fetch(`${origin}/api/hex/capabilities`)).status, 502);
    assert.equal((await fetch(`${origin}/healthz`)).status, 200);
    assert.equal(await readText(`${origin}/sites/demo/`), "updated site");
    assert.match(
      await readText(`${origin}/sites/demo/hex-client.js`),
      /createHexClient/,
    );

    console.log(
      "End-to-end passed: NGINX static sites, proxied API/WebSockets, publishing lifecycle, and static availability with Go stopped.",
    );
  } finally {
    await stopProcess(nginx);
    await stopProcess(server);
    await rm(directory, { recursive: true, force: true });
  }
}

await main();

import assert from "node:assert/strict";
import { spawn, execFile } from "node:child_process";
import { promisify } from "node:util";
import { mkdtemp, mkdir, rm, writeFile, symlink } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { fileURLToPath } from "node:url";
import { createServer } from "node:net";
import { once } from "node:events";
import { createHexClient } from "../packages/client/dist/index.js";
import WebSocket from "ws";
import { startNginx, waitForHTTP, stopProcess } from "./nginx.mjs";

globalThis.WebSocket = WebSocket;

const exec = promisify(execFile);
const root = fileURLToPath(new URL("../", import.meta.url));
const directory = await mkdtemp(join(tmpdir(), "hex-e2e-"));
let server;
let nginx;
async function freePort() {
  const listener = createServer().listen(0, "127.0.0.1");
  await once(listener, "listening");
  const port = listener.address().port;
  await new Promise(resolve => listener.close(resolve));
  return port;
}
try {
  const backendPort = await freePort();
  let port = await freePort();
  while (port === backendPort) port = await freePort();
  const origin = `http://127.0.0.1:${port}`;
  const backend = `http://127.0.0.1:${backendPort}`;
  const sitesDirectory = join(directory, "sites");
  const binary = join(directory, "hex-server");
  await exec("go", ["build", "-o", binary, "./cmd/hex-server"], { cwd: root });
  server = spawn(binary, [], { cwd: directory, env: { ...process.env, HEX_ADDR: `127.0.0.1:${backendPort}`, HEX_SITES_DIR: sitesDirectory, DATABASE_URL: "", AZURE_BLOB_ENDPOINT: "" }, stdio: ["ignore", "ignore", "inherit"] });
  await waitForHTTP(`${backend}/api/hex/capabilities`, server);
  nginx = await startNginx({ directory: join(directory, "nginx"), sitesDirectory, port, backendPort });
  await waitForHTTP(`${origin}/healthz`, nginx);
  const cli = join(root, "packages/cli/src/cli.mjs");
  const project = join(directory, "demo");
  await exec(process.execPath, [cli, "init", project, "--server", origin]);
  const invoke = args => exec(process.execPath, [cli, ...args], { cwd: project });
  const published = await invoke(["publish"]);
  assert.equal(published.stdout.trim(), `${origin}/sites/demo/`);
  assert.match(await (await fetch(published.stdout.trim())).text(), /Welcome to Hex/);
  assert.match(await (await fetch(`${origin}/sites/demo/hex-client.js`)).text(), /createHexClient/);
  assert.equal((await fetch(`${backend}/sites/demo/`)).status, 404);
  assert.equal((await fetch(`${origin}/sites/demo`, { redirect: "manual" })).headers.get("location"), "/sites/demo/");
  const asset = await fetch(`${origin}/sites/demo/hex-client.js`);
  assert.match(asset.headers.get("content-type"), /javascript/);
  assert.equal(asset.headers.get("x-content-type-options"), "nosniff");
  assert.equal((await fetch(`${origin}/sites/demo/missing.js`)).status, 404);
  const listedSite = JSON.parse((await invoke(["sites"])).stdout)[0];
  for (const path of ["/sites/demo.json", `/releases/demo/${listedSite.release}/index.html`, "/public/sites/demo/index.html"]) {
    assert.equal((await fetch(origin + path)).status, 404);
  }
  await writeFile(join(sitesDirectory, "public/sites/demo/.hex-test"), "temporary file");
  assert.equal((await fetch(`${origin}/sites/demo/.hex-test`)).status, 404);
  await rm(join(sitesDirectory, "public/sites/demo/.hex-test"));
  const secret = join(directory, "secret.txt");
  await writeFile(secret, "not published");
  await symlink(secret, join(sitesDirectory, "public/sites/demo/leak.txt"));
  assert.notEqual((await fetch(`${origin}/sites/demo/leak.txt`)).status, 200);
  await rm(join(sitesDirectory, "public/sites/demo/leak.txt"));
  await mkdir(join(project, "public/nested"));
  await writeFile(join(project, "public/nested/index.html"), "nested page");
  await writeFile(join(project, "public/old.js"), "old asset");
  await invoke(["publish"]);
  assert.equal(await (await fetch(`${origin}/sites/demo/nested/`)).text(), "nested page");
  const hex = createHexClient({ site: "demo", baseURL: origin });
  await hex.files.upload("binary.dat", new Uint8Array([0, 255, 128]));
  assert.deepEqual(new Uint8Array(await (await hex.files.download("binary.dat")).arrayBuffer()), new Uint8Array([0, 255, 128]));
  assert.equal((await hex.files.list()).length, 1);
  const notes = hex.db.collection("notes");
  const note = await notes.create({ message: "works" });
  assert.deepEqual((await notes.get(note.id)).data, { message: "works" });
  await notes.set(note.id, { message: "updated" });
  assert.equal((await notes.list())[0].data.message, "updated");
  await notes.delete(note.id);
  await hex.files.delete("binary.dat");
  let received;
  const message = new Promise(resolve => { received = resolve; });
  const channel = hex.realtime.connect("updates", { onMessage: received });
  try {
    await channel.ready;
    await channel.send({ type: "refresh" });
    assert.deepEqual(await Promise.race([message, new Promise((_, reject) => {
      const timer = setTimeout(() => reject(new Error("Realtime message timed out")), 5000);
      timer.unref();
    })]), { type: "refresh" });
  } finally {
    channel.close();
  }
  await writeFile(join(project, "public/index.html"), "updated site");
  await rm(join(project, "public/old.js"));
  await invoke(["publish"]);
  assert.equal(await (await fetch(`${origin}/sites/demo/`)).text(), "updated site");
  assert.equal((await fetch(`${origin}/sites/demo/old.js`)).status, 404);
  assert.equal(JSON.parse((await invoke(["sites"])).stdout).length, 1);
  await invoke(["delete", "--yes"]);
  assert.equal((await fetch(`${origin}/sites/demo/`)).status, 404);
  await invoke(["publish"]);
  await stopProcess(server);
  assert.equal((await fetch(`${origin}/api/hex/capabilities`)).status, 502);
  assert.equal((await fetch(`${origin}/healthz`)).status, 200);
  assert.equal(await (await fetch(`${origin}/sites/demo/`)).text(), "updated site");
  assert.match(await (await fetch(`${origin}/sites/demo/hex-client.js`)).text(), /createHexClient/);
  console.log("End-to-end passed: NGINX static sites, proxied API/WebSockets, publishing lifecycle, and static availability with Go stopped.");
} finally {
  await stopProcess(nginx);
  await stopProcess(server);
  await rm(directory, { recursive: true, force: true });
}

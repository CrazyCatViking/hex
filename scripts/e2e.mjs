import assert from "node:assert/strict";
import { spawn, execFile } from "node:child_process";
import { promisify } from "node:util";
import {
  mkdtemp,
  mkdir,
  readFile,
  rm,
  writeFile,
  symlink,
} from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { fileURLToPath } from "node:url";
import { createServer } from "node:net";
import { once } from "node:events";
import WebSocket from "ws";
import { Agent } from "undici";
import { createHexClient } from "@crazycatviking/hex";
import { startNginx, waitForHTTP, stopProcess } from "./nginx.mjs";
import { buildCLI } from "./build-cli.mjs";

function lookupLoopback(hostname, options, callback) {
  if (options.all) {
    callback(null, [{ address: "127.0.0.1", family: 4 }]);
  } else {
    callback(null, "127.0.0.1", 4);
  }
}

globalThis.WebSocket = class extends WebSocket {
  constructor(url) {
    super(url, { lookup: lookupLoopback });
  }
};

const exec = promisify(execFile);
const root = fileURLToPath(new URL("../", import.meta.url));
const loopback = new Agent({
  connect: { lookup: lookupLoopback },
});

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

function siteRequest(origin, path = "/", site = "demo", options = {}) {
  const url = new URL(path, origin);
  url.hostname = `${site}.localhost`;
  return fetch(url, {
    ...options,
    dispatcher: loopback,
  });
}

async function readSiteText(origin, path = "/", site = "demo") {
  const response = await siteRequest(origin, path, site);
  assert.equal(response.status, 200, `${site}: ${path}`);
  return response.text();
}

async function verifyStaticServing(origin, backend) {
  assert.match(await readSiteText(origin), /Welcome to Hex/);
  assert.match(await readSiteText(origin, "/app.js"), /console.log/);
  assert.equal((await fetch(`${backend}/sites/demo/`)).status, 404);

  assert.equal((await fetch(`${origin}/sites/demo/`)).status, 404);
  assert.equal((await fetch(origin)).status, 404);
  assert.equal(
    (
      await fetch(`http://demo.evil.example:${new URL(origin).port}/`, {
        dispatcher: loopback,
      })
    ).status,
    404,
  );

  const asset = await siteRequest(origin, "/app.js");
  assert.match(asset.headers.get("content-type"), /javascript/);
  assert.equal(asset.headers.get("x-content-type-options"), "nosniff");
  assert.equal((await siteRequest(origin, "/missing.js")).status, 404);
  assert.equal(
    (await siteRequest(origin, "/app.js", "missing-site")).status,
    404,
  );
}

async function verifyPrivateFiles(origin, directory, sitesDirectory) {
  const privatePaths = [
    "/sites/demo.json",
    "/releases/demo/old/index.html",
    "/public/sites/demo/index.html",
  ];
  for (const path of privatePaths) {
    assert.equal((await fetch(origin + path)).status, 404);
  }

  const hiddenFile = join(sitesDirectory, "public/sites/demo/.hex-test");
  await writeFile(hiddenFile, "temporary file");
  assert.equal((await siteRequest(origin, "/.hex-test")).status, 404);
  await rm(hiddenFile);

  const secret = join(directory, "secret.txt");
  const publicLink = join(sitesDirectory, "public/sites/demo/leak.txt");
  await writeFile(secret, "not published");
  await symlink(secret, publicLink);
  assert.notEqual((await siteRequest(origin, "/leak.txt")).status, 200);
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
  await mkdir(join(projectDirectory, "dist/nested"));
  await writeFile(
    join(projectDirectory, "dist/nested/index.html"),
    "nested page",
  );
  await writeFile(join(projectDirectory, "dist/old.js"), "old asset");
  await invoke(["publish"]);
  assert.equal(await readSiteText(origin, "/nested/"), "nested page");

  await writeFile(join(projectDirectory, "dist/index.html"), "updated site");
  await rm(join(projectDirectory, "dist/old.js"));
  await invoke(["publish"]);
  assert.equal(await readSiteText(origin), "updated site");
  assert.equal((await siteRequest(origin, "/old.js")).status, 404);

  const siteList = await invoke(["sites"]);
  assert.equal(JSON.parse(siteList.stdout).length, 1);

  await invoke(["delete", "--yes"]);
  assert.equal((await siteRequest(origin)).status, 404);
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
        HEX_SITE_BASE_URL: `http://localhost:${port}`,
        HEX_PUBLIC_URL: `http://localhost:${port}`,
        HEX_PLATFORM_NAME: "Test Hex",
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

    const cli = await buildCLI(directory);
    const projectDirectory = join(directory, "demo");
    await exec(cli, [
      "init",
      projectDirectory,
      "--server",
      origin,
      "--site-base-url",
      `http://localhost:${port}`,
      "--publish-root",
      join(sitesDirectory, "public/sites"),
    ]);

    await mkdir(join(projectDirectory, "dist"));
    await writeFile(
      join(projectDirectory, "dist/index.html"),
      "<!doctype html><title>Welcome to Hex</title>",
    );
    await writeFile(
      join(projectDirectory, "dist/app.js"),
      "console.log('test asset');",
    );
    const invoke = (args) => exec(cli, args, { cwd: projectDirectory });
    const configurationPath = join(projectDirectory, "hex.json");
    const configuration = JSON.parse(await readFile(configurationPath, "utf8"));
    configuration.title = "Team dashboard";
    configuration.description = "Daily reports";
    configuration.author = "Alex";
    configuration.discoverable = false;
    await writeFile(configurationPath, JSON.stringify(configuration));
    await invoke(["publish"]);
    assert.equal((await siteRequest(origin)).status, 200);
    assert.deepEqual(JSON.parse((await invoke(["sites"])).stdout), []);
    const hiddenOverview = await fetch(`${origin}/api/hex/overview`);
    assert.equal((await hiddenOverview.json()).statistics.sites, 0);
    configuration.discoverable = true;
    await writeFile(configurationPath, JSON.stringify(configuration));
    const published = await invoke(["publish"]);
    assert.equal(published.stdout.trim(), `http://demo.localhost:${port}/`);

    await verifyStaticServing(origin, backend);
    const siteList = await invoke(["sites"]);
    const site = JSON.parse(siteList.stdout)[0];
    assert.equal(site.name, "demo");
    assert.equal(site.url, `http://demo.localhost:${port}/`);
    assert.ok(Number.isFinite(Date.parse(site.metadata.publishedAt)));
    assert.equal((await siteRequest(origin, "/.hex-site.json")).status, 404);
    await verifyPrivateFiles(origin, directory, sitesDirectory);

    const client = createHexClient({
      site: "demo",
      baseURL: `http://demo.localhost:${port}`,
      fetch: (url, options) => {
        const headers = new Headers(options.headers);
        headers.set("Origin", `http://demo.localhost:${port}`);
        return fetch(url, { ...options, headers, dispatcher: loopback });
      },
    });
    await verifyClientStorage(client);
    await verifyRealtime(client);
    await verifyPublishing(origin, projectDirectory, invoke);

    if (process.argv.includes("--browser")) {
      const otherSite = join(sitesDirectory, "public/sites/other");
      await mkdir(otherSite, { recursive: true });
      await writeFile(
        join(otherSite, "index.html"),
        "<!doctype html><title>Other site</title>",
      );
      try {
        const { verifyBrowserIsolation } =
          await import("./browser-isolation.mjs");
        await verifyBrowserIsolation(port);
        const { verifyPortal } = await import("./test-portal.mjs");
        await verifyPortal(port);
      } finally {
        await rm(otherSite, { recursive: true });
      }
    }

    await stopProcess(server);
    assert.equal((await fetch(`${origin}/api/hex/capabilities`)).status, 502);
    await writeFile(
      join(projectDirectory, "dist/index.html"),
      "published without an API",
    );
    await invoke(["publish"]);
    assert.equal((await fetch(`${origin}/healthz`)).status, 200);
    assert.equal(await readSiteText(origin), "published without an API");
    assert.match(await readSiteText(origin, "/app.js"), /console.log/);

    await invoke(["delete", "--yes"]);
    assert.equal((await siteRequest(origin)).status, 404);

    console.log(
      "End-to-end passed: direct publishing, directory discovery, API/WebSockets, and publishing/unpublishing with Go stopped.",
    );
  } finally {
    await stopProcess(nginx);
    await stopProcess(server);
    await rm(directory, { recursive: true, force: true });
    await loopback.close();
  }
}

await main();

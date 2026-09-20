import { test } from "node:test";
import assert from "node:assert/strict";
import {
  mkdtemp,
  readFile,
  writeFile,
  mkdir,
  symlink,
  rm,
} from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { fileURLToPath } from "node:url";
import { promisify } from "node:util";
import { execFile } from "node:child_process";
import { createServer } from "node:http";
import { once } from "node:events";
import { azureFilesPublisher } from "../src/publishing/azure-files.mjs";
import { siteURL } from "../src/site-url.mjs";

const exec = promisify(execFile);
const cli = fileURLToPath(new URL("../src/cli.mjs", import.meta.url));

test("site URLs use distinct DNS origins and reject unsafe labels", () => {
  assert.equal(
    siteURL("https://hex.example.com", "one"),
    "https://one.hex.example.com/",
  );
  assert.equal(
    siteURL("http://localhost:8080", "two"),
    "http://two.localhost:8080/",
  );
  for (const name of [
    "../other",
    "upperCase",
    "under_score",
    "-start",
    "end-",
    "a".repeat(64),
  ]) {
    assert.throws(() => siteURL("https://hex.example.com", name), /Site names/);
  }
  assert.throws(() => siteURL("http://127.0.0.1:8080", "one"), /DNS hostname/);
});

test("init creates a working static project and skill without replacing existing configuration", async (t) => {
  const root = await mkdtemp(join(tmpdir(), "hex-init-"));
  t.after(() => rm(root, { recursive: true, force: true }));
  await exec(process.execPath, [cli, "init", root, "--name", "demo"]);
  const config = JSON.parse(await readFile(join(root, "hex.json"), "utf8"));
  assert.equal(config.directory, "public");
  assert.equal(config.name, "demo");
  assert.match(
    await readFile(join(root, "public/hex-client.js"), "utf8"),
    /createHexClient/,
  );
  assert.match(
    await readFile(join(root, ".agents/skills/hex/SKILL.md"), "utf8"),
    /hex\.files\.upload/,
  );
  await assert.rejects(
    exec(process.execPath, [cli, "init", root, "--name", "different"]),
  );
  assert.equal(
    JSON.parse(await readFile(join(root, "hex.json"), "utf8")).name,
    "demo",
  );
});

test("CLI runs through a symlink like an npm-installed binary", async (t) => {
  const root = await mkdtemp(join(tmpdir(), "hex-bin-"));
  t.after(() => rm(root, { recursive: true, force: true }));
  const link = join(root, "hex");
  await symlink(cli, link);
  const result = await exec(process.execPath, [link, "--help"]);
  assert.match(result.stdout, /hex publish/);
});

test("publishing and unpublishing synchronize storage without contacting Hex", async (t) => {
  const root = await mkdtemp(join(tmpdir(), "hex-publish-"));
  t.after(() => rm(root, { recursive: true, force: true }));
  let requests = 0;
  const server = createServer((request, response) => {
    requests += 1;
    response.writeHead(500).end();
  }).listen(0, "127.0.0.1");
  await once(server, "listening");
  t.after(() => new Promise((resolve) => server.close(resolve)));

  const destination = join(root, "sites");
  const project = join(root, "project");
  const origin = `http://127.0.0.1:${server.address().port}`;
  await exec(process.execPath, [
    cli,
    "init",
    project,
    "--name",
    "demo",
    "--server",
    origin,
    "--site-base-url",
    "http://localhost:8080",
    "--resource",
    "must-not-obtain-api-token",
    "--publish-root",
    destination,
  ]);
  const invoke = (args) =>
    exec(process.execPath, [cli, ...args], { cwd: project });
  const source = join(project, "public");

  await writeFile(join(source, "index.html"), "first");
  await writeFile(join(source, ".env"), "secret");
  await mkdir(join(source, "node_modules"));
  await writeFile(join(source, "node_modules/package.js"), "dependency");
  await writeFile(join(source, "asset"), "file");
  await mkdir(join(destination, "other"), { recursive: true });
  await writeFile(join(destination, "other/index.html"), "another site");

  const published = await invoke(["publish"]);
  assert.equal(published.stdout.trim(), "http://demo.localhost:8080/");
  assert.equal(
    await readFile(join(destination, "demo/index.html"), "utf8"),
    "first",
  );
  await assert.rejects(readFile(join(destination, "demo/.env")), {
    code: "ENOENT",
  });
  await assert.rejects(
    readFile(join(destination, "demo/node_modules/package.js")),
    { code: "ENOENT" },
  );

  await rm(join(source, "asset"));
  await mkdir(join(source, "asset"));
  await writeFile(join(source, "asset/nested.js"), "nested");
  await invoke(["publish"]);
  assert.equal(
    await readFile(join(destination, "demo/asset/nested.js"), "utf8"),
    "nested",
  );

  await rm(join(source, "asset"), { recursive: true });
  await writeFile(join(source, "asset"), "file again");
  await invoke(["publish"]);
  assert.equal(
    await readFile(join(destination, "demo/asset"), "utf8"),
    "file again",
  );

  await rm(join(source, "index.html"));
  await assert.rejects(invoke(["publish"]), /index.html/);
  assert.equal(
    await readFile(join(destination, "demo/index.html"), "utf8"),
    "first",
  );

  await invoke(["delete", "--yes"]);
  await assert.rejects(readFile(join(destination, "demo/index.html")), {
    code: "ENOENT",
  });
  assert.equal(
    await readFile(join(destination, "other/index.html"), "utf8"),
    "another site",
  );
  assert.equal(requests, 0);
});

test("direct publishing rejects symlinks, overlap and invalid site names", async (t) => {
  const root = await mkdtemp(join(tmpdir(), "hex-boundaries-"));
  t.after(() => rm(root, { recursive: true, force: true }));
  const project = join(root, "project");
  const destination = join(root, "sites");
  await exec(process.execPath, [
    cli,
    "init",
    project,
    "--name",
    "demo",
    "--publish-root",
    destination,
  ]);
  const invoke = (args) =>
    exec(process.execPath, [cli, ...args], { cwd: project });
  await symlink(
    join(project, "public/index.html"),
    join(project, "public/link.html"),
  );
  await assert.rejects(invoke(["publish"]), /Symlinks/);
  await rm(join(project, "public/link.html"));

  await mkdir(destination);
  await symlink(join(project, "public"), join(destination, "demo"));
  await assert.rejects(invoke(["publish"]), /real directory/);
  await assert.rejects(invoke(["delete", "../project", "--yes"]), /Site names/);
  await rm(join(destination, "demo"));

  const configPath = join(project, "hex.json");
  const config = JSON.parse(await readFile(configPath, "utf8"));
  config.publishing.root = join(project, "public");
  await writeFile(configPath, JSON.stringify(config));
  await assert.rejects(invoke(["publish"]), /overlap/);
});

test("Azure Files adapter invokes AzCopy directly and cleans its temporary source", async (t) => {
  const root = await mkdtemp(join(tmpdir(), "hex-azure-publish-"));
  t.after(() => rm(root, { recursive: true, force: true }));
  const file = join(root, "index.html");
  await writeFile(file, "hello");
  const calls = [];
  const publisher = azureFilesPublisher(
    { url: "https://account.file.core.windows.net/sites/public/sites" },
    async (args) => {
      calls.push(args);
      if (args[0] === "sync") {
        assert.equal(
          await readFile(join(args[1], "index.html"), "utf8"),
          "hello",
        );
      }
    },
  );
  await publisher.publish("demo", {
    files: [{ key: "index.html", path: file }],
  });
  assert.equal(calls[0][0], "sync");
  assert.equal(
    calls[0][2].split("?")[0],
    "https://account.file.core.windows.net/sites/public/sites/demo",
  );
  assert.ok(calls[0].includes("--delete-destination=true"));
  await assert.rejects(readFile(join(calls[0][1], "index.html")), {
    code: "ENOENT",
  });
  await publisher.delete("demo");
  assert.equal(calls[1][0], "remove");
  assert.ok(calls[1].includes("--recursive=true"));

  assert.throws(
    () =>
      azureFilesPublisher({
        url: "https://account.file.core.windows.net/sites?sig=secret",
      }),
    /without credentials/,
  );
  await assert.rejects(publisher.delete("../another-site"), /Site names/);
});

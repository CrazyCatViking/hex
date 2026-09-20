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
import { unzipSync } from "fflate";
import { archiveDirectory } from "../src/cli.mjs";

const exec = promisify(execFile);
const cli = fileURLToPath(new URL("../src/cli.mjs", import.meta.url));

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

test("archive skips hidden files and dependencies, rejects symlinks and oversize payloads", async (t) => {
  const root = await mkdtemp(join(tmpdir(), "hex-zip-"));
  t.after(() => rm(root, { recursive: true, force: true }));
  await writeFile(join(root, "index.html"), "hello");
  await writeFile(join(root, ".env"), "secret");
  await mkdir(join(root, "node_modules"));
  await writeFile(join(root, "node_modules/package.js"), "code");
  const files = unzipSync(await archiveDirectory(root, 4096));
  assert.deepEqual(Object.keys(files), ["index.html"]);
  await assert.rejects(archiveDirectory(root, 4), /limit/);
  await symlink(join(root, ".env"), join(root, "leak.txt"));
  await assert.rejects(archiveDirectory(root, 4096), /Symlinks/);
});

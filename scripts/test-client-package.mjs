import assert from "node:assert/strict";
import { execFile } from "node:child_process";
import { mkdtemp, readFile, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { fileURLToPath } from "node:url";
import { promisify } from "node:util";

const exec = promisify(execFile);
const root = fileURLToPath(new URL("../", import.meta.url));
const directory = await mkdtemp(join(tmpdir(), "hex-client-package-"));

try {
  const source = JSON.parse(
    await readFile(
      new URL("../packages/client/package.json", import.meta.url),
      "utf8",
    ),
  );
  await exec("just", ["pack-client", directory], { cwd: root });
  const tarball = join(directory, `crazycatviking-hex-${source.version}.tgz`);
  await writeFile(
    join(directory, "package.json"),
    JSON.stringify({ private: true, type: "module" }),
  );
  await exec(
    "npm",
    [
      "install",
      "--offline",
      "--ignore-scripts",
      "--no-audit",
      "--no-fund",
      tarball,
    ],
    { cwd: directory },
  );
  const installedDirectory = join(
    directory,
    "node_modules/@crazycatviking/hex",
  );
  const installed = JSON.parse(
    await readFile(join(installedDirectory, "package.json"), "utf8"),
  );
  assert.equal(installed.name, "@crazycatviking/hex");
  assert.equal(installed.version, source.version);
  assert.match(
    await readFile(
      join(installedDirectory, installed.exports["."].types),
      "utf8",
    ),
    /createHexClient/,
  );
  await exec(
    process.execPath,
    [
      "--input-type=module",
      "-e",
      `
    import assert from 'node:assert/strict';
    import { createHexClient, HexError } from '@crazycatviking/hex';
    const client = createHexClient({ site: 'test' });
    assert.equal(typeof client.files.upload, 'function');
    assert.equal(typeof client.db.collection, 'function');
    assert.equal(typeof client.realtime.connect, 'function');
    assert.equal(typeof HexError, 'function');
  `,
    ],
    { cwd: directory },
  );
  console.log(
    `Packed @crazycatviking/hex@${source.version}: independent installation, ESM imports, and TypeScript declarations verified.`,
  );
} finally {
  await rm(directory, { recursive: true, force: true });
}

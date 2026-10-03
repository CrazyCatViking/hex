import assert from "node:assert/strict";
import { execFileSync, spawnSync } from "node:child_process";
import {
  copyFile,
  mkdir,
  mkdtemp,
  readFile,
  rm,
  writeFile,
} from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { test } from "node:test";

const fakeTool = `#!${process.execPath}
import { appendFileSync, readFileSync, writeFileSync } from "node:fs";
import { basename } from "node:path";
const tool = basename(process.argv[1]);
const args = process.argv.slice(2);
appendFileSync(process.env.RELEASE_LOG, JSON.stringify([tool, ...args]) + "\\n");
if (process.env.FAIL_COMMAND === tool + " " + args[0]) {
  process.exit(1);
}
if (tool === "just" && args[0] === "version-client") {
  const file = "packages/client/package.json";
  const metadata = JSON.parse(readFileSync(file, "utf8"));
  metadata.version = args[1];
  writeFileSync(file, JSON.stringify(metadata) + "\\n");
  writeFileSync("package-lock.json", JSON.stringify({ version: args[1] }) + "\\n");
}
`;

async function fixture(t, version = "0.9.3") {
  const directory = await mkdtemp(join(tmpdir(), "hex-release-test-"));
  t.after(() => rm(directory, { recursive: true, force: true }));
  const root = join(directory, "checkout");
  const remote = join(directory, "remote.git");
  const bin = join(directory, "bin");
  const log = join(directory, "commands.jsonl");
  await mkdir(join(root, "scripts"), { recursive: true });
  await mkdir(join(root, "packages/client"), { recursive: true });
  await mkdir(bin);
  await copyFile(
    new URL("release.mjs", import.meta.url),
    join(root, "scripts/release.mjs"),
  );
  await writeFile(
    join(root, "packages/client/package.json"),
    JSON.stringify({ version }),
  );
  await writeFile(join(root, "package-lock.json"), "{}\n");
  await writeFile(log, "");
  for (const tool of ["npm", "just", "go", "gh"]) {
    await writeFile(join(bin, tool), fakeTool, { mode: 0o755 });
  }

  const env = {
    ...process.env,
    PATH: `${bin}:${process.env.PATH}`,
    RELEASE_LOG: log,
    GIT_AUTHOR_NAME: "Release Test",
    GIT_AUTHOR_EMAIL: "release@example.test",
    GIT_COMMITTER_NAME: "Release Test",
    GIT_COMMITTER_EMAIL: "release@example.test",
    GIT_CONFIG_NOSYSTEM: "1",
    GIT_CONFIG_GLOBAL: "/dev/null",
  };
  function git(...args) {
    return execFileSync("git", args, {
      cwd: root,
      env,
      encoding: "utf8",
      stdio: ["ignore", "pipe", "pipe"],
    }).trim();
  }
  git("init", "--bare", remote);
  git("init", "-b", "main");
  git("add", ".");
  git("commit", "-m", "Initial source");
  git("remote", "add", "origin", remote);
  git("push", "-u", "origin", "main");

  return {
    root,
    git,
    release(target, version = "", overrides = {}) {
      return spawnSync(
        process.execPath,
        ["scripts/release.mjs", target, version],
        {
          cwd: root,
          env: { ...env, ...overrides },
          encoding: "utf8",
        },
      );
    },
    async commands() {
      return (await readFile(log, "utf8"))
        .trim()
        .split("\n")
        .filter(Boolean)
        .map(JSON.parse);
    },
  };
}

function succeeded(result) {
  assert.equal(result.status, 0, result.stdout + result.stderr);
}

test("client defaults to the next minor, verifies, commits, pushes, and publishes", async (t) => {
  const repo = await fixture(t);
  succeeded(repo.release("client"));
  assert.deepEqual(await repo.commands(), [
    ["npm", "whoami"],
    ["npm", "ci"],
    ["just", "version-client", "0.10.0"],
    ["just", "pack-client"],
    ["just", "test-client-package"],
    [
      "npm",
      "publish",
      "--workspace",
      "@crazycatviking/hex",
      "--access",
      "public",
      "--tag",
      "latest",
    ],
  ]);
  assert.equal(repo.git("log", "-1", "--format=%s"), "Publish 0.10.0 client");
  assert.equal(
    repo.git("rev-parse", "HEAD"),
    repo.git("rev-parse", "origin/main"),
  );
  assert.equal(repo.git("status", "--porcelain"), "");
  assert.deepEqual(repo.git("diff", "HEAD~", "--name-only").split("\n"), [
    "package-lock.json",
    "packages/client/package.json",
  ]);
});

test("explicit client prerelease publishes to next", async (t) => {
  const repo = await fixture(t);
  succeeded(repo.release("client", "1.0.0-rc.1"));
  const commands = await repo.commands();
  assert.deepEqual(commands[2], ["just", "version-client", "1.0.0-rc.1"]);
  assert.equal(commands.at(-1).at(-1), "next");
});

test("failed client verification never commits, pushes, or publishes", async (t) => {
  const repo = await fixture(t);
  const head = repo.git("rev-parse", "HEAD");
  const result = repo.release("client", "", {
    FAIL_COMMAND: "just pack-client",
  });
  assert.equal(result.status, 1);
  assert.match(result.stderr, /Failed: just pack-client/);
  assert.equal(repo.git("rev-parse", "HEAD"), head);
  assert.equal(repo.git("rev-parse", "origin/main"), head);
  assert.equal(
    (await repo.commands()).some((command) => command[1] === "publish"),
    false,
  );
});

test("failed npm publication reports the exact retry command", async (t) => {
  const repo = await fixture(t);
  const result = repo.release("client", "", { FAIL_COMMAND: "npm publish" });
  assert.equal(result.status, 1);
  assert.match(result.stderr, /Client 0\.10\.0 was committed and pushed/);
  assert.match(result.stderr, /Retry with: npm publish .*--tag latest/);
  assert.equal(
    repo.git("rev-parse", "HEAD"),
    repo.git("rev-parse", "origin/main"),
  );
});

test("CLI fetches tags and uses numeric minor ordering, independently of the client", async (t) => {
  const repo = await fixture(t, "8.0.0");
  for (const tag of [
    "cli-v0.9.9",
    "cli-v0.10.2",
    "cli-v0.11.0-rc.1",
    "cli-vinvalid",
  ]) {
    repo.git("tag", tag);
    repo.git("push", "origin", tag);
    repo.git("tag", "-d", tag);
  }
  succeeded(repo.release("cli"));
  assert.deepEqual(await repo.commands(), [
    ["gh", "auth", "status"],
    ["go", "test", "-race", "./..."],
    ["go", "vet", "./..."],
    ["just", "build-cli", "0.12.0"],
    ["just", "upload-cli", "0.12.0"],
  ]);
  assert.match(
    repo.git("ls-remote", "--tags", "origin", "cli-v0.12.0"),
    /refs\/tags\/cli-v0\.12\.0$/,
  );
  assert.equal(
    repo.git("rev-parse", "cli-v0.12.0"),
    repo.git("rev-parse", "HEAD"),
  );
  assert.equal(repo.git("log", "--format=%s"), "Initial source");
});

test("CLI accepts an explicit prerelease and starts at 0.1.0 without tags", async (t) => {
  const repo = await fixture(t);
  succeeded(repo.release("cli"));
  assert.equal(repo.git("tag", "--list", "cli-v0.1.0"), "cli-v0.1.0");
  succeeded(repo.release("cli", "1.0.0-rc.2"));
  assert.deepEqual((await repo.commands()).at(-1), [
    "just",
    "upload-cli",
    "1.0.0-rc.2",
  ]);
});

test("CLI verification failure does not tag or upload", async (t) => {
  const repo = await fixture(t);
  const result = repo.release("cli", "1.0.0", { FAIL_COMMAND: "go test" });
  assert.equal(result.status, 1);
  assert.equal(repo.git("tag", "--list"), "");
  assert.equal(
    (await repo.commands()).some((command) => command[1] === "upload-cli"),
    false,
  );
});

test("CLI rejects an existing remote tag before building", async (t) => {
  const repo = await fixture(t);
  repo.git("tag", "cli-v1.0.0");
  repo.git("push", "origin", "cli-v1.0.0");
  repo.git("tag", "-d", "cli-v1.0.0");
  const result = repo.release("cli", "1.0.0");
  assert.equal(result.status, 1);
  assert.match(result.stderr, /cli-v1\.0\.0 already exists/);
  assert.deepEqual(await repo.commands(), []);
});

test("failed CLI upload leaves the pushed tag and gives a retry command", async (t) => {
  const repo = await fixture(t);
  const result = repo.release("cli", "1.0.0", {
    FAIL_COMMAND: "just upload-cli",
  });
  assert.equal(result.status, 1);
  assert.match(result.stderr, /Retry with: just upload-cli 1\.0\.0/);
  assert.match(
    repo.git("ls-remote", "--tags", "origin", "cli-v1.0.0"),
    /refs\/tags\/cli-v1\.0\.0$/,
  );
});

test("invalid versions and uncommitted source stop before release commands", async (t) => {
  const repo = await fixture(t);
  for (const version of [
    "next",
    "v1.0.0",
    "01.0.0",
    "1.0",
    "1.0.0-rc..1",
    "1.0.0-01",
  ]) {
    const result = repo.release("client", version);
    assert.equal(result.status, 1);
    assert.match(result.stderr, /Use a version/);
  }
  await writeFile(join(repo.root, "uncommitted.txt"), "work in progress");
  const result = repo.release("cli");
  assert.equal(result.status, 1);
  assert.match(result.stderr, /Commit or stash/);
  assert.deepEqual(await repo.commands(), []);
});

test("a branch behind its upstream stops before modifying the release version", async (t) => {
  const repo = await fixture(t);
  const head = repo.git("rev-parse", "HEAD");
  await writeFile(join(repo.root, "new-source.txt"), "new source");
  repo.git("add", "new-source.txt");
  repo.git("commit", "-m", "New upstream source");
  repo.git("push", "origin", "main");
  repo.git("reset", "--hard", head);

  const result = repo.release("client");
  assert.equal(result.status, 1);
  assert.match(result.stderr, /Update the release branch from its upstream/);
  assert.deepEqual(await repo.commands(), []);
  assert.equal(repo.git("status", "--porcelain"), "");
});

import assert from "node:assert/strict";
import { test } from "node:test";
import { createServer } from "node:http";
import { once } from "node:events";
import { execFile } from "node:child_process";
import { promisify } from "node:util";
import { mkdir, mkdtemp, readFile, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join, delimiter } from "node:path";
import { fileURLToPath } from "node:url";
import { validateConnection } from "../src/connection.mjs";
import { droppedFilePath } from "../src/setup/files.mjs";
import { downloadConnection } from "../src/setup/download.mjs";
import { setupPlatform } from "../src/setup/index.mjs";
import { loadProfile } from "../src/profiles.mjs";

const exec = promisify(execFile);
const cli = fileURLToPath(new URL("../src/cli.mjs", import.meta.url));

async function fixture(t) {
  const directory = await mkdtemp(join(tmpdir(), "hex-setup-"));
  t.after(() => rm(directory, { recursive: true, force: true }));
  let mode = "direct";
  let requests = 0;
  const server = createServer((request, response) => {
    requests += 1;
    assert.equal(request.url, "/api/hex/config");
    assert.equal(request.headers.authorization, undefined);
    if (mode === "redirect") {
      response.writeHead(302, { Location: "/login" }).end();
    } else if (mode === "html") {
      response.writeHead(200, { "Content-Type": "text/html" }).end("Sign in");
    } else if (mode === "denied") {
      response.writeHead(401).end();
    } else if (mode === "missing") {
      response.writeHead(404).end();
    } else {
      response
        .writeHead(200, { "Content-Type": "application/json" })
        .end(JSON.stringify(connection));
    }
  }).listen(0, "127.0.0.1");
  await once(server, "listening");
  t.after(() => new Promise((resolve) => server.close(resolve)));
  const origin = `http://127.0.0.1:${server.address().port}`;
  const connection = {
    version: 1,
    name: "Test Company",
    server: origin,
    siteBaseURL: "http://localhost:8080",
    publishing: { provider: "filesystem", root: join(directory, "published") },
    capabilities: {
      version: 1,
      sites: true,
      files: true,
      database: false,
      realtime: false,
      maxUploadBytes: 4096,
    },
  };
  const environment = {
    ...process.env,
    HEX_CONFIG_DIR: join(directory, "profiles"),
    HEX_TOKEN: "must-not-be-sent-during-setup",
  };
  return {
    directory,
    origin,
    connection,
    environment,
    mode(value) {
      mode = value;
    },
    requests() {
      return requests;
    },
    invoke(args, cwd = directory) {
      return exec(process.execPath, [cli, ...args], { cwd, env: environment });
    },
  };
}

test("setup downloads directly, creates a profile, and configures offline publishing", async (t) => {
  const platform = await fixture(t);
  const result = await platform.invoke([
    "setup",
    platform.origin,
    "--name",
    "company",
    "--json",
  ]);
  assert.equal(JSON.parse(result.stdout).status, "ready");
  const stored = JSON.parse(
    await readFile(
      join(platform.environment.HEX_CONFIG_DIR, "profiles.json"),
      "utf8",
    ),
  );
  assert.equal(stored.defaultProfile, "company");
  assert.equal(stored.profiles.company.server, platform.origin);
  assert.ok(!JSON.stringify(stored).includes("must-not-be-sent"));

  const project = join(platform.directory, "demo");
  await platform.invoke(["init", project]);
  const config = JSON.parse(await readFile(join(project, "hex.json"), "utf8"));
  assert.deepEqual(config, {
    name: "demo",
    directory: "public",
    platform: "company",
  });
  platform.mode("denied");
  const published = await platform.invoke(["publish"], project);
  assert.equal(published.stdout.trim(), "http://demo.localhost:8080/");
  assert.match(
    await readFile(
      join(platform.connection.publishing.root, "demo/index.html"),
      "utf8",
    ),
    /Welcome to Hex/,
  );
  const capabilities = await platform.invoke(["capabilities"], project);
  assert.equal(JSON.parse(capabilities.stdout).files, true);
  await platform.invoke(["delete", "--yes"], project);
  assert.equal(platform.requests(), 1);
});

test("agents receive a download action without a prompt and can import the user's file", async (t) => {
  const platform = await fixture(t);
  platform.mode("redirect");
  await assert.rejects(
    platform.invoke(["setup", platform.origin, "--json"]),
    (error) => {
      assert.equal(error.code, 2);
      const result = JSON.parse(error.stdout);
      assert.equal(result.status, "download_required");
      assert.equal(result.downloadURL, platform.origin + "/api/hex/config");
      return true;
    },
  );
  assert.equal(platform.requests(), 1);

  const downloads = join(platform.directory, "Downloads with spaces");
  await mkdir(downloads);
  const file = join(downloads, "company config.json");
  await writeFile(file, JSON.stringify(platform.connection));
  const result = await platform.invoke([
    "setup",
    "--file",
    `'${file}'`,
    "--server",
    platform.origin,
    "--name",
    "company",
    "--json",
  ]);
  assert.equal(JSON.parse(result.stdout).profile, "company");
  assert.equal(platform.requests(), 1);
});

test("interactive fallback opens the original protected URL and accepts a dropped file", async (t) => {
  const platform = await fixture(t);
  platform.mode("denied");
  const file = join(platform.directory, "my connection.json");
  await writeFile(file, JSON.stringify(platform.connection));
  const answers = [platform.origin, file.replaceAll(" ", "\\ ")];
  const opened = [];
  const result = await setupPlatform(
    {},
    {
      ask: async () => answers.shift(),
      write() {},
      openBrowser: async (url) => {
        opened.push(url);
      },
    },
    { profileDirectory: platform.environment.HEX_CONFIG_DIR },
  );
  assert.equal(result.status, "ready");
  assert.deepEqual(opened, [platform.origin + "/api/hex/config"]);
  const profile = await loadProfile(undefined, {
    directory: platform.environment.HEX_CONFIG_DIR,
  });
  assert.equal(profile.connection.name, "Test Company");
});

test("login pages trigger browser fallback; a missing config endpoint does not", async (t) => {
  const platform = await fixture(t);
  platform.mode("html");
  assert.equal(
    (await downloadConnection(platform.origin)).status,
    "download_required",
  );
  platform.mode("missing");
  await assert.rejects(downloadConnection(platform.origin), /does not expose/);
});

test("connection validation rejects secrets, version mismatches and wrong-platform files", async (t) => {
  const platform = await fixture(t);
  assert.throws(
    () =>
      validateConnection({
        ...platform.connection,
        clientSecret: "never-store-this",
      }),
    /unsupported fields/,
  );
  assert.throws(
    () => validateConnection({ ...platform.connection, version: 2 }),
    /version/,
  );
  assert.throws(
    () => validateConnection(platform.connection, "https://different.example"),
    /different platform/,
  );
  assert.throws(
    () =>
      validateConnection({
        ...platform.connection,
        server: "https://company.example",
      }),
    /Filesystem publishing/,
  );
  assert.throws(
    () =>
      validateConnection({
        ...platform.connection,
        publishing: {
          provider: "azure-files",
          url: "https://account.file.core.windows.net/sites?sig=secret",
        },
      }),
    /without credentials/,
  );

  const file = join(platform.directory, "invalid.json");
  await writeFile(
    file,
    JSON.stringify({ ...platform.connection, accessToken: "never-store-this" }),
  );
  await assert.rejects(
    platform.invoke(["setup", "--file", file, "--json"]),
    (error) => {
      assert.equal(JSON.parse(error.stdout).status, "error");
      assert.ok(!error.stdout.includes("never-store-this"));
      return true;
    },
  );
  await assert.rejects(
    readFile(join(platform.environment.HEX_CONFIG_DIR, "profiles.json")),
    { code: "ENOENT" },
  );
});

test("terminal-dropped paths are parsed as data, never shell commands", () => {
  assert.equal(droppedFilePath("'/tmp/a b.json'"), "/tmp/a b.json");
  assert.equal(droppedFilePath('"/tmp/a b.json"'), "/tmp/a b.json");
  assert.equal(droppedFilePath("/tmp/a\\ b.json "), "/tmp/a b.json");
  assert.equal(
    droppedFilePath("'/tmp/user'\\''s file.json'"),
    "/tmp/user's file.json",
  );
  assert.equal(
    droppedFilePath("& 'C:\\Users\\Alex\\Downloads\\hex config.json'"),
    "C:\\Users\\Alex\\Downloads\\hex config.json",
  );
  assert.equal(droppedFilePath("file:///tmp/a%20b.json"), "/tmp/a b.json");
  assert.equal(
    droppedFilePath("$(touch /tmp/never-execute).json"),
    "$(touch /tmp/never-execute).json",
  );
  assert.throws(() => droppedFilePath(""), /Drop or paste/);
});

test(
  "hex login delegates to AzCopy without requesting Hex tokens",
  { skip: process.platform === "win32" },
  async (t) => {
    const platform = await fixture(t);
    const document = {
      ...platform.connection,
      publishing: {
        provider: "azure-files",
        url: "https://example.file.core.windows.net/sites/public/sites",
      },
    };
    const file = join(platform.directory, "azure.json");
    await writeFile(file, JSON.stringify(document));
    await platform.invoke([
      "setup",
      "--file",
      file,
      "--name",
      "company",
      "--json",
    ]);
    const tools = join(platform.directory, "bin");
    await mkdir(tools);
    await writeFile(
      join(tools, "azcopy"),
      "#!/usr/bin/env node\nconsole.log(JSON.stringify(process.argv.slice(2)));\n",
      { mode: 0o755 },
    );
    const result = await exec(
      process.execPath,
      [cli, "login", "--platform", "company"],
      {
        cwd: platform.directory,
        env: {
          ...platform.environment,
          PATH: tools + delimiter + process.env.PATH,
        },
      },
    );
    assert.match(result.stdout, /\["login"\]/);
    assert.equal(platform.requests(), 0);
  },
);

#!/usr/bin/env node
import { readFile, writeFile, mkdir, readdir, lstat, realpath } from "node:fs/promises";
import { resolve, join, relative, sep } from "node:path";
import { fileURLToPath, pathToFileURL } from "node:url";
import { parseArgs } from "node:util";
import { execFileSync } from "node:child_process";
import { zipSync } from "fflate";

const validName = value => /^[a-zA-Z0-9][a-zA-Z0-9_-]{0,63}$/.test(value);

export async function archiveDirectory(directory, maxBytes) {
  const files = Object.create(null);
  let size = 0;
  let count = 0;
  async function walk(path, prefix = "") {
    for (const entry of await readdir(path, { withFileTypes: true })) {
      if (entry.name.startsWith(".") || entry.name === "node_modules") continue;
      const full = join(path, entry.name);
      const key = prefix + entry.name;
      if (entry.isSymbolicLink()) throw new Error(`Symlinks cannot be published: ${key}`);
      if (entry.isDirectory()) { await walk(full, key + "/"); continue; }
      if (!entry.isFile()) throw new Error(`Not a regular file: ${key}`);
      if (++count > 5000) throw new Error("Maximum 5000 files per deployment");
      const stat = await lstat(full);
      size += stat.size;
      if (size > maxBytes) throw new Error("Site exceeds server upload limit");
      files[key] = new Uint8Array(await readFile(full));
    }
  }
  await walk(directory);
  if (!files["index.html"]) throw new Error("Publish directory must contain index.html");
  const archive = zipSync(files);
  if (archive.byteLength > maxBytes) throw new Error("ZIP exceeds server upload limit");
  return archive;
}

async function installSkills(project) {
  const source = new URL("../skills/hex/SKILL.md", import.meta.url);
  const directory = join(project, ".agents/skills/hex");
  await mkdir(directory, { recursive: true });
  await writeFile(join(directory, "SKILL.md"), await readFile(source));
}

async function initialize(directory, values) {
  const project = resolve(directory ?? ".");
  const name = values.name ?? project.split(sep).at(-1);
  if (!validName(name)) throw new Error("Use --name with 1–64 letters, digits, underscores or hyphens, starting with a letter or digit");
  const server = new URL(values.server ?? "http://localhost:8080");
  if (!["https:", "http:"].includes(server.protocol) || server.username || server.password || server.pathname !== "/" || server.search || server.hash) throw new Error("Server must be an HTTP(S) origin");
  await mkdir(project, { recursive: true });
  await writeFile(join(project, "hex.json"), JSON.stringify({ name, server: server.origin, directory: "public", ...(values.resource ? { resource: values.resource } : {}) }, null, 2) + "\n", { flag: "wx" });
  await mkdir(join(project, "public"), { recursive: true });
  const client = await readFile(fileURLToPath(import.meta.resolve("@hex-platform/client")));
  const templates = {
    "hex-client.js": client,
    "index.html": '<!doctype html>\n<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>Hex app</title></head><body><h1>Welcome to Hex</h1><pre id="status">Connecting…</pre><script type="module" src="./app.js"></script></body></html>\n',
    "app.js": `import { createHexClient } from './hex-client.js';\n\nconst hex = createHexClient({ site: ${JSON.stringify(name)} });\nconst status = document.querySelector('#status');\ntry {\n  status.textContent = JSON.stringify(await hex.capabilities(), null, 2);\n} catch (error) {\n  status.textContent = error.message;\n}\n`,
  };
  for (const [file, content] of Object.entries(templates)) {
    try { await writeFile(join(project, "public", file), content, { flag: "wx" }); }
    catch (error) { if (error.code !== "EEXIST") throw error; }
  }
  await installSkills(project);
  console.log(`Initialized ${name}. Agent skill: .agents/skills/hex/SKILL.md`);
}

export async function main(args = process.argv.slice(2)) {
  const { positionals, values } = parseArgs({ args, allowPositionals: true, options: {
    server: { type: "string" }, name: { type: "string" }, resource: { type: "string" }, yes: { type: "boolean" }, help: { type: "boolean" },
  } });
  const [command, argument] = positionals;
  if (values.help || !command) {
    console.log("hex init [directory] [--name name] [--server URL] [--resource ENTRA_APP_ID_URI]\nhex publish\nhex sites\nhex delete [site] --yes\nhex capabilities\nhex skills\n\nRun management commands in a project containing hex.json. For Azure, supply HEX_TOKEN or configure resource and sign in with az login.");
    return;
  }
  if (command === "init") return initialize(argument, values);
  if (command === "skills") { await installSkills(process.cwd()); console.log("Installed .agents/skills/hex/SKILL.md"); return; }
  if (!["publish", "sites", "delete", "capabilities"].includes(command)) throw new Error(`Unknown command: ${command}`);
  const config = JSON.parse(await readFile("hex.json", "utf8"));
  const server = new URL(values.server ?? config.server);
  if (!["https:", "http:"].includes(server.protocol) || server.username || server.password || server.pathname !== "/" || server.search || server.hash) throw new Error("Server must be an HTTP(S) origin");
  let token = process.env.HEX_TOKEN;
  const resource = values.resource ?? config.resource;
  if (!token && resource) {
    token = execFileSync("az", ["account", "get-access-token", "--resource", resource, "--query", "accessToken", "-o", "tsv"], { encoding: "utf8", stdio: ["ignore", "pipe", "inherit"] }).trim();
  }
  async function request(path, method = "GET", body) {
    const headers = { "X-Hex-Request": "1" };
    if (token) headers.Authorization = `Bearer ${token}`;
    if (body) headers["Content-Type"] = "application/zip";
    const response = await fetch(new URL(path, server), { method, headers, body, redirect: "error" });
    if (!response.ok) throw new Error(`Hex ${response.status}: ${await response.text()}`);
    if (response.status === 204) return;
    if (!response.headers.get("content-type")?.includes("application/json")) throw new Error("Expected Hex JSON response. Check server URL and Azure authentication.");
    return response.json();
  }
  if (command === "capabilities") { console.log(JSON.stringify(await request("/api/hex/capabilities"), null, 2)); return; }
  if (command === "sites") { console.log(JSON.stringify(await request("/api/sites"), null, 2)); return; }
  const name = argument ?? config.name;
  if (!validName(name)) throw new Error("Invalid site name");
  if (command === "delete") {
    if (!values.yes) throw new Error("Use --yes to unpublish this site");
    await request(`/api/sites/${name}`, "DELETE");
    console.log(`Unpublished ${name}`);
    return;
  }
  const capabilities = await request("/api/hex/capabilities");
  if (!capabilities.sites) throw new Error("Site publishing is disabled on this server");
  const project = await realpath(process.cwd());
  const directory = await realpath(resolve(config.directory));
  const within = relative(project, directory);
  if (!within || within.startsWith(".." + sep) || within === ".." || resolve(project, within) !== directory) throw new Error("Publish directory must be a subdirectory of the project");
  const archive = await archiveDirectory(directory, capabilities.maxUploadBytes);
  const site = await request(`/api/sites/${name}/deploy`, "POST", archive);
  console.log(new URL(site.url, server).href);
}

if (process.argv[1] && import.meta.url === pathToFileURL(await realpath(process.argv[1])).href) {
  main().catch(error => { console.error(error.message); process.exitCode = 1; });
}

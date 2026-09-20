import { execFile, spawn } from "node:child_process";
import { promisify } from "node:util";
import { mkdir, readFile, writeFile } from "node:fs/promises";
import { join, dirname } from "node:path";
import { once } from "node:events";

const exec = promisify(execFile);

function quote(value) {
  if (/[\r\n\0$]/.test(value)) throw new Error("Unsupported NGINX configuration path");
  return '"' + value.replaceAll("\\", "\\\\").replaceAll('"', '\\"') + '"';
}

export async function startNginx({ directory, sitesDirectory, port = 8080, backendPort = 8081 }) {
  const binary = process.env.NGINX_BIN ?? "nginx";
  let version;
  try { version = await exec(binary, ["-V"]); }
  catch { throw new Error("NGINX is required. Install nginx or set NGINX_BIN to its executable."); }
  const prefix = version.stderr.match(/--prefix=([^\s]+)/)?.[1] ?? "/usr/local/nginx";
  const configPath = version.stderr.match(/--conf-path=([^\s]+)/)?.[1] ?? join(prefix, "conf/nginx.conf");
  const mimeTypes = process.env.NGINX_MIME_TYPES ?? join(dirname(configPath), "mime.types");
  await mkdir(join(directory, "logs"), { recursive: true });
  const template = await readFile(new URL("../deploy/nginx/nginx.conf", import.meta.url), "utf8");
  const config = `daemon off;\npid ${quote(join(directory, "nginx.pid"))};\nerror_log stderr;\n` + template
    .replace("user nginx;", "")
    .replace("include /etc/nginx/mime.types;", `include ${quote(mimeTypes)};`)
    .replace("listen 8080;", `listen 127.0.0.1:${port};`)
    .replace("http://127.0.0.1:8081", `http://127.0.0.1:${backendPort}`)
    .replace("root /mnt/sites/public;", `root ${quote(join(sitesDirectory, "public"))};`)
    .replace("http {", "http {\n    access_log off;");
  const file = join(directory, "nginx.conf");
  await writeFile(file, config);
  await exec(binary, ["-t", "-p", directory + "/", "-c", file]);
  return spawn(binary, ["-p", directory + "/", "-c", file], { stdio: ["ignore", "ignore", "inherit"] });
}

export async function waitForHTTP(url, child) {
  for (let attempt = 0; attempt < 100; attempt++) {
    if (child.exitCode !== null || child.signalCode !== null) throw new Error(`Process exited while waiting for ${url}`);
    try { if ((await fetch(url)).ok) return; } catch {}
    await new Promise(resolve => setTimeout(resolve, 50));
  }
  throw new Error(`Timed out waiting for ${url}`);
}

export async function stopProcess(child) {
  if (!child?.pid || child.exitCode !== null || child.signalCode !== null) return;
  const exited = once(child, "exit");
  child.kill("SIGTERM");
  await exited;
}

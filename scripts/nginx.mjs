import { execFile, spawn } from "node:child_process";
import { promisify } from "node:util";
import { mkdir, readFile, writeFile } from "node:fs/promises";
import { join, dirname } from "node:path";
import { once } from "node:events";

const exec = promisify(execFile);

function quote(value) {
  if (/[\r\n\0$]/.test(value)) {
    throw new Error("Unsupported NGINX configuration path");
  }

  const escaped = value.replaceAll("\\", "\\\\").replaceAll('"', '\\"');
  return `"${escaped}"`;
}

async function locateMimeTypes(binary) {
  let version;
  try {
    version = await exec(binary, ["-V"]);
  } catch (cause) {
    throw new Error(
      "NGINX is required. Install nginx or set NGINX_BIN to its executable.",
      { cause },
    );
  }

  const prefix =
    version.stderr.match(/--prefix=([^\s]+)/)?.[1] ?? "/usr/local/nginx";
  const configPath =
    version.stderr.match(/--conf-path=([^\s]+)/)?.[1] ??
    join(prefix, "conf/nginx.conf");
  return (
    process.env.NGINX_MIME_TYPES ?? join(dirname(configPath), "mime.types")
  );
}

export async function startNginx({
  directory,
  sitesDirectory,
  port = 8080,
  backendPort = 8081,
}) {
  const binary = process.env.NGINX_BIN ?? "nginx";
  const mimeTypes = await locateMimeTypes(binary);
  await mkdir(join(directory, "logs"), { recursive: true });

  const template = await readFile(
    new URL("../deploy/nginx/nginx.conf", import.meta.url),
    "utf8",
  );
  const processSettings = [
    "daemon off;",
    `pid ${quote(join(directory, "nginx.pid"))};`,
    "error_log stderr;",
    "",
  ].join("\n");
  const configuration = template
    .replace("user nginx;", "")
    .replace("include /etc/nginx/mime.types;", `include ${quote(mimeTypes)};`)
    .replace("listen 8080;", `listen 127.0.0.1:${port};`)
    .replace("http://127.0.0.1:8081", `http://127.0.0.1:${backendPort}`)
    .replace(
      "root /mnt/sites/public;",
      `root ${quote(join(sitesDirectory, "public"))};`,
    )
    .replace("http {", "http {\n    access_log off;");
  const file = join(directory, "nginx.conf");
  await writeFile(file, processSettings + configuration);
  await exec(binary, ["-t", "-p", directory + "/", "-c", file]);

  return spawn(binary, ["-p", directory + "/", "-c", file], {
    stdio: ["ignore", "ignore", "inherit"],
  });
}

export async function waitForHTTP(url, child) {
  let lastError;

  for (let attempt = 0; attempt < 100; attempt++) {
    if (child.exitCode !== null || child.signalCode !== null) {
      throw new Error(`Process exited while waiting for ${url}`, {
        cause: lastError,
      });
    }

    try {
      const response = await fetch(url);
      if (response.ok) {
        return;
      }
      lastError = new Error(`Health check returned HTTP ${response.status}`);
    } catch (error) {
      lastError = error;
    }

    await new Promise((resolve) => setTimeout(resolve, 50));
  }

  throw new Error(`Timed out waiting for ${url}`, { cause: lastError });
}

export async function stopProcess(child) {
  if (!child?.pid || child.exitCode !== null || child.signalCode !== null) {
    return;
  }

  const exited = once(child, "exit");
  child.kill("SIGTERM");
  await exited;
}

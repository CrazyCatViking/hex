import { execFile } from "node:child_process";
import { promisify } from "node:util";
import { mkdir, readFile, writeFile } from "node:fs/promises";
import { join, dirname } from "node:path";
import { startProcess } from "./processes.mjs";

export { waitForHTTP, stopProcess } from "./processes.mjs";

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
  siteDomain = "localhost",
}) {
  if (!/^[a-z0-9.-]+$/.test(siteDomain)) {
    throw new Error("Invalid site domain");
  }
  const binary = process.env.NGINX_BIN ?? "nginx";
  const mimeTypes = await locateMimeTypes(binary);
  await mkdir(join(directory, "logs"), { recursive: true });

  const template = await readFile(
    new URL("../internal/cli/assets/nginx.conf.template", import.meta.url),
    "utf8",
  );
  const processSettings = [
    "daemon off;",
    `pid ${quote(join(directory, "nginx.pid"))};`,
    "error_log stderr;",
    "lock_file logs/nginx.lock;",
    "",
  ].join("\n");
  const configuration = template
    .replaceAll("${HEX_SITE_DOMAIN_PATTERN}", siteDomain.replaceAll(".", "\\."))
    .replace("user nginx;", "")
    .replace("include /etc/nginx/mime.types;", `include ${quote(mimeTypes)};`)
    .replace("listen 8080;", `listen 127.0.0.1:${port};`)
    .replaceAll("http://127.0.0.1:8081", `http://127.0.0.1:${backendPort}`)
    .replace(
      "root /mnt/sites/public/sites/$hex_site;",
      `root ${quote(join(sitesDirectory, "public/sites", "__HEX_SITE__")).replace("__HEX_SITE__", "$hex_site")};`,
    )
    .replace(
      "http {",
      `http {
    access_log off;
    client_body_temp_path client-body;
    proxy_temp_path proxy;
    fastcgi_temp_path fastcgi;
    scgi_temp_path scgi;
    uwsgi_temp_path uwsgi;`,
    );
  const file = join(directory, "nginx.conf");
  await writeFile(file, processSettings + configuration);
  await exec(binary, ["-t", "-p", directory + "/", "-c", file]);

  return startProcess(binary, ["-p", directory + "/", "-c", file], {
    stdio: ["ignore", "ignore", "inherit"],
  });
}

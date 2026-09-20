import { execFile, spawn } from "node:child_process";
import { promisify } from "node:util";
import { mkdtemp, mkdir, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { startNginx, waitForHTTP, stopProcess } from "./nginx.mjs";

const root = fileURLToPath(new URL("../", import.meta.url));
const directory = await mkdtemp(join(tmpdir(), "hex-dev-"));
const sites = resolve(process.env.HEX_SITES_DIR ?? join(root, ".hex-data/sites"));
let server;
let nginx;
try {
  await mkdir(sites, { recursive: true });
  const binary = join(directory, "hex-server");
  await promisify(execFile)("go", ["build", "-o", binary, "./cmd/hex-server"], { cwd: root });
  server = spawn(binary, [], { cwd: root, env: { ...process.env, HEX_ADDR: "127.0.0.1:8081", HEX_SITES_DIR: sites }, stdio: "inherit" });
  await waitForHTTP("http://127.0.0.1:8081/api/hex/capabilities", server);
  nginx = await startNginx({ directory: join(directory, "nginx"), sitesDirectory: sites });
  await waitForHTTP("http://127.0.0.1:8080/healthz", nginx);
  console.log("Hex gateway: http://localhost:8080 (NGINX static sites; Go API on loopback :8081)");
  await new Promise(resolve => {
    process.once("SIGINT", resolve);
    process.once("SIGTERM", resolve);
    server.once("exit", resolve);
    nginx.once("exit", resolve);
  });
} finally {
  await stopProcess(nginx);
  await stopProcess(server);
  await rm(directory, { recursive: true, force: true });
}

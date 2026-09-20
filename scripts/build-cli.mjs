import { execFile } from "node:child_process";
import { promisify } from "node:util";
import { join } from "node:path";
import { fileURLToPath } from "node:url";

export async function buildCLI(directory) {
  const root = fileURLToPath(new URL("../", import.meta.url));
  const binary = join(
    directory,
    process.platform === "win32" ? "hex.exe" : "hex",
  );
  await promisify(execFile)("go", ["build", "-o", binary, "./cmd/hex"], {
    cwd: root,
  });
  return binary;
}

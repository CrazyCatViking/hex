import { lstat, readdir, readFile } from "node:fs/promises";
import { join } from "node:path";
import { zipSync } from "fflate";

export async function archiveDirectory(directory, maxBytes) {
  const files = Object.create(null);
  let totalSize = 0;
  let fileCount = 0;

  async function collectFiles(currentDirectory, prefix = "") {
    const entries = await readdir(currentDirectory, { withFileTypes: true });

    for (const entry of entries) {
      if (entry.name.startsWith(".") || entry.name === "node_modules") {
        continue;
      }

      const filePath = join(currentDirectory, entry.name);
      const key = prefix + entry.name;
      if (entry.isSymbolicLink()) {
        throw new Error(`Symlinks cannot be published: ${key}`);
      }

      if (entry.isDirectory()) {
        await collectFiles(filePath, key + "/");
        continue;
      }

      if (!entry.isFile()) {
        throw new Error(`Not a regular file: ${key}`);
      }

      fileCount += 1;
      if (fileCount > 5000) {
        throw new Error("Maximum 5000 files per deployment");
      }

      const stat = await lstat(filePath);
      totalSize += stat.size;
      if (totalSize > maxBytes) {
        throw new Error("Site exceeds server upload limit");
      }

      files[key] = new Uint8Array(await readFile(filePath));
    }
  }

  await collectFiles(directory);
  if (!files["index.html"]) {
    throw new Error("Publish directory must contain index.html");
  }

  const archive = zipSync(files);
  if (archive.byteLength > maxBytes) {
    throw new Error("ZIP exceeds server upload limit");
  }

  return archive;
}

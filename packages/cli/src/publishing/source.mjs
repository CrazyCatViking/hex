import { readdir, realpath } from "node:fs/promises";
import { join, relative, resolve, sep, posix } from "node:path";

export function containsPath(parent, child) {
  const path = relative(parent, child);
  return (
    path === "" ||
    (path !== ".." &&
      !path.startsWith(".." + sep) &&
      resolve(parent, path) === child)
  );
}

export async function readSource(directory) {
  const project = await realpath(process.cwd());
  const source = await realpath(resolve(directory));
  if (project === source || !containsPath(project, source)) {
    throw new Error("Publish directory must be a subdirectory of the project");
  }

  const files = [];
  async function collect(current, prefix = "") {
    for (const entry of await readdir(current, { withFileTypes: true })) {
      if (entry.name.startsWith(".") || entry.name === "node_modules") {
        continue;
      }
      const key = posix.join(prefix, entry.name);
      const path = join(current, entry.name);
      if (entry.isSymbolicLink()) {
        throw new Error(`Symlinks cannot be published: ${key}`);
      }
      if (entry.name.includes("\\")) {
        throw new Error(`Unsupported filename: ${key}`);
      }
      if (entry.isDirectory()) {
        await collect(path, key);
        continue;
      }
      if (!entry.isFile()) {
        throw new Error(`Not a regular file: ${key}`);
      }
      files.push({ key, path });
    }
  }

  await collect(source);
  if (!files.some((file) => file.key === "index.html")) {
    throw new Error("Publish directory must contain index.html");
  }
  files.sort((left, right) => {
    if (left.key === right.key) {
      return 0;
    }
    if (left.key === "index.html") {
      return 1;
    }
    if (right.key === "index.html") {
      return -1;
    }
    return left.key.localeCompare(right.key);
  });

  return { directory: source, files };
}

import { randomUUID } from "node:crypto";
import {
  copyFile,
  lstat,
  mkdir,
  readdir,
  realpath,
  rename,
  rm,
} from "node:fs/promises";
import { dirname, join, posix, resolve } from "node:path";
import { containsPath } from "./source.mjs";
import { validateSiteName } from "../project.mjs";

async function inspect(path) {
  try {
    return await lstat(path);
  } catch (error) {
    if (error.code === "ENOENT") {
      return null;
    }
    throw error;
  }
}

async function inspectDestination(directory) {
  const entries = [];
  async function walk(path, prefix = "") {
    for (const entry of await readdir(path, { withFileTypes: true })) {
      const key = posix.join(prefix, entry.name);
      const fullPath = join(path, entry.name);
      if (entry.isSymbolicLink()) {
        throw new Error(`Publishing destination contains a symlink: ${key}`);
      }
      if (!entry.isFile() && !entry.isDirectory()) {
        throw new Error(`Unsupported publishing destination entry: ${key}`);
      }
      entries.push({ key, path: fullPath, directory: entry.isDirectory() });
      if (entry.isDirectory()) {
        await walk(fullPath, key);
      }
    }
  }
  await walk(directory);
  return entries;
}

export function filesystemPublisher(settings) {
  if (typeof settings.root !== "string" || !settings.root) {
    throw new Error("Filesystem publishing requires publishing.root");
  }

  async function destination(name, createRoot) {
    validateSiteName(name);
    const configuredRoot = resolve(settings.root);
    if (createRoot) {
      await mkdir(configuredRoot, { recursive: true });
    }
    const root = await realpath(configuredRoot);
    const path = join(root, name);
    const info = await inspect(path);
    if (info && (!info.isDirectory() || info.isSymbolicLink())) {
      throw new Error(`Site destination must be a real directory: ${path}`);
    }
    return { path, exists: info !== null };
  }

  return {
    async publish(name, source) {
      const target = await destination(name, true);
      if (
        containsPath(source.directory, target.path) ||
        containsPath(target.path, source.directory)
      ) {
        throw new Error("Source and destination directories must not overlap");
      }

      await mkdir(target.path, { recursive: true });
      const previous = await inspectDestination(target.path);
      const wantedFiles = new Set(source.files.map((file) => file.key));
      const wantedDirectories = new Set();
      for (const file of source.files) {
        for (
          let parent = posix.dirname(file.key);
          parent !== ".";
          parent = posix.dirname(parent)
        ) {
          wantedDirectories.add(parent);
        }
      }

      for (const entry of previous.reverse()) {
        const keep = entry.directory
          ? wantedDirectories.has(entry.key)
          : wantedFiles.has(entry.key);
        if (!keep) {
          await rm(entry.path, { recursive: entry.directory, force: true });
        }
      }

      for (const file of source.files) {
        const path = join(target.path, ...file.key.split("/"));
        await mkdir(dirname(path), { recursive: true });
        const temporaryPath = join(dirname(path), `.hex-${randomUUID()}`);
        try {
          await copyFile(file.path, temporaryPath);
          await rename(temporaryPath, path);
        } finally {
          await rm(temporaryPath, { force: true });
        }
      }
    },

    async delete(name) {
      const target = await destination(name, false);
      if (!target.exists) {
        throw new Error(`Site does not exist: ${name}`);
      }
      await rm(target.path, { recursive: true });
    },
  };
}

import { open } from "node:fs/promises";
import { homedir } from "node:os";
import { join, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { maxConfigBytes, validateConnection } from "../connection.mjs";

export function droppedFilePath(input) {
  let value = input.trim();
  if (value.startsWith("& ")) {
    value = value.slice(2).trim();
  }
  if (value.startsWith("$'") && value.endsWith("'")) {
    value = value.slice(2, -1).replace(/\\(['\\])/g, "$1");
  } else if (value.startsWith("'") && value.endsWith("'")) {
    value = value.slice(1, -1).replace(/'\\''/g, "'");
  } else if (value.startsWith('"') && value.endsWith('"')) {
    value = value.slice(1, -1);
  } else if (!/^[a-z]:[\\/]/i.test(value) && !value.startsWith("\\\\")) {
    value = value.replace(/\\([\s'"()&;\\])/g, "$1");
  }
  if (value.startsWith("file:")) {
    value = fileURLToPath(value);
  }
  if (value.startsWith("~/")) {
    value = join(homedir(), value.slice(2));
  }
  if (!value || /[\x00-\x1f\x7f]/.test(value)) {
    throw new Error("Drop or paste the path to one downloaded JSON file");
  }
  return value;
}

export async function readConnectionFile(input, expectedServer) {
  const path = resolve(droppedFilePath(input));
  const file = await open(path, "r");
  try {
    const info = await file.stat();
    if (!info.isFile() || info.size > maxConfigBytes) {
      throw new Error(
        "Connection settings must be a JSON file of at most 64 KiB",
      );
    }
    const data = await file.readFile("utf8");
    return validateConnection(
      JSON.parse(data.replace(/^\uFEFF/, "")),
      expectedServer,
    );
  } finally {
    await file.close();
  }
}

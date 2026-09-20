import { randomUUID } from "node:crypto";
import { mkdir, readFile, rename, rm, writeFile } from "node:fs/promises";
import { homedir } from "node:os";
import { join } from "node:path";
import { validateConnection } from "./connection.mjs";

export function profileDirectory() {
  if (process.env.HEX_CONFIG_DIR) {
    return process.env.HEX_CONFIG_DIR;
  }
  const base =
    process.env.XDG_CONFIG_HOME ??
    (process.platform === "win32" ? process.env.LOCALAPPDATA : undefined) ??
    join(homedir(), ".config");
  return join(base, "hex");
}

function validateProfileName(name) {
  if (
    typeof name !== "string" ||
    !/^[a-z0-9][a-z0-9.-]{0,127}$/.test(name) ||
    name === "__proto__"
  ) {
    throw new Error(
      "Profile names must contain lowercase letters, digits, dots or hyphens",
    );
  }
}

async function readProfiles(directory) {
  try {
    const document = JSON.parse(
      await readFile(join(directory, "profiles.json"), "utf8"),
    );
    if (
      document.version !== 1 ||
      !document.profiles ||
      typeof document.profiles !== "object" ||
      Array.isArray(document.profiles)
    ) {
      throw new Error("Invalid profile store");
    }
    return document;
  } catch (cause) {
    if (cause.code === "ENOENT") {
      return { version: 1, profiles: {} };
    }
    throw new Error(`Read Hex profiles: ${cause.message}`, { cause });
  }
}

export async function loadProfile(
  name,
  { optional = false, directory = profileDirectory() } = {},
) {
  const store = await readProfiles(directory);
  const selected = name ?? store.defaultProfile;
  if (!selected) {
    if (optional) {
      return null;
    }
    throw new Error("No platform configured. Run hex setup first.");
  }
  validateProfileName(selected);
  if (!Object.hasOwn(store.profiles, selected)) {
    throw new Error(
      `Unknown platform profile: ${selected}. Run hex setup to configure it.`,
    );
  }
  return {
    name: selected,
    connection: validateConnection(store.profiles[selected]),
  };
}

export async function saveProfile(
  connection,
  name,
  directory = profileDirectory(),
) {
  connection = validateConnection(connection);
  const url = new URL(connection.server);
  const hostname = url.hostname
    .replace(/[\[\]:]/g, "-")
    .replace(/^-+|-+$/g, "");
  const derived = `${hostname}${url.port ? "-" + url.port : ""}`;
  const selected = name ?? derived;
  validateProfileName(selected);

  const store = await readProfiles(directory);
  store.profiles[selected] = connection;
  store.defaultProfile = selected;
  await mkdir(directory, { recursive: true, mode: 0o700 });
  const temporary = join(directory, `.profiles-${randomUUID()}.json`);
  try {
    await writeFile(temporary, JSON.stringify(store, null, 2) + "\n", {
      flag: "wx",
      mode: 0o600,
    });
    await rename(temporary, join(directory, "profiles.json"));
  } finally {
    await rm(temporary, { force: true });
  }
  return selected;
}

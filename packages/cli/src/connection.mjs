import { isIP } from "node:net";
import { isAbsolute, win32 } from "node:path";

export const connectionPath = "/api/hex/config";
export const maxConfigBytes = 64 * 1024;

export function isLocalHost(hostname) {
  const host = hostname.replace(/^\[|\]$/g, "");
  return (
    host === "localhost" ||
    host.endsWith(".localhost") ||
    host === "::1" ||
    (isIP(host) === 4 && host.startsWith("127."))
  );
}

export function platformOrigin(value) {
  if (typeof value !== "string" || !value.trim()) {
    throw new Error("A platform URL is required");
  }
  const url = new URL(value);
  const validScheme =
    url.protocol === "https:" ||
    (url.protocol === "http:" && isLocalHost(url.hostname));
  if (
    !validScheme ||
    url.username ||
    url.password ||
    url.search ||
    url.hash ||
    url.pathname !== "/"
  ) {
    throw new Error(
      "Use an HTTPS platform origin, or HTTP on localhost, without credentials, paths or query parameters",
    );
  }
  return url.origin;
}

function object(value, description) {
  if (!value || typeof value !== "object" || Array.isArray(value)) {
    throw new Error(`${description} must be an object`);
  }
}

function knownFields(value, allowed, description) {
  object(value, description);
  if (Object.keys(value).some((key) => !allowed.includes(key))) {
    throw new Error(
      `${description} contains unsupported fields; connection files must not contain credentials or commands`,
    );
  }
}

function validatePublishing(value, server) {
  if (value === undefined || value === null) {
    return undefined;
  }
  knownFields(value, ["provider", "root", "url"], "Publishing configuration");

  if (value.provider === "filesystem") {
    if (!isLocalHost(new URL(server).hostname)) {
      throw new Error(
        "Filesystem publishing is only supported for local platforms",
      );
    }
    if (
      typeof value.root !== "string" ||
      (!isAbsolute(value.root) && !win32.isAbsolute(value.root)) ||
      value.url !== undefined
    ) {
      throw new Error(
        "Filesystem publishing requires an absolute root and no URL",
      );
    }
    return { provider: "filesystem", root: value.root };
  }

  if (value.provider === "azure-files") {
    if (typeof value.url !== "string") {
      throw new Error("Azure Files publishing requires a URL");
    }
    const url = new URL(value.url);
    if (
      url.protocol !== "https:" ||
      url.username ||
      url.password ||
      url.search ||
      url.hash ||
      url.pathname === "/" ||
      value.root !== undefined
    ) {
      throw new Error(
        "Azure Files publishing requires an HTTPS share URL without credentials, query parameters or a filesystem root",
      );
    }
    return { provider: "azure-files", url: url.href.replace(/\/$/, "") };
  }

  throw new Error("Unsupported publishing provider in connection file");
}

export function validateConnection(value, expectedServer) {
  knownFields(
    value,
    ["version", "name", "server", "siteBaseURL", "publishing", "capabilities"],
    "Connection file",
  );
  if (value.version !== 1) {
    throw new Error("Unsupported connection file version; expected version 1");
  }
  if (
    typeof value.name !== "string" ||
    !value.name.trim() ||
    /[\x00-\x1f\x7f]/.test(value.name)
  ) {
    throw new Error("Connection file requires a printable platform name");
  }

  const server = platformOrigin(value.server);
  if (expectedServer) {
    const expected = new URL(platformOrigin(expectedServer));
    const actual = new URL(server);
    const sameLocalOrigin =
      isLocalHost(expected.hostname) &&
      isLocalHost(actual.hostname) &&
      expected.port === actual.port &&
      expected.protocol === actual.protocol;
    if (expected.origin !== actual.origin && !sameLocalOrigin) {
      throw new Error("Connection file belongs to a different platform URL");
    }
  }

  const siteBaseURL = platformOrigin(value.siteBaseURL);
  const hostname = new URL(siteBaseURL).hostname;
  if (
    isIP(hostname) ||
    hostname.startsWith("[") ||
    hostname
      .split(".")
      .some((label) => !/^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?$/.test(label))
  ) {
    throw new Error("siteBaseURL requires a DNS hostname for site subdomains");
  }

  const capabilities = value.capabilities;
  knownFields(
    capabilities,
    ["version", "sites", "files", "database", "realtime", "maxUploadBytes"],
    "Capabilities",
  );
  if (
    capabilities.version !== 1 ||
    ["sites", "files", "database", "realtime"].some(
      (key) => typeof capabilities[key] !== "boolean",
    ) ||
    !Number.isSafeInteger(capabilities.maxUploadBytes) ||
    capabilities.maxUploadBytes < 1
  ) {
    throw new Error("Invalid capability description");
  }

  return {
    version: 1,
    name: value.name.trim(),
    server,
    siteBaseURL,
    publishing: validatePublishing(value.publishing, server),
    capabilities: { ...capabilities },
  };
}

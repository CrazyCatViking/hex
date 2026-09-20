import { execFileSync } from "node:child_process";
import { parseServerOrigin } from "./project.mjs";

function accessToken(resource) {
  if (process.env.HEX_TOKEN) {
    return process.env.HEX_TOKEN;
  }
  if (!resource) {
    return undefined;
  }

  const args = [
    "account",
    "get-access-token",
    "--resource",
    resource,
    "--query",
    "accessToken",
    "-o",
    "tsv",
  ];

  try {
    return execFileSync("az", args, {
      encoding: "utf8",
      stdio: ["ignore", "pipe", "inherit"],
    }).trim();
  } catch (error) {
    throw new Error(
      "Could not obtain an Azure access token. Check az login and the configured resource.",
      {
        cause: error,
      },
    );
  }
}

export function createAPI(config, options) {
  const server = parseServerOrigin(options.server ?? config.server);
  const token = accessToken(options.resource ?? config.resource);

  async function request(path, method = "GET", body) {
    const headers = { "X-Hex-Request": "1" };
    if (token) {
      headers.Authorization = `Bearer ${token}`;
    }
    if (body) {
      headers["Content-Type"] = "application/zip";
    }

    const response = await fetch(new URL(path, server), {
      method,
      headers,
      body,
      redirect: "manual",
    });

    if ([301, 302, 303, 307, 308, 401, 403].includes(response.status)) {
      await response.body?.cancel();
      throw new Error(
        `Gateway access requires authentication or permission (HTTP ${response.status}). Open ${server.origin} in your browser, or supply HEX_TOKEN for programmatic API access. hex login authenticates publishing storage only.`,
      );
    }

    if (!response.ok) {
      throw new Error(`Hex ${response.status}: ${await response.text()}`);
    }
    if (response.status === 204) {
      return;
    }
    if (!response.headers.get("content-type")?.includes("application/json")) {
      throw new Error(
        "Expected Hex JSON response. Check server URL and hosting authentication.",
      );
    }

    return response.json();
  }

  return { server, request };
}

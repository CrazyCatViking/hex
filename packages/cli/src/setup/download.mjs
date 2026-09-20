import {
  connectionPath,
  maxConfigBytes,
  platformOrigin,
  validateConnection,
} from "../connection.mjs";

export async function downloadConnection(server, fetchConfig = fetch) {
  const origin = platformOrigin(server);
  const downloadURL = new URL(connectionPath, origin).href;
  let response;
  try {
    response = await fetchConfig(downloadURL, {
      redirect: "manual",
      headers: { Accept: "application/json" },
      signal: AbortSignal.timeout(15000),
    });
  } catch (cause) {
    throw new Error(
      `Could not reach ${downloadURL}. Check the URL and network/VPN connection.`,
      { cause },
    );
  }

  const loginResponse = [301, 302, 303, 307, 308, 401, 403].includes(
    response.status,
  );
  const contentType = response.headers
    .get("content-type")
    ?.split(";")[0]
    .trim()
    .toLowerCase();
  if (loginResponse || (response.ok && contentType === "text/html")) {
    await response.body?.cancel();
    return {
      status: "download_required",
      downloadURL,
      reason: "browser_authentication",
    };
  }
  if (!response.ok) {
    await response.body?.cancel();
    throw new Error(
      response.status === 404
        ? "This platform does not expose connection settings at /api/hex/config. Ask its operator to configure them."
        : `Platform configuration download failed with HTTP ${response.status}`,
    );
  }
  if (contentType !== "application/json" && !contentType?.endsWith("+json")) {
    await response.body?.cancel();
    throw new Error(
      "Expected JSON connection settings, not the platform home page",
    );
  }

  const chunks = [];
  let size = 0;
  for await (const chunk of response.body) {
    size += chunk.length;
    if (size > maxConfigBytes) {
      throw new Error("Platform connection settings exceed 64 KiB");
    }
    chunks.push(chunk);
  }
  const text = Buffer.concat(chunks)
    .toString("utf8")
    .replace(/^\uFEFF/, "");
  let document;
  try {
    document = JSON.parse(text);
  } catch (cause) {
    throw new Error("The platform returned invalid JSON connection settings", {
      cause,
    });
  }
  return {
    status: "downloaded",
    connection: validateConnection(document, origin),
  };
}

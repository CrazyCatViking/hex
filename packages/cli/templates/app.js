import { createHexClient } from "./hex-client.js";

const hex = createHexClient({ site: "__HEX_SITE_NAME__" });
const status = document.querySelector("#status");

try {
  const capabilities = await hex.capabilities();
  status.textContent = JSON.stringify(capabilities, null, 2);
} catch (error) {
  status.textContent = error.message;
}

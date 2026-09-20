import { isIP } from "node:net";
import { parseServerOrigin, validateSiteName } from "./project.mjs";

export function siteURL(base, name) {
  validateSiteName(name);
  const url = parseServerOrigin(base);
  const hostname = url.hostname;
  if (isIP(hostname) || hostname.startsWith("[")) {
    throw new Error(
      "Use a DNS hostname for siteBaseURL, such as http://localhost:8080",
    );
  }
  for (const label of hostname.split(".")) {
    validateSiteName(label);
  }
  url.hostname = `${name}.${hostname}`;
  return url.href;
}

import { copyFile, mkdir } from "node:fs/promises";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";

const source = dirname(fileURLToPath(import.meta.url));
const output = resolve(
  process.argv[2] || "/tmp/opencode/hex-integrations-prototype",
);
await mkdir(output, { recursive: true });
for (const file of ["index.html", "styles.css", "prototype.js"]) {
  await copyFile(join(source, file), join(output, file));
}
await copyFile(
  join(source, "../../server/portal/portal.css"),
  join(output, "portal.css"),
);
console.log(`Integration prototype: ${output}`);

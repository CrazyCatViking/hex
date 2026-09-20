import { readFile, writeFile } from "node:fs/promises";

const source = new URL("../packages/client/dist/index.js", import.meta.url);
const destination = new URL(
  "../internal/cli/assets/hex-client.js",
  import.meta.url,
);
const compiled = await readFile(source);

if (process.argv.includes("--check")) {
  const embedded = await readFile(destination);
  if (!compiled.equals(embedded)) {
    throw new Error(
      "Embedded browser client is stale. Run npm run build and commit the refreshed asset.",
    );
  }
} else {
  await writeFile(destination, compiled);
}

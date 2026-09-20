import { fileURLToPath } from "node:url";
import { devCommand } from "../packages/cli/src/dev/index.mjs";

const root = fileURLToPath(new URL("../", import.meta.url));

await devCommand([
  root,
  "--package",
  "./examples/custom-server",
  "--data-dir",
  ".hex-data",
  ...process.argv.slice(2),
]);

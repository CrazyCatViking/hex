import { readFile, writeFile } from "node:fs/promises";

const source = new URL("../node_modules/htmx.org/", import.meta.url);
const destination = new URL("../server/portal/htmx.min.js", import.meta.url);
const metadata = JSON.parse(
  await readFile(new URL("package.json", source), "utf8"),
);
const license = await readFile(new URL("LICENSE", source), "utf8");
const script = await readFile(new URL("dist/htmx.min.js", source), "utf8");
const content = `/*! htmx.org ${metadata.version}\n${license}\n*/\n${script}\n`;

if (process.argv.includes("--check")) {
  if ((await readFile(destination, "utf8")) !== content) {
    throw new Error(
      "Vendored HTMX differs from the pinned npm dependency. Run npm run build:portal and commit the updated asset.",
    );
  }
} else {
  await writeFile(destination, content);
}

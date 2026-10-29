import { copyFile, mkdir, readFile, writeFile } from "node:fs/promises";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";

const source = dirname(fileURLToPath(import.meta.url));
const output = resolve(
  process.argv[2] || "/tmp/opencode/hex-portal-prototypes",
);
const template = await readFile(join(source, "index.html"), "utf8");
const portalCSS = await readFile(
  join(source, "../../server/portal/portal.css"),
  "utf8",
);
// Preview-only theme switching reuses the current stylesheet's own declarations.
const light = portalCSS.match(/:root \{([\s\S]*?)\n\}/)?.[1];
const dark = portalCSS.match(
  /@media \(prefers-color-scheme: dark\) \{\s*:root \{([\s\S]*?)\n  \}/,
)?.[1];
if (!light || !dark) throw new Error("Could not find the portal theme tokens");
const themes = `:root[data-theme="light"] {${light}\n}\n:root[data-theme="dark"] {${dark}\n}\n`;
for (const concept of ["launchpad", "library", "operations"]) {
  const destination = join(output, concept);
  await mkdir(destination, { recursive: true });
  await writeFile(
    join(destination, "index.html"),
    template.replace("__CONCEPT__", concept),
  );
  for (const file of ["styles.css", "prototype.js"]) {
    await copyFile(join(source, file), join(destination, file));
  }
  await writeFile(join(destination, "portal.css"), portalCSS);
  await writeFile(join(destination, "themes.css"), themes);
  console.log(`${concept}: ${destination}`);
}

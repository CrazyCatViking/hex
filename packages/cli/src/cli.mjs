#!/usr/bin/env node
import { realpath } from "node:fs/promises";
import { relative, resolve, sep } from "node:path";
import { pathToFileURL } from "node:url";
import { parseArgs } from "node:util";
import { createAPI } from "./api.mjs";
import { archiveDirectory } from "./archive.mjs";
import {
  initializeProject,
  installSkills,
  readProjectConfig,
  validateSiteName,
} from "./project.mjs";

export { archiveDirectory } from "./archive.mjs";

const help = `hex init [directory] [--name name] [--server URL] [--resource ENTRA_APP_ID_URI]
hex publish
hex sites
hex delete [site] --yes
hex capabilities
hex skills

Run management commands in a project containing hex.json.
For Azure, supply HEX_TOKEN or configure resource and sign in with az login.`;

async function publishSite(api, config, siteName) {
  const capabilities = await api.request("/api/hex/capabilities");
  if (!capabilities.sites) {
    throw new Error("Site publishing is disabled on this server");
  }

  const projectDirectory = await realpath(process.cwd());
  const publishDirectory = await realpath(resolve(config.directory));
  const relativeDirectory = relative(projectDirectory, publishDirectory);
  const outsideProject =
    relativeDirectory === ".." ||
    relativeDirectory.startsWith(".." + sep) ||
    resolve(projectDirectory, relativeDirectory) !== publishDirectory;

  if (!relativeDirectory || outsideProject) {
    throw new Error("Publish directory must be a subdirectory of the project");
  }

  const archive = await archiveDirectory(
    publishDirectory,
    capabilities.maxUploadBytes,
  );
  const site = await api.request(
    `/api/sites/${siteName}/deploy`,
    "POST",
    archive,
  );
  console.log(new URL(site.url, api.server).href);
}

async function deleteSite(api, siteName, confirmed) {
  if (!confirmed) {
    throw new Error("Use --yes to unpublish this site");
  }

  await api.request(`/api/sites/${siteName}`, "DELETE");
  console.log(`Unpublished ${siteName}`);
}

export async function main(args = process.argv.slice(2)) {
  const { positionals, values } = parseArgs({
    args,
    allowPositionals: true,
    options: {
      server: { type: "string" },
      name: { type: "string" },
      resource: { type: "string" },
      yes: { type: "boolean" },
      help: { type: "boolean" },
    },
  });
  const [command, argument] = positionals;

  if (values.help || !command) {
    console.log(help);
    return;
  }

  if (command === "init") {
    await initializeProject(argument, values);
    return;
  }

  if (command === "skills") {
    await installSkills(process.cwd());
    console.log("Installed .agents/skills/hex/SKILL.md");
    return;
  }

  if (!["publish", "sites", "delete", "capabilities"].includes(command)) {
    throw new Error(`Unknown command: ${command}`);
  }

  const config = await readProjectConfig();
  const api = createAPI(config, values);

  switch (command) {
    case "capabilities":
      console.log(
        JSON.stringify(await api.request("/api/hex/capabilities"), null, 2),
      );
      return;

    case "sites":
      console.log(JSON.stringify(await api.request("/api/sites"), null, 2));
      return;

    case "delete":
      await deleteSite(
        api,
        validateSiteName(argument ?? config.name),
        values.yes,
      );
      return;

    case "publish":
      await publishSite(api, config, validateSiteName(argument ?? config.name));
      return;
  }
}

if (
  process.argv[1] &&
  import.meta.url === pathToFileURL(await realpath(process.argv[1])).href
) {
  main().catch((error) => {
    console.error(error.message);
    process.exitCode = 1;
  });
}

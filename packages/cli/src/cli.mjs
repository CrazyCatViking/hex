#!/usr/bin/env node
import { realpath } from "node:fs/promises";
import { pathToFileURL } from "node:url";
import { parseArgs } from "node:util";
import { createAPI } from "./api.mjs";
import { publishSite, unpublishSite } from "./publishing/index.mjs";
import { siteURL } from "./site-url.mjs";
import { loadProfile } from "./profiles.mjs";
import { loginPublishing } from "./publishing/login.mjs";
import {
  initializeProject,
  installSkills,
  readProjectConfig,
  validateSiteName,
} from "./project.mjs";

const help = `hex setup [platform-url] [--file downloaded.json] [--name company] [--json]
hex login [--platform company]
hex init [directory] [--name name] [--platform company] [--server URL] [--resource ENTRA_APP_ID_URI]
         [--publish-root DIRECTORY | --publish-url AZURE_FILES_URL]
         [--site-base-url URL]
hex publish
hex sites
hex delete [site] --yes
hex capabilities [--refresh]
hex skills
hex dev [server-directory] [--package ./cmd/server] [--services postgres,azurite]

Run commands in a project containing hex.json.
publish/delete use the publishing provider directly; they never call the Hex API.
Azure Files publishing uses AzCopy login or HEX_PUBLISH_SAS and requires storage network access.
sites/capabilities use HEX_TOKEN or the optional resource setting for gateway access.`;

export async function main(args = process.argv.slice(2)) {
  if (args[0] === "setup") {
    const { setupCommand } = await import("./setup/index.mjs");
    await setupCommand(args.slice(1));
    return;
  }
  if (args[0] === "dev") {
    const { devCommand } = await import("./dev/index.mjs");
    await devCommand(args.slice(1));
    return;
  }

  const { positionals, values } = parseArgs({
    args,
    allowPositionals: true,
    options: {
      server: { type: "string" },
      name: { type: "string" },
      resource: { type: "string" },
      platform: { type: "string" },
      refresh: { type: "boolean" },
      "publish-root": { type: "string" },
      "publish-url": { type: "string" },
      "site-base-url": { type: "string" },
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

  if (
    !["publish", "sites", "delete", "capabilities", "login"].includes(command)
  ) {
    throw new Error(`Unknown command: ${command}`);
  }

  const needsProject = command === "publish" || command === "delete";
  let config =
    values.platform && !needsProject
      ? null
      : await readProjectConfig({ optional: !needsProject });
  if (values.platform || !config) {
    const profile = await loadProfile(values.platform);
    config = needsProject
      ? {
          ...config,
          ...profile.connection,
          name: config.name,
          directory: config.directory,
        }
      : profile.connection;
  }

  if (command === "login") {
    await loginPublishing(config);
    return;
  }

  if (command === "capabilities" && config.capabilities && !values.refresh) {
    console.log(JSON.stringify(config.capabilities, null, 2));
    return;
  }

  if (command === "publish") {
    const siteName = validateSiteName(argument ?? config.name);
    const base =
      values["site-base-url"] ??
      config.siteBaseURL ??
      values.server ??
      config.server;
    const url = siteURL(base, siteName);
    await publishSite(config, siteName);
    console.log(url);
    return;
  }

  if (command === "delete") {
    if (!values.yes) {
      throw new Error("Use --yes to unpublish this site");
    }
    const siteName = validateSiteName(argument ?? config.name);
    await unpublishSite(config, siteName);
    console.log(`Unpublished ${siteName}`);
    return;
  }

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

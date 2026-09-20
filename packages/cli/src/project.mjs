import { mkdir, readFile, writeFile } from "node:fs/promises";
import { join, resolve, sep } from "node:path";
import { fileURLToPath } from "node:url";

export function validateSiteName(value) {
  if (
    typeof value !== "string" ||
    !/^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?$/.test(value)
  ) {
    throw new Error(
      "Site names must be DNS labels: 1–63 lowercase letters, digits or hyphens, starting and ending with a letter or digit",
    );
  }

  return value;
}

export function parseServerOrigin(value) {
  const server = new URL(value);
  const validProtocol =
    server.protocol === "https:" || server.protocol === "http:";
  const hasCredentials = server.username || server.password;
  const hasExtraPath = server.pathname !== "/" || server.search || server.hash;

  if (!validProtocol || hasCredentials || hasExtraPath) {
    throw new Error("Server must be an HTTP(S) origin");
  }

  return server;
}

export async function readProjectConfig() {
  try {
    const content = await readFile("hex.json", "utf8");
    return JSON.parse(content);
  } catch (error) {
    throw new Error(`Read project configuration hex.json: ${error.message}`, {
      cause: error,
    });
  }
}

export async function installSkills(projectDirectory) {
  const source = new URL("../skills/hex/SKILL.md", import.meta.url);
  const destination = join(projectDirectory, ".agents/skills/hex");

  await mkdir(destination, { recursive: true });
  await writeFile(join(destination, "SKILL.md"), await readFile(source));
}

async function writeStarterFile(path, content) {
  try {
    await writeFile(path, content, { flag: "wx" });
  } catch (error) {
    if (error.code === "EEXIST") {
      return;
    }

    throw new Error(`Create starter file ${path}: ${error.message}`, {
      cause: error,
    });
  }
}

async function installStarter(projectDirectory, siteName) {
  const publicDirectory = join(projectDirectory, "public");
  await mkdir(publicDirectory, { recursive: true });

  const clientPath = fileURLToPath(import.meta.resolve("@hex-platform/client"));
  const htmlTemplate = new URL("../templates/index.html", import.meta.url);
  const scriptTemplate = new URL("../templates/app.js", import.meta.url);
  const script = await readFile(scriptTemplate, "utf8");

  await writeStarterFile(
    join(publicDirectory, "hex-client.js"),
    await readFile(clientPath),
  );
  await writeStarterFile(
    join(publicDirectory, "index.html"),
    await readFile(htmlTemplate),
  );
  await writeStarterFile(
    join(publicDirectory, "app.js"),
    script.replace('"__HEX_SITE_NAME__"', JSON.stringify(siteName)),
  );
}

export async function initializeProject(directory, options) {
  const projectDirectory = resolve(directory ?? ".");
  const siteName = validateSiteName(
    options.name ?? projectDirectory.split(sep).at(-1),
  );
  const server = parseServerOrigin(options.server ?? "http://localhost:8080");
  const config = {
    name: siteName,
    server: server.origin,
    directory: "public",
  };

  if (options.resource) {
    config.resource = options.resource;
  }

  if (options["site-base-url"]) {
    config.siteBaseURL = parseServerOrigin(options["site-base-url"]).origin;
  }

  if (options["publish-root"] && options["publish-url"]) {
    throw new Error("Choose either --publish-root or --publish-url");
  }
  if (options["publish-root"]) {
    config.publishing = {
      provider: "filesystem",
      root: resolve(options["publish-root"]),
    };
  }
  if (options["publish-url"]) {
    config.publishing = {
      provider: "azure-files",
      url: options["publish-url"],
    };
  }

  await mkdir(projectDirectory, { recursive: true });
  await writeFile(
    join(projectDirectory, "hex.json"),
    JSON.stringify(config, null, 2) + "\n",
    {
      flag: "wx",
    },
  );

  await installStarter(projectDirectory, siteName);
  await installSkills(projectDirectory);
  console.log(
    `Initialized ${siteName}. Agent skill: .agents/skills/hex/SKILL.md`,
  );
}

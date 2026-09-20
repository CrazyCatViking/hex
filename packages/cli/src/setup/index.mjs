import { createInterface } from "node:readline/promises";
import { parseArgs } from "node:util";
import openBrowser from "open";
import { platformOrigin } from "../connection.mjs";
import { saveProfile } from "../profiles.mjs";
import { readConnectionFile } from "./files.mjs";
import { downloadConnection } from "./download.mjs";

export async function setupPlatform(options, interaction, dependencies = {}) {
  let server = options.server;
  let connection;
  if (options.file) {
    connection = await readConnectionFile(options.file, server);
  } else {
    if (!server && interaction) {
      server = await interaction.ask("Company Hex URL: ");
    }
    if (!server) {
      return {
        status: "input_required",
        message:
          "Supply a platform URL or use --file with a downloaded connection file.",
      };
    }
    server = platformOrigin(server.trim());
    const downloaded = await downloadConnection(server, dependencies.fetch);
    if (downloaded.status === "download_required") {
      if (!interaction) {
        return {
          ...downloaded,
          server,
          message:
            "Ask the user to open downloadURL in their browser, sign in, and download the connection file. Import its path with hex setup --file.",
          nextCommand:
            "hex setup --file <downloaded-file> --server <platform-url> --json",
        };
      }
      interaction.write(
        "Your hosting provider requires a browser visit. Sign in there and download the connection file.",
      );
      interaction.write(downloaded.downloadURL);
      try {
        await interaction.openBrowser(downloaded.downloadURL);
      } catch (error) {
        interaction.write(
          `Could not open a browser automatically: ${error.message}. Open the URL above manually.`,
        );
      }
      while (!connection) {
        const path = await interaction.ask(
          "Drop the downloaded JSON file here, or paste its path, then press Enter: ",
        );
        try {
          connection = await readConnectionFile(path, server);
        } catch (error) {
          interaction.write(`Could not import that file: ${error.message}`);
        }
      }
    } else {
      connection = downloaded.connection;
    }
  }

  const profile = await saveProfile(
    connection,
    options.name,
    dependencies.profileDirectory,
  );
  return {
    status: "ready",
    profile,
    name: connection.name,
    server: connection.server,
    publishing: connection.publishing?.provider ?? null,
    nextCommand:
      connection.publishing?.provider === "azure-files"
        ? `hex login --platform ${profile}`
        : "hex init my-app",
  };
}

export async function setupCommand(args) {
  const { positionals, values } = parseArgs({
    args,
    allowPositionals: true,
    options: {
      file: { type: "string" },
      server: { type: "string" },
      name: { type: "string" },
      json: { type: "boolean" },
      help: { type: "boolean" },
    },
  });
  if (values.help) {
    console.log(`hex setup [platform-url] [--name company] [--json]
hex setup --file downloaded.json [--server platform-url] [--name company] [--json]

Downloads non-secret platform settings, or imports a file downloaded through
your browser after company SSO. Interactive prompts accept terminal-dropped
file paths. --json never opens a browser or waits for input; exit code 2
means a user action is needed. Successful imports become the default profile.`);
    return;
  }
  if (positionals.length > 1 || (positionals[0] && values.server)) {
    throw new Error("Supply one platform URL");
  }

  let terminal;
  const controller = new AbortController();
  const interactive =
    !values.json && process.stdin.isTTY && process.stdout.isTTY;
  if (interactive) {
    terminal = createInterface({
      input: process.stdin,
      output: process.stdout,
    });
    terminal.on("SIGINT", () => controller.abort());
    terminal.on("close", () => controller.abort());
  }
  const interaction = terminal
    ? {
        ask: (question) =>
          terminal.question(question, { signal: controller.signal }),
        write: (message) => console.log(message),
        openBrowser,
      }
    : null;

  try {
    const result = await setupPlatform(
      {
        server: positionals[0] ?? values.server,
        file: values.file,
        name: values.name,
      },
      interaction,
    );
    if (values.json) {
      console.log(JSON.stringify(result));
    } else if (result.status === "ready") {
      console.log(
        `Connected to ${result.name}. Saved default profile: ${result.profile}`,
      );
      console.log(`Next: ${result.nextCommand}`);
    } else {
      console.log(result.message);
      if (result.downloadURL) {
        console.log(`Download: ${result.downloadURL}`);
      }
      if (result.nextCommand) {
        console.log(`Then: ${result.nextCommand}`);
      }
    }
    if (result.status !== "ready") {
      process.exitCode = 2;
    }
  } catch (error) {
    const message = controller.signal.aborted
      ? "Setup cancelled"
      : error.message;
    if (!values.json) {
      throw new Error(message, { cause: error });
    }
    console.log(JSON.stringify({ status: "error", message }));
    process.exitCode = 1;
  } finally {
    terminal?.close();
  }
}

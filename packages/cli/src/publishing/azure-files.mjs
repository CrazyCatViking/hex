import { execFile } from "node:child_process";
import { promisify } from "node:util";
import { copyFile, mkdir, mkdtemp, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import { dirname, join } from "node:path";
import { validateSiteName } from "../project.mjs";

const exec = promisify(execFile);

async function runAzCopy(args) {
  try {
    await exec("azcopy", args, { maxBuffer: 16 * 1024 * 1024 });
  } catch (cause) {
    if (cause.code === "ENOENT") {
      throw new Error("Install AzCopy to publish to Azure Files", { cause });
    }
    throw new Error(
      "AzCopy failed. Check storage connectivity, AzCopy login/permissions, and its job logs.",
      { cause },
    );
  }
}

export function azureFilesPublisher(settings, run = runAzCopy) {
  const root = new URL(settings.url);
  if (
    root.protocol !== "https:" ||
    root.username ||
    root.password ||
    root.search ||
    root.hash ||
    root.pathname === "/"
  ) {
    throw new Error(
      "Azure Files publishing requires an HTTPS share/prefix URL without credentials or query parameters",
    );
  }

  function siteURL(name) {
    validateSiteName(name);
    const destination = new URL(root);
    destination.pathname = root.pathname.replace(/\/$/, "") + "/" + name;
    if (process.env.HEX_PUBLISH_SAS) {
      destination.search = process.env.HEX_PUBLISH_SAS.replace(/^\?/, "");
    }
    return destination.href;
  }

  return {
    async publish(name, source) {
      const directory = await mkdtemp(join(tmpdir(), "hex-publish-"));
      try {
        for (const file of source.files) {
          const path = join(directory, ...file.key.split("/"));
          await mkdir(dirname(path), { recursive: true });
          await copyFile(file.path, path);
        }
        await run([
          "sync",
          directory,
          siteURL(name),
          "--recursive=true",
          "--delete-destination=true",
        ]);
      } finally {
        await rm(directory, { recursive: true, force: true });
      }
    },

    async delete(name) {
      await run(["remove", siteURL(name), "--recursive=true"]);
    },
  };
}

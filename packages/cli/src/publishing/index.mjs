import { validateSiteName } from "../project.mjs";
import { readSource } from "./source.mjs";
import { filesystemPublisher } from "./filesystem.mjs";
import { azureFilesPublisher } from "./azure-files.mjs";

function publishingProvider(settings) {
  switch (settings?.provider) {
    case "filesystem":
      return filesystemPublisher(settings);
    case "azure-files":
      return azureFilesPublisher(settings);
    default:
      throw new Error(
        "Configure publishing.provider in hex.json as filesystem or azure-files; publishing does not use the Hex API",
      );
  }
}

export async function publishSite(config, name) {
  validateSiteName(name);
  const provider = publishingProvider(config.publishing);
  const source = await readSource(config.directory);
  await provider.publish(name, source);
}

export async function unpublishSite(config, name) {
  validateSiteName(name);
  const provider = publishingProvider(config.publishing);
  await provider.delete(name);
}

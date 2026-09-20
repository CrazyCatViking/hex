import { spawn } from "node:child_process";
import { once } from "node:events";

export async function loginPublishing(config) {
  if (config.publishing?.provider === "filesystem") {
    console.log("Filesystem publishing does not require a storage login.");
    return;
  }
  if (config.publishing?.provider !== "azure-files") {
    throw new Error("No supported publishing provider is configured");
  }

  console.log(
    "AzCopy will sign you in to Azure Storage. Hex does not handle your credentials.",
  );
  const child = spawn("azcopy", ["login"], { stdio: "inherit" });
  let result;
  try {
    result = await once(child, "close");
  } catch (cause) {
    throw new Error(
      "Could not start AzCopy. Install AzCopy v10 to use Azure Files publishing.",
      { cause },
    );
  }
  const [code, signal] = result;
  if (code !== 0) {
    throw new Error(
      `AzCopy login did not complete (${signal ?? code}). Follow its instructions or ask your storage administrator.`,
    );
  }
}

import { execFileSync } from "node:child_process";
import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";

const root = fileURLToPath(new URL("../", import.meta.url));
const packageName = "@crazycatviking/hex";
const versionPattern =
  /^(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)(?:-([0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*))?$/;

function run(command, args, capture = false) {
  try {
    const output = execFileSync(command, args, {
      cwd: root,
      encoding: "utf8",
      stdio: capture ? ["inherit", "pipe", "inherit"] : "inherit",
    });
    if (capture) {
      return output.trim();
    }
  } catch (error) {
    throw new Error(`Failed: ${command} ${args.join(" ")}`, { cause: error });
  }
}

function isVersion(version) {
  const match = version.match(versionPattern);
  if (!match) {
    return false;
  }

  const prerelease = match[4];
  if (!prerelease) {
    return true;
  }
  return prerelease
    .split(".")
    .every((identifier) => !/^0\d+$/.test(identifier));
}

function validateVersion(version) {
  if (!isVersion(version)) {
    throw new Error("Use a version such as 0.10.0 or 0.10.0-rc.1.");
  }
  return version;
}

function nextMinor(version) {
  const [major, minor] = validateVersion(version).split(".");
  return `${major}.${Number(minor) + 1}.0`;
}

function releaseBranch() {
  if (run("git", ["status", "--porcelain"], true)) {
    throw new Error("Commit or stash working-tree changes before releasing.");
  }

  const branch = run("git", ["symbolic-ref", "--short", "HEAD"], true);
  const remote = run("git", ["config", `branch.${branch}.remote`], true);
  const ref = run("git", ["config", `branch.${branch}.merge`], true);
  if (remote === "." || !ref.startsWith("refs/heads/")) {
    throw new Error("The release branch must track a remote branch.");
  }

  run("git", ["fetch", remote, "--tags"]);
  const behind = run("git", ["rev-list", "--count", "HEAD..@{upstream}"], true);
  if (behind !== "0") {
    throw new Error(
      "Update the release branch from its upstream before releasing.",
    );
  }
  return { remote, ref };
}

function latestCLIVersion() {
  const tags = run("git", ["tag", "--list", "cli-v*"], true);
  const versions = tags
    .split("\n")
    .map((tag) => tag.slice("cli-v".length))
    .filter(isVersion);

  versions.sort((left, right) => {
    const leftParts = left.split("-")[0].split(".").map(Number);
    const rightParts = right.split("-")[0].split(".").map(Number);
    for (let index = 0; index < 3; index += 1) {
      const difference = rightParts[index] - leftParts[index];
      if (difference !== 0) {
        return difference;
      }
    }
    return 0;
  });
  return versions[0] ?? "0.0.0";
}

function publishClient(requestedVersion, branch) {
  const metadata = JSON.parse(
    readFileSync(
      new URL("../packages/client/package.json", import.meta.url),
      "utf8",
    ),
  );
  const version = requestedVersion || nextMinor(metadata.version);
  const tag = version.includes("-") ? "next" : "latest";
  console.log(`Releasing ${packageName}@${version} (${tag}).`);

  run("npm", ["whoami"]);
  run("npm", ["ci"]);
  run("just", ["version-client", version]);
  run("just", ["pack-client"]);
  run("just", ["test-client-package"]);

  run("git", ["diff", "--check"]);
  run("git", ["log", "--oneline", "-10"]);
  run("git", ["status", "--short"]);
  run("git", [
    "diff",
    "--",
    "packages/client/package.json",
    "package-lock.json",
  ]);
  run("git", [
    "add",
    "--",
    "packages/client/package.json",
    "package-lock.json",
  ]);
  run("git", ["commit", "-m", `Publish ${version} client`]);
  run("git", ["push", branch.remote, `HEAD:${branch.ref}`]);

  const publishArgs = [
    "publish",
    "--workspace",
    packageName,
    "--access",
    "public",
    "--tag",
    tag,
  ];
  try {
    run("npm", publishArgs);
  } catch (error) {
    throw new Error(
      `Client ${version} was committed and pushed, but npm publishing failed. Retry with: npm ${publishArgs.join(" ")}`,
      { cause: error },
    );
  }
}

function publishCLI(requestedVersion, branch) {
  const version = requestedVersion || nextMinor(latestCLIVersion());
  const tag = `cli-v${version}`;
  const existingTag = run("git", ["tag", "--list", tag], true);
  if (existingTag) {
    throw new Error(
      `${tag} already exists. Choose a new version, or use just upload-cli ${version} to retry its upload.`,
    );
  }
  console.log(`Releasing Hex CLI ${version}.`);

  run("gh", ["auth", "status"]);
  run("go", ["test", "-race", "./..."]);
  run("go", ["vet", "./..."]);
  run("just", ["build-cli", version]);
  if (run("git", ["status", "--porcelain"], true)) {
    throw new Error(
      "Verification changed tracked source files; commit them before releasing.",
    );
  }

  run("git", ["push", branch.remote, `HEAD:${branch.ref}`]);
  run("git", ["tag", tag]);
  run("git", ["push", branch.remote, `refs/tags/${tag}`]);
  try {
    run("just", ["upload-cli", version]);
  } catch (error) {
    throw new Error(
      `CLI tag ${tag} was pushed, but the GitHub release failed. Retry with: just upload-cli ${version}`,
      { cause: error },
    );
  }
}

try {
  const [target, requestedVersion = "", ...extra] = process.argv.slice(2);
  if (!["client", "cli"].includes(target) || extra.length > 0) {
    throw new Error("Usage: node scripts/release.mjs <client|cli> [version]");
  }
  if (requestedVersion) {
    validateVersion(requestedVersion);
  }

  const branch = releaseBranch();
  if (target === "client") {
    publishClient(requestedVersion, branch);
  } else {
    publishCLI(requestedVersion, branch);
  }
} catch (error) {
  console.error(error.message);
  process.exitCode = 1;
}

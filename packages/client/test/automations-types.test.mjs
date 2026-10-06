import { test } from "node:test";
import { execFileSync } from "node:child_process";
import { fileURLToPath } from "node:url";

test("automation declarations provide JSDoc hints and reject unsupported capabilities", () => {
  execFileSync(
    process.execPath,
    [
      fileURLToPath(import.meta.resolve("typescript/bin/tsc")),
      "--allowJs",
      "--checkJs",
      "--noEmit",
      "--strict",
      "--target",
      "ES2022",
      "--module",
      "NodeNext",
      "--moduleResolution",
      "NodeNext",
      "--lib",
      "ES2022",
      fileURLToPath(new URL("./fixtures/automation.js", import.meta.url)),
    ],
    { stdio: "inherit" },
  );
});

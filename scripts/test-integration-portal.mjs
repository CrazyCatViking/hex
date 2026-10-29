import assert from "node:assert/strict";
import { execFile, spawn } from "node:child_process";
import { once } from "node:events";
import { mkdir, mkdtemp, rm } from "node:fs/promises";
import { join } from "node:path";
import { fileURLToPath } from "node:url";
import { promisify } from "node:util";
import { chromium } from "playwright";

const root = fileURLToPath(new URL("../", import.meta.url));
const temporary = await mkdtemp("/tmp/opencode/hex-integration-browser-");
const screenshots = "/tmp/opencode/hex-integration-portal-review";
await mkdir(screenshots, { recursive: true });
let child;
let browser;
try {
  await promisify(execFile)(
    "go",
    [
      "build",
      "-o",
      join(temporary, "server"),
      "./scripts/testdata/integration-portal",
    ],
    { cwd: root },
  );
  child = spawn(join(temporary, "server"), {
    stdio: ["ignore", "pipe", "inherit"],
  });
  const origin = await new Promise((resolve, reject) => {
    child.once("error", reject);
    child.once("exit", (code) =>
      reject(new Error(`Fixture exited before readiness: ${code}`)),
    );
    child.stdout.once("data", (data) => resolve(data.toString().trim()));
  });
  browser = await chromium.launch({ headless: true });
  const context = await browser.newContext({
    viewport: { width: 1440, height: 1080 },
    colorScheme: "light",
  });
  const page = await context.newPage();
  page.setDefaultTimeout(7000);
  const errors = [];
  const calls = [];
  page.on("pageerror", (error) => errors.push(error.message));
  page.on("request", (request) => {
    assert.ok(
      request.url().startsWith(origin),
      "Do not call upstream services",
    );
    if (request.url().includes("/api/sites/") && request.method() === "POST")
      calls.push(request.url());
  });
  const dialog = page.locator("#integration-dialog");
  const card = (name) => page.locator(`[data-integration-card="${name}"]`);
  const feature = (name) =>
    dialog.locator(".integration-endpoint").filter({
      has: page.locator(".integration-endpoint-name strong", {
        hasText: new RegExp(`^${name}$`),
      }),
    });
  const open = async (name) => {
    await page.locator(`[data-integration-open="${name}"]`).click();
    await dialog.locator(`[data-integration-detail="${name}"]`).waitFor();
  };
  const close = async () => {
    await dialog
      .getByRole("button", { name: "Close integration details", exact: true })
      .click();
    await dialog.waitFor({ state: "hidden" });
  };
  const noOverflow = async (selector = "html") =>
    assert.equal(
      await page
        .locator(selector)
        .evaluate((element) => element.scrollWidth > element.clientWidth + 1),
      false,
      `${selector} overflow`,
    );
  const screenshot = async (name) =>
    page.screenshot({
      path: `${screenshots}/${name}.png`,
      fullPage: !(await dialog.isVisible()),
    });

  await page.goto(`${origin}/integrations`);
  await page.locator("[data-integration-controls]:visible").waitFor();
  assert.equal(await page.locator("[data-integration-card]").count(), 6);
  assert.equal(await page.locator(".integration-facts > div").count(), 30);
  assert.equal(
    await page.getByRole("textbox").count(),
    0,
    "No site lookup form",
  );
  assert.match(await card("github").innerText(), /2 of 3 features/);
  assert.match(await card("azuredevops").innerText(), /Link login to use/);
  assert.match(await card("google-tasks").innerText(), /Connected/);
  assert.match(await card("hubspot").innerText(), /No permission/);
  assert.equal(
    await page.locator("[data-integration-detail]").count(),
    0,
    "Load contracts only on demand",
  );
  await noOverflow();
  await screenshot("catalog-desktop");

  await page
    .getByRole("button", { name: "Granted to you", exact: true })
    .click();
  assert.equal(
    await page.locator("[data-integration-card]:visible").count(),
    5,
  );
  await page
    .getByRole("button", { name: "For automations", exact: true })
    .click();
  assert.equal(
    await page.locator("[data-integration-card]:visible").count(),
    4,
  );
  await page.getByRole("button", { name: "Linked login", exact: true }).click();
  assert.equal(
    await page.locator("[data-integration-card]:visible").count(),
    2,
  );
  await page
    .getByRole("button", { name: "All integrations", exact: false })
    .click();
  await page.getByRole("button", { name: "List view", exact: true }).click();
  await noOverflow();
  await screenshot("catalog-list");
  for (const width of [768, 1024, 1120]) {
    await page.setViewportSize({ width, height: 1080 });
    await noOverflow();
  }
  await page.setViewportSize({ width: 1440, height: 1080 });
  await page.getByRole("button", { name: "Grid view", exact: true }).click();

  await open("github");
  assert.match(await dialog.innerText(), /2 of 3 permissions granted/);
  assert.match(await feature("create-issue").innerText(), /Permission needed/);
  await feature("create-issue").locator("summary").click();
  assert.match(
    await feature("create-issue").innerText(),
    /github.create-issue/,
  );
  assert.equal(await feature("create-issue").locator("pre").count(), 2);
  await noOverflow("#integration-dialog");
  await screenshot("details-desktop");
  for (let index = 0; index < 12; index++) {
    await page.keyboard.press("Tab");
    assert.equal(
      await dialog.evaluate((element) => {
        // Native dialogs may tab to browser chrome (activeElement is body),
        // but must never focus an inert catalog or navigation control.
        return (
          document.activeElement === document.body ||
          element.contains(document.activeElement)
        );
      }),
      true,
      "Modal focus must not escape into the catalog",
    );
  }
  await page.keyboard.press("Escape");
  await dialog.waitFor({ state: "hidden" });
  assert.equal(
    await page.evaluate(() => document.activeElement.dataset.integrationOpen),
    "github",
  );

  await open("azuredevops");
  assert.equal(
    await dialog
      .locator(".integration-endpoint-status .integration-status")
      .filter({ hasText: /^Link login$/ })
      .count(),
    2,
  );
  const connect = dialog.getByRole("link", {
    name: "Link Microsoft login",
    exact: true,
  });
  const start = new URL(await connect.getAttribute("href"));
  assert.equal(start.origin, origin);
  assert.equal(
    start.searchParams.get("return"),
    `${origin}/integrations#azuredevops`,
  );
  const oauth = await page.request.get(start.href, { maxRedirects: 0 });
  assert.equal(oauth.status(), 302);
  assert.equal(
    new URL(oauth.headers().location).hostname,
    "oauth.example.test",
    "Real OAuth start route, without following external redirect",
  );
  await screenshot("linked-login");
  await close();

  await open("google-tasks");
  assert.match(
    await dialog.innerText(),
    /Jamie <img src=x onerror=alert\(1\)>/,
  );
  assert.equal(
    await dialog.locator("img").count(),
    0,
    "Connected account metadata must be escaped",
  );
  await close();

  await open("sentry");
  assert.match(await dialog.innerText(), /3 of 3 permissions granted/);
  assert.match(await dialog.innerText(), /Site approval also required/);
  await close();
  await page.locator(".integration-approval-review > summary").click();
  await page
    .locator("#integration-approvals")
    .getByRole("button", { name: "Approve", exact: true })
    .click();
  await page
    .locator("#integration-approvals")
    .getByRole("button", { name: "Revoke approval", exact: true })
    .waitFor();
  await page.locator(".integration-approval-review > summary").click();
  await page.goto(`${origin}/integrations?site=demo`);
  assert.match(
    await page.locator("#site-integrations").innerText(),
    /Approved/,
  );
  assert.match(await page.locator("#site-integrations").innerText(), /Allowed/);
  await page.goto(`${origin}/integrations`);

  // Exercise loading, cancellation, retry and a gateway's HTML login response.
  const detailURL = "**/api/hex/manage/integrations/github";
  let receiveRequest;
  const heldRequest = new Promise((resolve) => {
    receiveRequest = resolve;
  });
  await page.route(detailURL, async (route) => {
    receiveRequest(route);
  });
  await page.locator('[data-integration-open="github"]').click();
  const delayed = await heldRequest;
  await dialog.locator("[data-integration-loading]:visible").waitFor();
  await close();
  await delayed.continue();
  await page.unroute(detailURL);
  await open("slack");
  assert.equal(
    await dialog.locator('[data-integration-detail="github"]').count(),
    0,
    "A dismissed request must not overwrite another integration",
  );
  await close();

  await page.route(detailURL, (route) =>
    route.fulfill({
      status: 503,
      json: { error: "Catalog temporarily unavailable" },
    }),
  );
  await page.locator('[data-integration-open="github"]').click();
  await dialog
    .getByRole("alert")
    .filter({ hasText: "Catalog temporarily unavailable" })
    .waitFor();
  assert.equal(
    await dialog
      .getByRole("link", { name: "Open full details page" })
      .getAttribute("href"),
    `${origin}/integrations/github`,
  );
  await page.unroute(detailURL);
  await dialog.getByRole("button", { name: "Try again", exact: true }).click();
  await dialog.locator('[data-integration-detail="github"]').waitFor();
  await close();
  await page.route(detailURL, (route) =>
    route.fulfill({
      status: 200,
      contentType: "text/html",
      body: "<form>Gateway login</form>",
    }),
  );
  await page.locator('[data-integration-open="github"]').click();
  await dialog
    .getByRole("alert")
    .filter({ hasText: "Your session may have expired" })
    .waitFor();
  assert.equal(await dialog.locator("form").count(), 0);
  await close();
  await page.unroute(detailURL);

  await open("github");
  await page.goBack();
  await dialog.waitFor({ state: "hidden" });
  await page.goForward();
  await dialog.locator('[data-integration-detail="github"]').waitFor();
  await close();
  await page.goto(`${origin}/integrations#sentry`);
  await dialog.locator('[data-integration-detail="sentry"]').waitFor();
  await close();

  for (const colorScheme of ["light", "dark"]) {
    await page.emulateMedia({ colorScheme });
    await page.setViewportSize({ width: 1440, height: 1080 });
    await screenshot(`catalog-${colorScheme}`);
    await open("github");
    await screenshot(`details-${colorScheme}`);
    await close();
    for (const width of [390, 320]) {
      await page.setViewportSize({ width, height: 844 });
      await noOverflow();
      await screenshot(`catalog-${colorScheme}-${width}`);
      await open("github");
      await feature("create-issue").locator("summary").click();
      await noOverflow("#integration-dialog");
      await screenshot(`details-${colorScheme}-${width}`);
      await close();
      await page
        .getByRole("button", { name: "List view", exact: true })
        .click();
      await noOverflow();
      await page
        .getByRole("button", { name: "Grid view", exact: true })
        .click();
    }
  }

  const noJS = await browser.newContext({ javaScriptEnabled: false });
  const fallback = await noJS.newPage();
  await fallback.goto(`${origin}/integrations`);
  assert.equal(
    await fallback.locator("[data-integration-controls]:visible").count(),
    0,
  );
  await fallback.getByRole("link", { name: "GitHub", exact: true }).click();
  assert.equal(new URL(fallback.url()).pathname, "/integrations/github");
  assert.equal(await fallback.locator(".integration-endpoint").count(), 3);
  await fallback
    .locator(".integration-endpoint")
    .first()
    .locator("summary")
    .click();
  assert.equal(
    await fallback.locator(".integration-endpoint[open]").count(),
    1,
  );
  await noJS.close();
  assert.deepEqual(errors, [], "No browser errors");
  assert.deepEqual(calls, [], "Discovery must not execute integrations");
  console.log(
    `Integration portal browser checks passed. Screenshots: ${screenshots}`,
  );
} finally {
  if (browser) await browser.close();
  if (child && child.exitCode === null) {
    const exit = once(child, "exit");
    child.kill("SIGTERM");
    await exit;
  }
  await rm(temporary, { recursive: true, force: true });
}

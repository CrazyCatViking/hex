import assert from "node:assert/strict";
import { mkdir, readFile } from "node:fs/promises";
import { createServer } from "node:http";
import { join } from "node:path";
import { chromium } from "playwright";

const root = process.argv[2] || "/tmp/opencode/hex-integrations-prototype";
const screenshots = "/tmp/opencode/hex-integrations-review";
await mkdir(screenshots, { recursive: true });
const server = createServer(async (request, response) => {
  const pathname = new URL(request.url, "http://localhost").pathname;
  const file = pathname === "/" ? "index.html" : pathname.slice(1);
  if (
    !["index.html", "portal.css", "styles.css", "prototype.js"].includes(file)
  ) {
    response.writeHead(404).end();
    return;
  }
  try {
    const content = await readFile(join(root, file));
    const type = file.endsWith(".css")
      ? "text/css"
      : file.endsWith(".js")
        ? "text/javascript"
        : "text/html";
    response.writeHead(200, { "Content-Type": type }).end(content);
  } catch (error) {
    console.error(error);
    response.writeHead(500).end();
  }
});
await new Promise((resolve) => server.listen(0, "127.0.0.1", resolve));
const origin = `http://127.0.0.1:${server.address().port}`;
let browser;
const errors = [];

async function noOverflow(page, selector = "html") {
  assert.equal(
    await page
      .locator(selector)
      .evaluate((element) => element.scrollWidth > element.clientWidth + 1),
    false,
    `${selector} must not overflow horizontally`,
  );
}

async function open(page, name) {
  await page.getByRole("button", { name, exact: true }).click();
  await page.locator("#detail-dialog[open]").waitFor();
  assert.equal(await page.locator("#detail-title").textContent(), name);
}

try {
  browser = await chromium.launch({ headless: true });
  const context = await browser.newContext({
    viewport: { width: 1440, height: 1080 },
    colorScheme: "light",
  });
  const page = await context.newPage();
  page.on("pageerror", (error) => errors.push(error.message));
  page.on("request", (request) =>
    assert.ok(request.url().startsWith(origin), "No live or external requests"),
  );
  await page.goto(origin);
  await page.locator(".integration-card").first().waitFor();
  assert.equal(
    await readFile(join(root, "portal.css"), "utf8"),
    await readFile(
      new URL("../../server/portal/portal.css", import.meta.url),
      "utf8",
    ),
  );
  assert.equal(await page.locator(".integration-card").count(), 6);
  assert.equal(
    await page.locator(".integration-card .fact").count(),
    30,
    "All five facts on all six cards",
  );
  assert.equal(await page.getByRole("textbox").count(), 0, "No site lookup");
  await noOverflow(page);
  await page.screenshot({
    path: `${screenshots}/catalog-desktop.png`,
    fullPage: true,
  });

  await page
    .getByRole("button", { name: "Granted to you", exact: true })
    .click();
  assert.equal(await page.locator(".integration-card").count(), 5);
  await page
    .getByRole("button", { name: "For automations", exact: true })
    .click();
  assert.equal(await page.locator(".integration-card").count(), 4);
  await page.getByRole("button", { name: "Linked login", exact: true }).click();
  assert.equal(await page.locator(".integration-card").count(), 2);
  await page
    .getByRole("button", { name: "All integrations", exact: false })
    .click();
  await page.getByRole("button", { name: "List view", exact: true }).click();
  assert.equal(
    await page.locator("#catalog").getAttribute("data-layout"),
    "list",
  );
  await noOverflow(page);
  await page.screenshot({
    path: `${screenshots}/catalog-list.png`,
    fullPage: true,
  });
  await page.getByRole("button", { name: "Grid view", exact: true }).click();

  await open(page, "GitHub");
  assert.match(
    await page.locator("#detail-content").innerText(),
    /2 of 3 permissions granted/,
  );
  assert.equal(await page.locator(".endpoint").count(), 3);
  await page.locator(".endpoint").last().locator("summary").click();
  assert.match(
    await page.locator(".endpoint[open]").innerText(),
    /github.create-issue/,
  );
  assert.match(
    await page.locator(".endpoint[open]").innerText(),
    /Ask a platform administrator/,
  );
  assert.equal(await page.locator(".endpoint[open] pre").count(), 2);
  await noOverflow(page, "#detail-dialog");
  await page.screenshot({
    path: `${screenshots}/github-details.png`,
    fullPage: true,
  });
  await page.keyboard.press("Escape");
  await page.locator("#detail-dialog[open]").waitFor({ state: "detached" });
  assert.equal(
    await page.evaluate(() => document.activeElement.textContent),
    "GitHub",
    "Restore focus to invoking card",
  );

  await open(page, "Azure DevOps");
  assert.match(
    await page.locator("#detail-content").innerText(),
    /Not supported/,
  );
  assert.equal(
    await page
      .locator(".endpoint-status")
      .filter({ hasText: "Link login" })
      .count(),
    2,
  );
  await page.screenshot({
    path: `${screenshots}/linked-login-details.png`,
    fullPage: true,
  });
  await page.getByRole("button", { name: "Preview linking login" }).click();
  assert.match(
    await page.locator("#detail-content").innerText(),
    /Linked login · Connected/,
  );
  assert.equal(
    await page
      .locator(".endpoint-status")
      .filter({ hasText: "Permission needed" })
      .count(),
    1,
    "Linking must not grant missing permissions",
  );
  assert.equal(
    await page
      .locator(".endpoint-status .status")
      .filter({ hasText: /^Granted to you$/ })
      .count(),
    2,
  );
  await page.getByRole("button", { name: "Close details" }).click();
  await page.locator("#detail-dialog[open]").waitFor({ state: "detached" });

  await open(page, "HubSpot");
  assert.match(
    await page.locator("#detail-content").innerText(),
    /Restricted · approval required/,
  );
  assert.equal(
    await page
      .locator(".endpoint-status")
      .filter({ hasText: "Permission needed" })
      .count(),
    3,
  );
  await page.keyboard.press("Escape");
  await page.locator("#detail-dialog[open]").waitFor({ state: "detached" });
  await open(page, "Sentry");
  assert.match(
    await page.locator("#detail-content").innerText(),
    /3 of 3 permissions granted/,
  );
  assert.match(
    await page.locator("#detail-content").innerText(),
    /Site approval also required/,
  );
  await page.keyboard.press("Escape");
  await page.locator("#detail-dialog[open]").waitFor({ state: "detached" });

  await page.getByRole("button", { name: "About this prototype" }).click();
  assert.equal(await page.locator("#notes-dialog li").count(), 4);
  await page.keyboard.press("Escape");
  for (const state of ["loading", "empty", "error"]) {
    await page.getByLabel("Preview state").selectOption(state);
    if (state === "loading") {
      assert.equal(
        await page.locator("#catalog").getAttribute("aria-busy"),
        "true",
      );
    } else {
      assert.equal(await page.locator(".catalog-state").count(), 1);
      await page.locator("[data-reset]").click();
      assert.equal(await page.locator(".integration-card").count(), 6);
    }
  }
  await page.getByLabel("Preview state").selectOption("normal");

  await page.goto(`${origin}/#sentry`);
  await page.locator("#detail-dialog[open]").waitFor();
  assert.equal(
    await page.locator("#detail-title").innerText(),
    "Sentry",
    "Direct detail link",
  );
  await page.keyboard.press("Escape");
  await page.locator("#detail-dialog[open]").waitFor({ state: "detached" });

  await page.setViewportSize({ width: 390, height: 844 });
  await noOverflow(page);
  await page.screenshot({
    path: `${screenshots}/catalog-mobile.png`,
    fullPage: true,
  });
  await open(page, "GitHub");
  await page.locator(".endpoint").last().locator("summary").click();
  await noOverflow(page, "#detail-dialog");
  await page.screenshot({
    path: `${screenshots}/details-mobile.png`,
    fullPage: true,
  });
  await page.keyboard.press("Escape");
  await page.locator("#detail-dialog[open]").waitFor({ state: "detached" });
  await page.getByRole("button", { name: "List view", exact: true }).click();
  await noOverflow(page);
  await page.setViewportSize({ width: 320, height: 740 });
  await noOverflow(page);
  await page.getByRole("button", { name: "Grid view", exact: true }).click();
  await noOverflow(page);

  await page.setViewportSize({ width: 1440, height: 1080 });
  await page.emulateMedia({ colorScheme: "dark" });
  assert.equal(
    await page.evaluate(() =>
      getComputedStyle(document.documentElement)
        .getPropertyValue("--bg")
        .trim(),
    ),
    "#101a21",
  );
  await page.screenshot({
    path: `${screenshots}/catalog-dark.png`,
    fullPage: true,
  });
  await open(page, "Sentry");
  await page.screenshot({
    path: `${screenshots}/details-dark.png`,
    fullPage: true,
  });
  assert.deepEqual(errors, [], "No browser JavaScript errors");
  console.log(
    `Passed: catalog facts, filters, layouts, endpoint grants, contracts, sample linking, site restrictions, focus, deep links, states, mobile and dark mode. Screenshots: ${screenshots}`,
  );
} finally {
  if (browser) await browser.close();
  await new Promise((resolve, reject) =>
    server.close((error) => (error ? reject(error) : resolve())),
  );
}

import assert from "node:assert/strict";
import { mkdir } from "node:fs/promises";
import { chromium } from "playwright";

const origin = process.env.HEX_PORTAL_URL;
if (!origin)
  throw new Error(
    "Run with HEX_PORTAL_BROWSER=1 go test ./server -run '^TestPortalBrowser$' -v",
  );
const headers = Object.fromEntries(
  Object.entries(JSON.parse(process.env.HEX_PORTAL_HEADERS)).map(
    ([name, values]) => [name, values.join(",")],
  ),
);
const browser = await chromium.launch({ headless: true });
const errors = [];
const output = "/tmp/opencode/hex-portal-implementation";
await mkdir(output, { recursive: true });
try {
  const context = await browser.newContext({
    extraHTTPHeaders: headers,
    viewport: { width: 1440, height: 1050 },
  });
  const page = await context.newPage();
  page.on("pageerror", (error) => errors.push(error.message));
  await page.goto(origin);
  await page
    .getByRole("heading", { name: "Pinned apps", exact: true })
    .waitFor();
  assert.equal(await page.locator(".site-card").count(), 4);
  assert.equal(
    await page
      .locator(".site-card")
      .first()
      .getByRole("link", { name: "Manage", exact: true })
      .count(),
    1,
  );
  assert.equal(
    await page
      .locator(".site-card")
      .first()
      .getByRole("link", { name: "Analytics", exact: true })
      .count(),
    1,
    "Analytics is available without loading traffic",
  );
  assert.equal(
    await page
      .locator(".site-card")
      .first()
      .getByRole("button", { name: "Copy link", exact: true })
      .count(),
    1,
  );
  await page
    .getByRole("button", { name: "Pin app-0000", exact: true })
    .first()
    .click();
  await page.locator('[aria-labelledby="pinned-heading"] .site-card').waitFor();
  await page.reload();
  assert.equal(
    await page.locator('[aria-labelledby="pinned-heading"] .site-card').count(),
    1,
  );
  // Typing replaces home sections with a unified result, including sites beyond
  // the first page, and keeps the search input focused through the HTMX swap.
  await page.getByRole("searchbox").fill("app-0155");
  await page.waitForURL("**/library?search=app-0155");
  await page
    .getByRole("heading", { name: "App library", exact: true })
    .waitFor();
  assert.equal(
    await page.evaluate(() => document.activeElement.id),
    "hero-search",
    "Search keeps keyboard focus after replacing home",
  );
  assert.equal(await page.locator("#site-list .site-card").count(), 1);
  assert.equal(
    await page.locator("#site-list .site-link").textContent(),
    "app-0155",
  );
  assert.equal(await page.locator("[aria-labelledby=mine-heading]").count(), 0);
  await page.goBack();
  await page
    .getByRole("heading", { name: "Pinned apps", exact: true })
    .waitFor();
  await page.goto(`${origin}/library?sort=name`);
  assert.equal(await page.locator("#site-list .site-card").count(), 12);
  await page.getByRole("link", { name: "Next →", exact: true }).click();
  assert.ok(
    (await page.locator(".portal-pagination").textContent()).includes(
      "Showing 13–24 of 156",
    ),
  );
  await page.getByRole("searchbox").fill("app-0155");
  await page.waitForURL(/search=app-0155/);
  assert.equal(await page.locator("#site-list .site-card").count(), 1);
  await page.getByRole("button", { name: "Pin app-0155", exact: true }).click();
  await page.locator('[data-pin="app-0155"][aria-pressed="true"]').waitFor();
  await page
    .locator(".library-scopes")
    .getByRole("link", { name: "Pinned", exact: true })
    .click();
  // Scope links retain the query, so this pin is the only matching shortcut.
  assert.equal(await page.locator("#site-list .site-card").count(), 1);
  await page.goto(`${origin}/library?scope=pinned`);
  assert.equal(await page.locator("#site-list .site-card").count(), 2);
  await page.getByLabel("View", { exact: true }).selectOption("list");
  await page.locator("#site-list.site-list").waitFor();
  assert.equal(await page.locator("#site-list .site-row").count(), 2);
  await page.getByRole("button", { name: "Pin app-0155", exact: true }).click();
  await page.locator('[data-pin="app-0155"]').waitFor({ state: "detached" });
  // Record a real link click without navigating off the fixture platform.
  await page.evaluate(() =>
    document.addEventListener("click", (event) => {
      if (event.target.closest("[data-open-site]")) event.preventDefault();
    }),
  );
  const recorded = page.waitForResponse(
    (response) =>
      response.url().includes("/preferences/recent/") &&
      response.status() === 200,
  );
  await page.locator("#site-list .site-title").click();
  await recorded;
  await page.goto(origin);
  await page
    .getByRole("heading", { name: "Recently opened", exact: true })
    .waitFor();
  await page.screenshot({ path: `${output}/home-desktop.png`, fullPage: true });
  await page.goto(`${origin}/manage`);
  assert.equal(await page.locator("#manage-list > li").count(), 12);
  await page.getByRole("searchbox").fill("app-0155");
  await page.waitForURL(/q=app-0155/);
  assert.equal(await page.locator("#manage-list > li").count(), 1);
  await page.locator("#manage-list .site-title").click();
  assert.ok(page.url().endsWith("/manage/app-0155"));
  assert.equal(await page.locator("dialog[open]").count(), 0);
  await page
    .getByRole("link", { name: "Analytics", exact: true })
    .first()
    .click();
  assert.ok(page.url().includes("tab=analytics"));
  for (const route of ["/", "/library", "/library?mode=list", "/manage"]) {
    await page.goto(`${origin}${route}`);
    await page.setViewportSize({ width: 390, height: 844 });
    assert.ok(
      await page.evaluate(
        () => document.documentElement.scrollWidth <= innerWidth + 1,
      ),
      `mobile overflow on ${route}`,
    );
    await page.emulateMedia({ colorScheme: "dark" });
    assert.ok(
      await page.evaluate(
        () => document.documentElement.scrollWidth <= innerWidth + 1,
      ),
    );
    const name =
      route === "/"
        ? "home"
        : route.includes("mode")
          ? "library-list"
          : route.slice(1);
    await page.screenshot({
      path: `${output}/${name}-mobile-dark.png`,
      fullPage: true,
    });
  }
  // Network errors and gateway sign-in HTML must preserve current results.
  await page.goto(`${origin}/library`);
  await page.route("**/library?**", (route) =>
    route.fulfill({
      status: 200,
      contentType: "text/html",
      body: "<!doctype html><h1>Sign in</h1>",
    }),
  );
  await page.getByRole("searchbox").fill("expired-session");
  await page
    .getByRole("alert")
    .filter({ hasText: "session may have expired" })
    .waitFor();
  assert.equal(await page.locator("#site-list .site-card").count(), 12);
  assert.equal(errors.length, 0, errors.join("\n"));
  await context.close();
  const noJS = await browser.newContext({
    javaScriptEnabled: false,
    extraHTTPHeaders: headers,
  });
  const plain = await noJS.newPage();
  await plain.goto(`${origin}/library?search=app-0155`);
  assert.equal(await plain.locator("#site-list .site-card").count(), 1);
  await plain.goto(`${origin}/library`);
  await plain.getByRole("link", { name: "Next →", exact: true }).click();
  assert.ok(plain.url().includes("page=2"));
  await noJS.close();
  console.log(
    `Production portal: pin persistence, shortcuts, unified search, paging, management handoff, history, error handling, no-JS and mobile/dark layouts passed. Screenshots: ${output}`,
  );
} finally {
  await browser.close();
}

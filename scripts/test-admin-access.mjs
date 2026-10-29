import assert from "node:assert/strict";
import { chromium } from "playwright";

const origin = process.env.HEX_PORTAL_URL;
const headers = Object.fromEntries(
  Object.entries(JSON.parse(process.env.HEX_PORTAL_HEADERS)).map(
    ([name, values]) => [name, values.join(",")],
  ),
);
const browser = await chromium.launch();
try {
  const context = await browser.newContext({ extraHTTPHeaders: headers });
  const page = await context.newPage();
  const errors = [];
  page.on("pageerror", (error) => errors.push(error.message));
  await page.goto(`${origin}/admin/sites`);
  assert.equal(await page.locator(".site-row").count(), 12);
  assert.equal(await page.locator("img[data-site-icon]").count(), 0);
  await page.getByRole("searchbox").fill("app-0155");
  await page.waitForURL(/q=app-0155/);
  assert.equal(await page.locator(".site-row").count(), 1);
  await page.getByRole("link", { name: "Manage", exact: true }).click();
  await page
    .getByText("You do not currently have management access.", { exact: false })
    .waitFor();
  assert.equal(await page.locator(".site-header").count(), 0);
  await page
    .getByLabel("Reason", { exact: true })
    .fill("Investigate a reported support issue");
  await page
    .getByRole("button", { name: "Grant temporary access", exact: true })
    .click();
  await page.waitForURL("**/manage/app-0155?tab=admin-access");
  await page
    .getByRole("link", { name: "Back to management", exact: true })
    .click();
  await page
    .getByText("Temporary admin access is active.", { exact: true })
    .waitFor();
  assert.equal(await page.locator(".site-header").count(), 1);
  const denied = await page.request.get(
    `${origin}/api/sites/app-0155/db/private`,
  );
  assert.equal(
    denied.status(),
    403,
    "Management alone must not admit app/data access",
  );
  await page.getByRole("button", { name: "End access", exact: true }).click();
  await page
    .getByText("You do not currently have management access.", { exact: false })
    .waitFor();
  await page
    .getByLabel("Access needed", { exact: true })
    .selectOption("app-read");
  await page
    .getByLabel("Reason", { exact: true })
    .fill("Read diagnostics without changing records");
  await page
    .getByRole("button", { name: "Grant temporary access", exact: true })
    .click();
  await page.waitForURL("**/manage/app-0155?tab=admin-access");
  await page
    .getByText("Read diagnostics without changing records", { exact: true })
    .waitFor();
  assert.equal(
    (
      await page.request.get(`${origin}/api/sites/app-0155/db/private`)
    ).status(),
    200,
  );
  assert.equal(
    (
      await page.request.post(`${origin}/api/sites/app-0155/db/private`, {
        headers: { "X-Hex-Request": "1" },
        data: { test: true },
      })
    ).status(),
    403,
  );
  assert.equal(
    await page
      .getByRole("link", { name: "Back to management", exact: true })
      .count(),
    0,
  );
  await page.getByRole("button", { name: "End access", exact: true }).click();
  await page
    .getByRole("button", { name: "End access", exact: true })
    .waitFor({ state: "detached" });
  assert.equal(
    (
      await page.request.get(`${origin}/api/sites/app-0155/db/private`)
    ).status(),
    403,
  );
  await page.goto(`${origin}/admin`);
  await page.locator(".metric-value").first().waitFor();
  assert.equal(await page.locator("img[data-site-icon]").count(), 0);
  await page.setViewportSize({ width: 390, height: 844 });
  for (const route of ["/admin/sites", "/manage/app-0155?tab=admin-access"]) {
    await page.goto(origin + route);
    assert.equal(
      await page.evaluate(
        () => document.documentElement.scrollWidth <= innerWidth + 1,
      ),
      true,
      `overflow on ${route}`,
    );
  }
  assert.deepEqual(errors, []);
  const noJS = await browser.newContext({
    javaScriptEnabled: false,
    extraHTTPHeaders: headers,
  });
  const staticPage = await noJS.newPage();
  await staticPage.goto(`${origin}/admin`);
  await staticPage
    .getByRole("link", { name: "Load this report", exact: true })
    .click();
  assert.ok((await staticPage.locator(".metric-value").count()) > 0);
  await noJS.close();
  console.log(
    "Admin browser passed: directory paging/search, explicit management/read access, revocation, reports, mobile and no-JS reporting.",
  );
} finally {
  await browser.close();
}

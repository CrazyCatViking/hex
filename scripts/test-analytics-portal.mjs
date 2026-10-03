import assert from "node:assert/strict";
import { chromium } from "playwright";

export async function verifyAnalyticsPortal(port) {
  const browser = await chromium.launch();
  try {
    const page = await browser.newPage();
    const errors = [];
    page.on("pageerror", (error) => errors.push(error.message));
    const base = `http://localhost:${port}`;
    await page.goto(`http://demo.localhost:${port}/`);
    await page.goto(`${base}/admin`);
    assert.equal(await page.locator("h1").textContent(), "Platform overview");
    assert.equal(
      await page
        .locator("dt", { hasText: /^Published sites$/ })
        .locator("..")
        .locator("dd")
        .textContent(),
      "1",
    );
    assert.ok((await page.locator(".analytics-chart rect").count()) > 0);
    await page.getByRole("link", { name: "Sites", exact: true }).click();
    await page.locator('input[name="q"]').fill("does-not-exist");
    const filtered = page.waitForResponse(
      (response) =>
        response.url().includes("/admin/sites?") &&
        response.request().headers()["hx-request"] === "true",
    );
    await page.getByRole("button", { name: "Apply", exact: true }).click();
    assert.equal((await filtered).status(), 200);
    await page.getByText("No sites match these filters.").waitFor();
    await page.locator('input[name="q"]').fill("demo");
    const refiltered = page.waitForResponse(
      (response) =>
        response.url().includes("/admin/sites?") &&
        response.request().headers()["hx-request"] === "true",
    );
    await page.getByRole("button", { name: "Apply", exact: true }).click();
    await refiltered;
    await page.locator('a[href^="/admin/sites/demo?"]').first().click();
    assert.match(await page.locator("h1").textContent(), /Site analytics/);
    const today = new Date();
    const yesterday = new Date(today);
    yesterday.setUTCDate(yesterday.getUTCDate() - 1);
    await page
      .locator('input[name="from"]')
      .fill(yesterday.toISOString().slice(0, 10));
    await page
      .locator('input[name="until"]')
      .fill(today.toISOString().slice(0, 10));
    const ranged = page.waitForResponse(
      (response) =>
        response.url().includes("/admin/sites/demo?") &&
        response.request().headers()["hx-request"] === "true",
    );
    await page.getByRole("button", { name: "Apply", exact: true }).click();
    await ranged;
    await page.waitForFunction(
      () => document.querySelectorAll(".analytics-chart rect").length === 2,
    );
    await page.goto(`${base}/manage/demo?tab=analytics`);
    assert.equal(await page.locator("#site-analytics").count(), 1);
    await page
      .locator('input[name="from"]')
      .fill(yesterday.toISOString().slice(0, 10));
    await page
      .locator('input[name="until"]')
      .fill(today.toISOString().slice(0, 10));
    const siteRange = page.waitForResponse((response) =>
      response.url().includes("/api/hex/manage/sites/demo/analytics?"),
    );
    await page.getByRole("button", { name: "Apply", exact: true }).click();
    assert.equal((await siteRange).status(), 200);
    await page.waitForFunction(
      () =>
        document.querySelectorAll("#site-analytics .analytics-chart rect")
          .length === 2,
    );
    await page.setViewportSize({ width: 390, height: 844 });
    for (const path of [
      "/admin",
      "/admin/sites",
      "/admin/users",
      "/manage/demo?tab=analytics",
    ]) {
      await page.goto(base + path);
      assert.equal(
        await page.evaluate(
          () => document.documentElement.scrollWidth <= window.innerWidth,
        ),
        true,
        `mobile overflow on ${path}`,
      );
    }
    assert.deepEqual(errors, []);
    const noJS = await browser.newContext({ javaScriptEnabled: false });
    const staticPage = await noJS.newPage();
    await staticPage.goto(`${base}/admin/sites?q=does-not-exist`);
    assert.match(
      await staticPage.locator("main").textContent(),
      /No sites match/,
    );
    await staticPage.goto(`${base}/manage/demo?tab=analytics`);
    assert.equal(await staticPage.locator("#site-analytics").count(), 1);
    console.log(
      "Analytics browser passed: dashboard, HTMX filters/date ranges, site analytics, mobile layout and no-JavaScript browsing.",
    );
  } finally {
    await browser.close();
  }
}

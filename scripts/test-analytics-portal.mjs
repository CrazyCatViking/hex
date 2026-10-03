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
    // Collection is asynchronous; wait for that navigation before testing cards.
    for (let attempt = 0; attempt < 50; attempt++) {
      const report = await (
        await page.request.get(`${base}/api/hex/admin/analytics?site=demo`)
      ).json();
      if (report.traffic.pageViews > 0) break;
      await page.waitForTimeout(50);
    }
    await page.goto(base);
    assert.match(
      await page.locator("#catalog .site-card-traffic").textContent(),
      /[1-9]\d* page views?[\s\S]*all time/,
    );
    const popular = page.waitForResponse(
      (response) =>
        response.url().includes("/api/hex/catalog?") &&
        response.url().includes("sort=popular"),
    );
    await page.locator("#sort").selectOption("popular");
    await popular;
    const noViews = page.waitForResponse(
      (response) =>
        response.url().includes("/api/hex/catalog?") &&
        response.url().includes("traffic=unvisited"),
    );
    await page.locator("#traffic").selectOption("unvisited");
    await noViews;
    await page.waitForFunction(
      () => document.querySelectorAll("#catalog .site-card").length === 0,
    );
    const visited = page.waitForResponse(
      (response) =>
        response.url().includes("/api/hex/catalog?") &&
        response.url().includes("traffic=visited"),
    );
    await page.locator("#traffic").selectOption("visited");
    await visited;
    await page.waitForFunction(
      () => document.querySelectorAll("#catalog .site-card").length === 1,
    );
    await page.waitForFunction(
      () =>
        document.querySelector(".site-card img[data-site-icon]")?.naturalWidth >
        0,
    );
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
    const appIcon = await page.request.get(`${base}/api/hex/sites/demo/icon`);
    assert.equal(appIcon.status(), 200);
    assert.match(await appIcon.text(), /demo-app-icon/);
    await page.waitForFunction(() =>
      [...document.querySelectorAll("img[data-site-icon]")].some(
        (image) => image.complete && image.naturalWidth > 0,
      ),
    );
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
    assert.match(
      await page.locator(".site-visitors").textContent(),
      /Local Developer/,
    );
    assert.ok(
      (await page
        .locator('[aria-label="All-time site traffic"] dd')
        .first()
        .textContent()) !== "0",
    );
    await page.waitForFunction(
      () =>
        document.querySelector(".site-header img[data-site-icon]")
          ?.naturalWidth > 0,
    );
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
    const visitorSearch = page.waitForResponse(
      (response) =>
        response.url().includes("/api/hex/manage/sites/demo/analytics?") &&
        response.url().includes("visitor-q="),
    );
    await page.locator('input[name="visitor-q"]').fill("no-such-visitor");
    await page.getByRole("button", { name: "Apply", exact: true }).click();
    await visitorSearch;
    await page.getByText("No visitors match this search.").waitFor();
    await page.setViewportSize({ width: 390, height: 844 });
    for (const path of [
      "/admin",
      "/",
      "/manage",
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
    await page.route("**/api/hex/sites/demo/icon", (route) => route.abort());
    await page.goto(`${base}/manage/demo`);
    await page.waitForFunction(
      () => !document.querySelector(".site-header img[data-site-icon]"),
    );
    assert.equal(
      await page.locator(".site-header .site-icon-initial").isVisible(),
      true,
    );
    await page.unroute("**/api/hex/sites/demo/icon");
    const noJS = await browser.newContext({ javaScriptEnabled: false });
    const staticPage = await noJS.newPage();
    await staticPage.goto(`${base}/admin/sites?q=does-not-exist`);
    assert.match(
      await staticPage.locator("main").textContent(),
      /No sites match/,
    );
    await staticPage.goto(`${base}/manage/demo?tab=analytics`);
    assert.equal(await staticPage.locator("#site-analytics").count(), 1);
    assert.match(
      await staticPage.locator(".site-visitors").textContent(),
      /Local Developer/,
    );
    await staticPage.goto(`${base}/?sort=popular&traffic=visited`);
    assert.equal(await staticPage.locator("#catalog .site-card").count(), 1);
    await staticPage.goto(`${base}/manage/demo?tab=analytics`);
    await staticPage.waitForFunction(
      () =>
        document.querySelector(".site-header img[data-site-icon]")
          ?.naturalWidth > 0,
    );
    console.log(
      "Analytics browser passed: dashboard, HTMX filters/date ranges, site analytics, mobile layout and no-JavaScript browsing.",
    );
  } finally {
    await browser.close();
  }
}

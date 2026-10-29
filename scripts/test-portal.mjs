import assert from "node:assert/strict";
import { chromium } from "playwright";

export async function verifyPortal(port) {
  const browser = await chromium.launch();
  try {
    const context = await browser.newContext();
    const page = await context.newPage();
    const errors = [];
    page.on("pageerror", (error) => errors.push(error.message));
    const response = await page.goto(`http://localhost:${port}/`);
    assert.equal(response.status(), 200);
    assert.equal(
      await page.locator("#platform-name").textContent(),
      "Test Hex",
    );
    assert.equal(await page.locator(".site-card").count(), 2);
    const overview = await (
      await page.request.get(`http://localhost:${port}/api/hex/overview`, {
        headers: { "X-Hex-Request": "1" },
      })
    ).json();
    assert.deepEqual(overview.statistics, {
      sites: 2,
      authors: 1,
      updatedRecently: 1,
    });
    await page.locator("#hero-search").fill("reports");
    await page.waitForFunction(
      () => document.querySelectorAll(".site-card").length === 1,
    );
    assert.equal(
      await page.locator(".site-link").textContent(),
      "Team dashboard",
    );
    await page.locator("#hero-search").fill("does-not-exist");
    await page.waitForFunction(
      () => document.querySelectorAll(".site-card").length === 0,
    );
    assert.match(
      await page.locator("#site-list").textContent(),
      /No apps match/,
    );
    await page.locator("#hero-search").fill("");
    await page.waitForFunction(
      () => document.querySelectorAll(".site-card").length === 2,
    );
    await page.locator('select[name="sort"]').selectOption("name");
    await page.waitForFunction(() =>
      document.querySelector(".site-link").href.includes("other.localhost"),
    );
    const refreshed = page.waitForResponse((response) =>
      response.url().includes("/library?"),
    );
    await page.getByRole("button", { name: "Search", exact: true }).click();
    assert.equal((await refreshed).status(), 200);
    assert.equal(await page.locator("#page-error").isVisible(), false);
    await page.goto(`http://localhost:${port}/start`);
    for (const os of ["macos", "windows", "linux"]) {
      await page.locator("#os").selectOption(os);
      assert.match(
        await page.locator("#install-command").textContent(),
        new RegExp(`install-hex-${os}`),
      );
      const downloading = page.waitForEvent("download");
      await page.locator("#download-installer").click();
      const download = await downloading;
      assert.equal(
        download.suggestedFilename(),
        `install-hex-${os}.${os === "windows" ? "ps1" : "sh"}`,
      );
      assert.equal(await download.failure(), null);
    }
    await page.setViewportSize({ width: 390, height: 844 });
    for (const path of ["/", "/library", "/start"]) {
      await page.goto(`http://localhost:${port}${path}`);
      const overflow = await page.evaluate(() =>
        [...document.querySelectorAll("body *")]
          .filter(
            (element) =>
              element.getBoundingClientRect().right > window.innerWidth,
          )
          .map(
            (element) =>
              `${element.tagName}.${element.className}: ${element.getBoundingClientRect().right}`,
          ),
      );
      assert.equal(
        await page.evaluate(
          () => document.documentElement.scrollWidth <= window.innerWidth,
        ),
        true,
        `mobile content on ${path} overflows the viewport: ${overflow.join(", ")}`,
      );
    }
    assert.deepEqual(errors, []);
    const noJavaScript = await browser.newContext({ javaScriptEnabled: false });
    const staticPage = await noJavaScript.newPage();
    await staticPage.goto(`http://localhost:${port}/?search=reports`);
    assert.equal(await staticPage.locator(".site-card").count(), 1);
    assert.equal(
      await staticPage.locator(".site-link").textContent(),
      "Team dashboard",
    );
    console.log(
      "Portal passed: Go-rendered HTML, unified HTMX library search/sort/refresh, overview API statistics, installer downloads, mobile layout, and no-JavaScript browsing.",
    );
  } finally {
    await browser.close();
  }
}

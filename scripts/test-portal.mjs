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
    assert.equal(await page.locator("#site-count").textContent(), "2");
    assert.equal(await page.locator(".site-card").count(), 2);
    assert.equal(await page.locator("#author-count").textContent(), "1");
    assert.equal(await page.locator("#recent-count").textContent(), "1");
    await page.locator("#search").fill("reports");
    await page.waitForFunction(
      () => document.querySelectorAll(".site-card").length === 1,
    );
    assert.equal(
      await page.locator(".site-link").textContent(),
      "Team dashboard",
    );
    await page.locator("#search").fill("does-not-exist");
    await page.waitForFunction(
      () => document.querySelectorAll(".site-card").length === 0,
    );
    assert.match(
      await page.locator("#site-list").textContent(),
      /No apps match/,
    );
    await page.locator("#search").fill("");
    await page.waitForFunction(
      () => document.querySelectorAll(".site-card").length === 2,
    );
    await page.locator("#sort").selectOption("name");
    await page.waitForFunction(
      () => document.querySelector(".site-label").textContent === "other",
    );
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
    const refreshed = page.waitForResponse((response) =>
      response.url().includes("/api/hex/catalog"),
    );
    await page.locator("#refresh").click();
    assert.equal((await refreshed).status(), 200);
    assert.equal(await page.locator("#catalog-error").isVisible(), false);
    await page.setViewportSize({ width: 390, height: 844 });
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
      `mobile content overflows the viewport: ${overflow.join(", ")}`,
    );
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
      "Portal passed: Go-rendered HTML, HTMX search/sort/refresh, statistics, installer downloads, mobile layout, and no-JavaScript browsing.",
    );
  } finally {
    await browser.close();
  }
}

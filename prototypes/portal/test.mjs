import assert from "node:assert/strict";
import { readFile, mkdir } from "node:fs/promises";
import { createServer } from "node:http";
import { join } from "node:path";
import { chromium } from "playwright";

const root = process.argv[2] || "/tmp/opencode/hex-portal-prototypes";
const screenshots = "/tmp/opencode/hex-portal-review";
await mkdir(screenshots, { recursive: true });
const server = createServer(async (request, response) => {
  try {
    const url = new URL(request.url, "http://localhost");
    const path = url.pathname.endsWith("/")
      ? `${url.pathname}index.html`
      : url.pathname;
    if (
      !/^\/(launchpad|library|operations)\/(index\.html|styles\.css|portal\.css|themes\.css|prototype\.js)$/.test(
        path,
      )
    ) {
      response.writeHead(404).end();
      return;
    }
    const content = await readFile(join(root, path));
    const type = path.endsWith(".css")
      ? "text/css"
      : path.endsWith(".js")
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
const browser = await chromium.launch({ headless: true });
const errors = [];
try {
  for (const concept of ["launchpad", "library", "operations"]) {
    const context = await browser.newContext({
      viewport: { width: 1440, height: 1080 },
      colorScheme: "light",
    });
    const page = await context.newPage();
    page.on("pageerror", (error) => errors.push(error.message));
    page.on("request", (request) =>
      assert.ok(
        request.url().startsWith(origin),
        "Prototype must not request live services",
      ),
    );
    await page.goto(`${origin}/${concept}/`);
    await page.locator("h1").waitFor();
    assert.equal(
      await readFile(join(root, concept, "portal.css"), "utf8"),
      await readFile(
        new URL("../../server/portal/portal.css", import.meta.url),
        "utf8",
      ),
      "Reuse the unmodified production stylesheet",
    );
    assert.equal(
      await page.locator("aside").count(),
      0,
      "No new sidebar navigation",
    );
    assert.equal(
      await page
        .locator(".primary-nav")
        .getByRole("link", { name: "Your sites", exact: true })
        .count(),
      1,
    );
    assert.equal(
      await page.evaluate(() =>
        getComputedStyle(document.documentElement)
          .getPropertyValue("--accent")
          .trim(),
      ),
      "#157d64",
    );
    await page.screenshot({
      path: `${screenshots}/${concept}-desktop.png`,
      fullPage: true,
    });
    assert.equal(
      await page.evaluate(
        () => document.documentElement.scrollWidth > innerWidth,
      ),
      false,
      `${concept}: desktop overflow`,
    );

    await page.getByRole("button", { name: "Review notes" }).click();
    assert.equal(await page.locator("dialog li").count(), 4);
    await page.keyboard.press("Escape");
    assert.equal(
      await page.locator("dialog").evaluate((dialog) => dialog.open),
      false,
    );

    if (concept === "launchpad") {
      assert.equal(await page.locator(".grid.pins .card").count(), 4);
      await page.locator("#search").fill("release maya");
      assert.equal(
        await page.locator(".result-summary strong").textContent(),
        "13 results",
      );
      assert.equal(
        await page
          .getByRole("heading", { name: "Recently opened", exact: true })
          .count(),
        0,
      );
      assert.equal(await page.locator("#content .card").count(), 12);
      assert.equal(
        await page
          .locator("#search")
          .evaluate((input) => document.activeElement === input),
        true,
      );
      await page.getByRole("link", { name: "Next →", exact: true }).click();
      assert.equal(await page.locator("#content .card").count(), 1);
      assert.ok(page.url().includes("page=2"));
      await page.goBack();
      assert.equal(await page.locator("#content .card").count(), 12);
      await page.locator("#search").fill("zzzz-no-app");
      await page
        .getByRole("button", { name: "Clear search & filters" })
        .click();
      assert.equal(
        await page.getByRole("heading", { name: "Recently opened" }).count(),
        1,
      );
      await page
        .getByRole("button", { name: "Pin Project pulse", exact: true })
        .first()
        .click();
      assert.equal(await page.locator(".pins .card").count(), 3);
      await page.reload();
      assert.equal(
        await page.locator(".pins .card").count(),
        3,
        "Pins persist across reloads",
      );
      await page
        .getByRole("button", { name: "Pin Project pulse", exact: true })
        .first()
        .click();
      assert.equal(await page.locator(".pins .card").count(), 4);
    }
    if (concept === "library") {
      assert.equal(await page.locator("#content .card").count(), 12);
      await page.locator("#search").fill("budget");
      await page
        .locator(".scope-tabs")
        .getByRole("link", { name: /Owned by you/ })
        .click();
      assert.equal(
        await page.locator(".result-summary strong").textContent(),
        "13 results",
      );
      await page.getByRole("button", { name: "☷ List view" }).click();
      assert.equal(await page.locator(".result-row").count(), 12);
      await page.getByLabel("Sort", { exact: true }).selectOption("name");
      assert.ok(page.url().includes("sort=name"));
      await page.getByRole("link", { name: "Next →", exact: true }).click();
      assert.equal(await page.locator(".result-row").count(), 1);
      await page.goto(`${origin}/library/`);
      await page
        .getByRole("button", { name: "Pin Release room", exact: true })
        .click();
      await page
        .locator(".scope-tabs")
        .getByRole("link", { name: /^Pinned/ })
        .click();
      assert.equal(
        await page.locator(".card").count(),
        5,
        "All pins appear beyond the four-card home preview",
      );
      await page
        .getByRole("button", { name: "Pin Release room", exact: true })
        .click();
      assert.equal(await page.locator(".card").count(), 4);
    }
    if (concept === "operations") {
      assert.equal(await page.locator("#manage-list > li").count(), 12);
      assert.equal(
        await page.locator(".result-summary strong").textContent(),
        "39 sites",
      );
      await page.locator("#manage-list .site-title").first().click();
      assert.equal(
        await page
          .getByRole("heading", { name: "Your existing app management page" })
          .count(),
        1,
      );
      assert.equal(
        await page.locator(".detail-tabs").count(),
        0,
        "No replacement per-app manager",
      );
      assert.ok(
        (await page.locator(".prototype-route").textContent()).includes(
          "/manage/customer-insights",
        ),
      );
      await page.keyboard.press("Escape");
      await page
        .locator("#manage-list")
        .getByRole("button", { name: "Share", exact: true })
        .first()
        .click();
      assert.ok(
        (await page.locator(".prototype-route").textContent()).includes(
          "?tab=sharing",
        ),
      );
      await page.keyboard.press("Escape");
      await page
        .locator("#manage-list")
        .getByRole("button", { name: "Analytics", exact: true })
        .first()
        .click();
      assert.ok(
        (await page.locator(".prototype-route").textContent()).includes(
          "?tab=analytics",
        ),
      );
      await page.keyboard.press("Escape");
      assert.equal(
        await page.getByRole("link", { name: /All sites/ }).count(),
        0,
      );
      await page.getByRole("link", { name: "Files", exact: true }).click();
      assert.equal(
        await page.locator(".result-summary strong").textContent(),
        "3 sites",
      );
      await page.goto(`${origin}/operations/?scope=admin`);
      assert.equal(
        await page.locator(".result-summary strong").textContent(),
        "39 sites",
        "Obsolete prototype admin URLs cannot reopen the removed admin view",
      );
    }

    await page.getByLabel("Preview state").selectOption("loading");
    assert.equal(
      await page.locator("#content").getAttribute("aria-busy"),
      "true",
    );
    await page.getByRole("button", { name: "Finish loading preview" }).click();
    assert.equal(
      await page.locator("#content").getAttribute("aria-busy"),
      null,
    );
    await page.getByLabel("Preview state").selectOption("error");
    await page.getByRole("button", { name: "Try again" }).click();
    assert.equal(await page.getByLabel("Preview state").inputValue(), "ready");

    await page.goto(`${origin}/${concept}/`);
    await page.setViewportSize({ width: 390, height: 844 });
    await page.screenshot({
      path: `${screenshots}/${concept}-mobile.png`,
      fullPage: true,
    });
    assert.equal(
      await page.evaluate(
        () => document.documentElement.scrollWidth > innerWidth,
      ),
      false,
      `${concept}: mobile overflow`,
    );
    await page.emulateMedia({ colorScheme: "dark" });
    assert.equal(
      await page.evaluate(() =>
        getComputedStyle(document.documentElement)
          .getPropertyValue("--accent")
          .trim(),
      ),
      "#61c6a3",
    );
    await page.screenshot({
      path: `${screenshots}/${concept}-mobile-dark.png`,
      fullPage: true,
    });
    await context.close();
    console.log(
      `${concept}: interactions, pagination, states, mobile and dark mode passed`,
    );
  }
  assert.deepEqual(errors, []);
  console.log(`Screenshots: ${screenshots}`);
} finally {
  await browser.close();
  await new Promise((resolve) => server.close(resolve));
}

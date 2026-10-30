import assert from "node:assert/strict";
import { spawn, execFile } from "node:child_process";
import { once } from "node:events";
import { mkdtemp, rm } from "node:fs/promises";
import { join } from "node:path";
import { fileURLToPath } from "node:url";
import { promisify } from "node:util";
import { chromium } from "playwright";

const root = fileURLToPath(new URL("../", import.meta.url));
const temporary = await mkdtemp("/tmp/opencode/hex-permission-browser-");
const exec = promisify(execFile);
let child;
let browser;
let page;
try {
  await exec(
    "go",
    [
      "build",
      "-o",
      join(temporary, "server"),
      "./scripts/testdata/permission-portal",
    ],
    { cwd: root },
  );
  child = spawn(join(temporary, "server"), {
    stdio: ["ignore", "pipe", "inherit"],
  });
  const url = await new Promise((resolve, reject) => {
    child.once("error", reject);
    child.once("exit", (code) =>
      reject(new Error(`Fixture exited before readiness: ${code}`)),
    );
    child.stdout.once("data", (data) => resolve(data.toString().trim()));
  });
  browser = await chromium.launch({ headless: true });
  page = await browser.newPage({ viewport: { width: 1440, height: 1000 } });
  page.setDefaultTimeout(7000);
  const errors = [];
  page.on("pageerror", (error) => errors.push(error.message));
  page.on("dialog", (dialog) => dialog.accept());
  const policy = async () =>
    (await page.request.get(`${url}/api/hex/sites/demo/access`)).json();
  const dialog = page.locator("[data-permission-dialog]");
  const row = (principal) =>
    page.locator(`[data-permission-row="${principal}"]`);
  const box = (principal, group, name) =>
    row(principal).getByRole("checkbox", {
      name: `${group} for ${name}`,
      exact: true,
    });
  const close = () =>
    dialog
      .getByRole("button", { name: "Close permission editor", exact: true })
      .click();
  const screenshot = async (name) => {
    if (!process.env.HEX_PORTAL_SCREENSHOTS) return;
    await page.evaluate(() => scrollTo(0, 0));
    await page.screenshot({
      path: `${process.env.HEX_PORTAL_SCREENSHOTS}-${name}.png`,
      fullPage: !(await dialog.isVisible()),
    });
  };
  const checkDesign = async (scheme, mobile = false) => {
    await page.emulateMedia({ colorScheme: scheme });
    await page.setViewportSize(
      mobile ? { width: 390, height: 844 } : { width: 1440, height: 1000 },
    );
    const design = await page.evaluate(() => {
      const style = (selector) =>
        getComputedStyle(document.querySelector(selector));
      return {
        background: style("body").getPropertyValue("--bg").trim(),
        surface: style("#site-access").backgroundColor,
        text: style("#permission-manager").color,
        buttonRadius: style("[data-new-permission-group]").borderRadius,
        cardRadius: style("#permission-groups").borderRadius,
        systemRow: style('[data-permission-row="default"] th').backgroundColor,
        overflow: document.documentElement.scrollWidth > innerWidth,
      };
    });
    // Exact tokens/shapes from the approved standalone prototype, not the
    // portal's previous green-tinted surfaces and pill-shaped controls.
    assert.deepEqual(design, {
      background: scheme === "dark" ? "#101a21" : "#f5f7f8",
      surface: scheme === "dark" ? "rgb(24, 37, 46)" : "rgb(255, 255, 255)",
      text: scheme === "dark" ? "rgb(225, 233, 237)" : "rgb(31, 43, 53)",
      buttonRadius: "7px",
      cardRadius: "11px",
      systemRow: scheme === "dark" ? "rgb(25, 44, 48)" : "rgb(249, 251, 250)",
      overflow: false,
    });
    await screenshot(`${mobile ? "mobile" : "desktop"}-${scheme}`);
  };
  const save = async (success = true) => {
    const [response] = await Promise.all([
      page.waitForResponse(
        (response) =>
          response.url().endsWith("/sharing") &&
          response.request().method() === "PUT",
      ),
      page.getByRole("button", { name: "Save changes", exact: true }).click(),
    ]);
    await response.finished();
    await page.waitForFunction(() => !document.querySelector(".htmx-request"));
    if (success) {
      await page
        .locator("#permission-manager > [role=status]")
        .filter({ hasText: "Permissions saved" })
        .waitFor();
      assert.equal(
        await page.locator("[data-permission-error]:visible").count(),
        0,
      );
      await page.locator("[data-save-permissions]:disabled").waitFor();
    } else {
      await page.locator("[data-permission-error]:visible").waitFor();
      assert.equal(
        await page
          .getByRole("button", { name: "Save changes", exact: true })
          .isEnabled(),
        true,
        "failed saves must retain the staged edits",
      );
      assert.equal(await response.headerValue("hx-reswap"), "none");
    }
  };
  const addRow = async (search, expected) => {
    await page
      .getByRole("button", { name: "+ Add person or group", exact: true })
      .click();
    const picker = dialog.getByRole("combobox", {
      name: "Search people or directory groups",
      exact: true,
    });
    await picker.fill(search);
    await dialog.getByRole("option").filter({ hasText: expected }).waitFor();
    await picker.press("ArrowDown");
    await picker.press("Enter");
    assert.equal(await dialog.isVisible(), true);
    await dialog.getByRole("button", { name: "Done", exact: true }).click();
    await dialog.waitFor({ state: "hidden" });
  };

  const verifyDirectoryPicker = async () => {
    const originalPolicy = await policy();
    const pickerPage = await browser.newPage({
      viewport: { width: 1440, height: 1000 },
    });
    pickerPage.setDefaultTimeout(7000);
    pickerPage.on("pageerror", (error) => errors.push(error.message));
    const people = Array.from({ length: 20 }, (_, i) => ({
      id: `picker-person-${i}`,
      name: `Test person ${i}`,
      email: `person${i}@example.test`,
    }));
    const groups = Array.from({ length: 20 }, (_, i) => ({
      id: `picker-group-${i}`,
      name: `Directory group ${i} with a very long name that should not overflow the picker or the viewport`,
    }));
    let receiveDelayed;
    const delayed = new Promise((resolve) => {
      receiveDelayed = resolve;
    });
    const routeDirectory = async (route) => {
      const query = new URL(route.request().url()).searchParams.get("query");
      if (query === "delayed") {
        receiveDelayed(route);
        return;
      }
      await route.fulfill({
        status: query === "error" ? 503 : 200,
        json:
          query === "empty"
            ? { people: [], groups: [] }
            : query === "single"
              ? { people: people.slice(0, 1), groups: [] }
              : query === "existing"
                ? {
                    people: [{ id: "alex", name: "Alex Andersen" }],
                    groups: [],
                  }
                : { people, groups },
      });
    };
    await pickerPage.route("**/api/hex/directory?*", routeDirectory);
    await pickerPage.goto(`${url}/manage/demo?tab=sharing`);
    await pickerPage
      .getByRole("button", { name: "+ Add person or group", exact: true })
      .click();
    const pickerDialog = pickerPage.locator("[data-permission-dialog]");
    const input = pickerDialog.getByRole("combobox");
    const options = pickerDialog.getByRole("listbox");
    const geometry = () =>
      pickerDialog.evaluate((dialog) => ({
        height: dialog.offsetHeight,
        scrollHeight: dialog.scrollHeight,
        scrollTop: dialog.scrollTop,
        top: dialog.getBoundingClientRect().top,
      }));
    await pickerDialog.getByRole("option").last().waitFor();
    await input.press("Escape");
    assert.equal(
      await pickerDialog.isVisible(),
      true,
      "Escape dismisses results before dismissing the modal",
    );
    const closed = await geometry();
    await input.fill("single");
    await pickerDialog.getByRole("option").first().waitFor();
    assert.deepEqual(
      await geometry(),
      closed,
      "one result must not enlarge or move the modal",
    );
    await input.fill("many");
    await pickerDialog.getByRole("option").nth(39).waitFor();
    assert.deepEqual(
      await geometry(),
      closed,
      "many results must not enlarge the modal or its scroll area",
    );
    const popup = await options.evaluate((list) => {
      const rect = list.getBoundingClientRect();
      const dialog = list.closest("dialog").getBoundingClientRect();
      const hit = document.elementFromPoint(rect.left + 20, rect.bottom - 20);
      return {
        height: rect.height,
        scrolling: list.scrollHeight > list.clientHeight,
        outsideDialog: rect.bottom > dialog.bottom || rect.top < dialog.top,
        hit: list.contains(hit),
        withinViewport:
          rect.left >= 0 &&
          rect.right <= innerWidth &&
          rect.top >= 0 &&
          rect.bottom <= innerHeight,
      };
    });
    assert.deepEqual(
      popup,
      {
        height: 300,
        scrolling: true,
        outsideDialog: true,
        hit: true,
        withinViewport: true,
      },
      "results should be a capped, unclipped overlay even beyond the modal's bounds",
    );
    if (process.env.HEX_PORTAL_SCREENSHOTS)
      await pickerPage.screenshot({
        path: `${process.env.HEX_PORTAL_SCREENSHOTS}-people-dropdown.png`,
      });
    await input.press("Escape");
    await input.press("ArrowUp");
    await pickerDialog.getByRole("option").nth(39).waitFor();
    assert.equal(
      await pickerDialog
        .getByRole("option")
        .last()
        .getAttribute("aria-selected"),
      "true",
    );
    assert.equal(
      await options.evaluate((list) => list.scrollTop > 0),
      true,
      "keyboard navigation scrolls results, not the dialog",
    );
    assert.deepEqual(await geometry(), closed);
    await input.press("Tab");
    assert.equal(await input.getAttribute("aria-expanded"), "false");
    await input.click();
    await options.waitFor();
    await pickerDialog.locator(".muted").click();
    assert.equal(
      await input.getAttribute("aria-expanded"),
      "false",
      "clicking outside dismisses results",
    );
    await input.fill("delayed");
    const pending = await delayed;
    await input.press("Escape");
    await pending.fulfill({ json: { people, groups } });
    await pickerPage.waitForLoadState("networkidle");
    assert.equal(
      await input.getAttribute("aria-expanded"),
      "false",
      "late responses must not reopen a dismissed dropdown",
    );
    for (const [query, message] of [
      ["empty", "No match."],
      ["error", "Could not load people."],
    ]) {
      await input.fill(query);
      await options.getByText(message, { exact: false }).waitFor();
      assert.deepEqual(
        await geometry(),
        closed,
        "empty/error states stay in the overlay",
      );
      assert.equal(
        await pickerDialog.getByRole("option").count(),
        0,
        "failed searches must not retain selectable stale results",
      );
    }
    await input.fill("many");
    await pickerDialog.getByRole("option").nth(39).waitFor();
    await input.press("Escape");
    await input.press("ArrowUp");
    await pickerPage.waitForFunction(() =>
      document
        .querySelector('[role="combobox"]')
        .getAttribute("aria-activedescendant")
        ?.endsWith("-39"),
    );
    await input.press("Enter");
    assert.equal(
      await pickerDialog.isVisible(),
      true,
      "adding a custom permission row keeps the dialog open",
    );
    assert.equal(await input.inputValue(), "");
    assert.equal(
      await input.evaluate((input) => input === document.activeElement),
      true,
    );
    assert.equal(
      await pickerPage
        .locator('[data-permission-row="user:picker-person-19"]')
        .count(),
      1,
      "Enter selects the highlighted person",
    );
    assert.equal(
      await pickerPage.locator(".combobox-options:popover-open").count(),
      0,
    );
    await input.fill("single");
    await pickerDialog.getByRole("option").click();
    assert.equal(await pickerDialog.isVisible(), true);
    assert.equal(
      await pickerPage
        .locator('[data-permission-row="user:picker-person-0"]')
        .count(),
      1,
      "a second person can be added without reopening the dialog",
    );
    await input.fill("single");
    await pickerDialog.getByRole("option").click();
    assert.equal(
      await pickerPage
        .locator('[data-permission-row="user:picker-person-0"]')
        .count(),
      1,
      "selecting the same person twice must not duplicate their row",
    );
    const selected = pickerDialog.getByRole("list", {
      name: "Selected people and groups",
      exact: true,
    });
    assert.equal(await selected.getByRole("listitem").count(), 2);
    assert.match(await selected.innerText(), /Test person 19/);
    assert.match(await selected.innerText(), /Test person 0/);
    const removePerson = pickerDialog.getByRole("button", {
      name: "Remove Test person 0 from selection",
      exact: true,
    });
    await removePerson.focus();
    await removePerson.press("Enter");
    assert.equal(await pickerDialog.isVisible(), true);
    assert.equal(await selected.getByRole("listitem").count(), 1);
    assert.equal(
      await pickerPage
        .locator('[data-permission-row="user:picker-person-0"]')
        .count(),
      0,
      "removing a selection also removes its newly added permission row",
    );
    assert.equal(
      await selected
        .getByRole("button")
        .evaluate((button) => button === document.activeElement),
      true,
      "removing a selection keeps keyboard focus in the selected list",
    );
    await pickerDialog
      .getByLabel("Also give this person or directory group site access")
      .check();
    await input.fill("single");
    await pickerDialog.getByRole("option").click();
    assert.match(
      await pickerPage.locator("[data-admission-entries]").innerText(),
      /Test person 0/,
    );
    await removePerson.click();
    assert.doesNotMatch(
      await pickerPage.locator("[data-admission-entries]").innerText(),
      /Test person 0/,
      "removal also undoes site access added with the permission row",
    );
    await input.fill("existing");
    await pickerDialog.getByRole("option").click();
    await pickerDialog
      .getByRole("button", {
        name: "Remove Alex Andersen from selection",
        exact: true,
      })
      .click();
    assert.equal(
      await pickerPage.locator('[data-permission-row="user:alex"]').count(),
      1,
    );
    assert.match(
      await pickerPage.locator("[data-admission-entries]").innerText(),
      /Alex Andersen/,
      "undoing a custom-row selection preserves pre-existing permissions and site access",
    );
    await pickerDialog
      .getByRole("button", { name: "Done", exact: true })
      .click();
    await pickerDialog.waitFor({ state: "hidden" });

    // The same picker also serves owners and advanced direct recipients.
    await pickerPage
      .getByRole("button", { name: "Manage site owners", exact: true })
      .click();
    const ownerInput = pickerDialog.getByRole("combobox", {
      name: "Add site owner",
      exact: true,
    });
    await ownerInput.fill("single");
    await pickerDialog.getByRole("option").click();
    assert.match(
      await pickerDialog.locator(".permission-members").innerText(),
      /Test person 0/,
    );
    assert.equal(
      await ownerInput.getAttribute("aria-expanded"),
      "false",
      "selection in a multi-select picker closes only its dropdown",
    );
    await ownerInput.click();
    await pickerDialog.getByRole("option").nth(39).waitFor();
    await pickerPage.setViewportSize({ width: 390, height: 400 });
    await pickerPage.emulateMedia({ colorScheme: "dark" });
    await ownerInput.scrollIntoViewIfNeeded();
    await ownerInput.click();
    await pickerDialog.getByRole("option").nth(39).waitFor();
    await pickerPage.waitForFunction(() => {
      const list = document.querySelector('[role="listbox"]');
      const rect = list.getBoundingClientRect();
      const input = list
        .closest(".permission-picker")
        .querySelector("input")
        .getBoundingClientRect();
      return (
        rect.bottom <= input.top &&
        rect.top >= 0 &&
        rect.right <= innerWidth &&
        rect.bottom <= innerHeight
      );
    });
    const placement = await options.evaluate((list) => {
      const rect = list.getBoundingClientRect();
      const input = list
        .closest(".permission-picker")
        .querySelector("input")
        .getBoundingClientRect();
      return {
        above: rect.bottom <= input.top,
        fits:
          rect.top >= 0 &&
          rect.right <= innerWidth &&
          rect.bottom <= innerHeight,
      };
    });
    assert.deepEqual(
      placement,
      { above: true, fits: true },
      "near the bottom of a short viewport the popup opens above the field",
    );
    if (process.env.HEX_PORTAL_SCREENSHOTS)
      await pickerPage.screenshot({
        path: `${process.env.HEX_PORTAL_SCREENSHOTS}-people-dropdown-mobile-dark.png`,
      });
    await pickerPage.close();

    const touchPage = await browser.newPage({
      viewport: { width: 390, height: 844 },
      isMobile: true,
      hasTouch: true,
    });
    touchPage.on("pageerror", (error) => errors.push(error.message));
    await touchPage.route("**/api/hex/directory?*", routeDirectory);
    await touchPage.goto(`${url}/manage/demo?tab=sharing`);
    await touchPage
      .getByRole("button", {
        name: "+ Add a person or directory group",
        exact: true,
      })
      .tap();
    const touchDialog = touchPage.locator("[data-permission-dialog]");
    const touchSelected = touchDialog.getByRole("list", {
      name: "Selected people and groups",
      exact: true,
    });
    assert.match(await touchSelected.innerText(), /Alex Andersen/);
    await touchDialog.getByRole("option").first().tap();
    assert.equal(
      await touchDialog.isVisible(),
      true,
      "adding site access by touch keeps the dialog open",
    );
    assert.match(
      await touchPage.locator("[data-admission-entries]").innerText(),
      /Directory group 0/,
    );
    const touchInput = touchDialog.getByRole("combobox");
    await touchInput.fill("single");
    await touchDialog.getByRole("option").tap();
    assert.equal(await touchDialog.isVisible(), true);
    assert.match(
      await touchPage.locator("[data-admission-entries]").innerText(),
      /Test person 0/,
    );
    await touchInput.fill("single");
    await touchDialog.getByRole("option").tap();
    assert.equal(
      await touchPage
        .getByRole("button", {
          name: "Remove Test person 0 from site access",
          exact: true,
        })
        .count(),
      1,
      "selecting the same person twice must not duplicate site access",
    );
    assert.equal(await touchSelected.getByRole("listitem").count(), 3);
    assert.match(await touchSelected.innerText(), /Directory group 0/);
    assert.match(await touchSelected.innerText(), /Test person 0/);
    await touchDialog
      .getByRole("button", {
        name: "Remove Test person 0 from selection",
        exact: true,
      })
      .tap();
    assert.equal(await touchDialog.isVisible(), true);
    assert.equal(await touchSelected.getByRole("listitem").count(), 2);
    assert.doesNotMatch(
      await touchPage.locator("[data-admission-entries]").innerText(),
      /Test person 0/,
    );
    await touchInput.fill("single");
    await touchDialog.getByRole("option").tap();
    assert.equal(await touchSelected.getByRole("listitem").count(), 3);
    await touchSelected
      .getByRole("listitem")
      .filter({ hasText: groups[0].name })
      .getByRole("button", {
        name: `Remove ${groups[0].name} from selection`,
        exact: true,
      })
      .tap();
    assert.equal(await touchDialog.isVisible(), true);
    assert.equal(await touchSelected.getByRole("listitem").count(), 2);
    assert.doesNotMatch(
      await touchPage.locator("[data-admission-entries]").innerText(),
      /Directory group 0/,
      "a directory group can also be removed without closing the dialog",
    );
    await touchDialog.getByRole("button", { name: "Done", exact: true }).tap();
    await touchDialog.waitFor({ state: "hidden" });
    assert.match(
      await touchPage.locator("[data-admission-entries]").innerText(),
      /Test person 0/,
    );
    await touchPage
      .getByRole("button", {
        name: "+ Add a person or directory group",
        exact: true,
      })
      .tap();
    assert.equal(await touchSelected.getByRole("listitem").count(), 2);
    assert.match(await touchSelected.innerText(), /Test person 0/);
    await touchDialog.getByRole("option").first().waitFor();
    await touchInput.press("Escape");
    await touchDialog
      .getByRole("button", {
        name: "Remove Alex Andersen from selection",
        exact: true,
      })
      .tap();
    assert.equal(await touchSelected.getByRole("listitem").count(), 1);
    assert.doesNotMatch(
      await touchPage.locator("[data-admission-entries]").innerText(),
      /Alex Andersen/,
      "existing site-access selections can be removed inside the dialog too",
    );
    await touchDialog
      .getByRole("button", {
        name: "Remove Test person 0 from selection",
        exact: true,
      })
      .tap();
    assert.equal(await touchSelected.getByRole("listitem").count(), 0);
    assert.equal(
      await touchDialog
        .getByText("No people or groups selected yet.", { exact: true })
        .isVisible(),
      true,
    );
    assert.equal(
      await touchDialog
        .getByRole("button", { name: "Done", exact: true })
        .evaluate((button) => button === document.activeElement),
      true,
    );
    await touchDialog.getByRole("button", { name: "Done", exact: true }).tap();
    await touchDialog.waitFor({ state: "hidden" });
    await touchPage.close();
    assert.deepEqual(
      await policy(),
      originalPolicy,
      "searching and picking draft recipients must not save or reset access",
    );
  };

  await verifyDirectoryPicker();

  await page.goto(`${url}/manage/demo?tab=sharing`);
  await page.locator('#permission-manager[data-ready="true"]').waitFor();
  const initial = await policy();
  assert.equal(initial.admission.roles, undefined);
  assert.deepEqual(initial.admission.principals, ["user:alex"]);
  assert.doesNotMatch(
    await page.locator("#site-access").innerText(),
    /Permission group:|Permission groups and custom/,
  );
  assert.equal(
    await page
      .getByText("Other resource rules and page restrictions", { exact: true })
      .count(),
    0,
  );
  assert.equal(
    await page
      .locator("[data-independent-rules] .permission-access-row")
      .count(),
    1,
    "migrated owner grants should be summarized as one audience",
  );
  assert.equal(
    await page
      .locator("[data-independent-rules] .permission-rule-card")
      .count(),
    0,
    "do not dump technical rule cards into the sharing screen",
  );
  assert.equal(
    await page.locator("[data-additional-access]").getAttribute("open"),
    null,
    "outside grants should be summarized, not expanded by default",
  );
  await page.locator("[data-additional-access] > summary").click();
  await page
    .getByRole("button", { name: "Review access", exact: true })
    .click();
  assert.equal(await dialog.locator(".permission-rule-card").count(), 5);
  await close();
  await page.locator("[data-additional-access] > summary").click();
  await page
    .getByRole("button", { name: "Review page access", exact: true })
    .click();
  assert.equal(await dialog.locator(".permission-rule-card").count(), 2);
  assert.match(await dialog.innerText(), /\/admin\//);
  await close();
  assert.deepEqual(
    await policy(),
    initial,
    "reviewing existing access must not rewrite policies",
  );
  assert.equal(
    await page.locator(".permission-assignment-table tbody tr").count(),
    3,
  );
  assert.equal(
    await box("owners", "Reviewers", "Site owners").isChecked(),
    false,
    "existing owners must not silently gain data access",
  );
  assert.equal(
    await row("user:bea").count(),
    0,
    "directory users must not be listed automatically",
  );
  assert.equal(
    await page.locator(".permission-technical").getAttribute("open"),
    null,
  );

  await page
    .getByLabel("General access", { exact: true })
    .selectOption("everyone");
  assert.deepEqual(
    await policy(),
    initial,
    "edits must stay staged until save",
  );
  await save();
  let saved = await policy();
  assert.equal(saved.admission.signedIn, true);
  assert.equal(
    await page.locator("[data-permission-site-badge]").innerText(),
    "Everyone",
    "saved admission must update the header overview too",
  );
  assert.deepEqual(saved.roles, initial.roles);
  assert.deepEqual(saved.bindings, initial.bindings);
  assert.deepEqual(saved.grants.admin, initial.grants.admin);
  assert.deepEqual(
    saved.grants["project-expenses"],
    initial.grants["project-expenses"],
  );

  // The real management screen must retain the approved design in both themes
  // and at narrow widths, not just pass the interaction tests.
  for (const scheme of ["light", "dark"]) {
    await checkDesign(scheme);
    await checkDesign(scheme, true);
  }
  await page.setViewportSize({ width: 1440, height: 1000 });
  await page.emulateMedia({ colorScheme: "light" });

  await page
    .getByRole("button", { name: "+ New permission group", exact: true })
    .click();
  await dialog
    .getByLabel("Permission group name", { exact: true })
    .fill("Finance reviewers");
  await dialog
    .getByLabel("Description", { exact: true })
    .fill("View submitted invoices and edit their amount");
  assert.equal(
    await dialog
      .getByLabel("Assign this new group to all listed site owners", {
        exact: true,
      })
      .isChecked(),
    true,
  );
  await dialog
    .getByRole("button", { name: "Add permission group", exact: true })
    .click();
  await dialog
    .getByRole("button", { name: "+ Add resource rule", exact: true })
    .click();
  const rule = dialog.locator("form");
  await rule
    .getByLabel("Resource type", { exact: true })
    .selectOption("collection");
  await rule
    .getByLabel("Resource name or prefix", { exact: true })
    .fill("invoices");
  await rule.getByLabel("View", { exact: true }).check();
  await rule.getByLabel("Edit", { exact: true }).check();
  const selectorLayout = await rule.evaluate((form) => {
    const kind = form.querySelector("select").getBoundingClientRect();
    const resource = form.querySelector("input[list]").getBoundingClientRect();
    return {
      aligned: Math.abs(kind.top - resource.top) < 1,
      kindHeight: kind.height,
      resourceHeight: resource.height,
    };
  });
  assert.deepEqual(
    selectorLayout,
    { aligned: true, kindHeight: 42, resourceHeight: 42 },
    "shared form grid must align resource controls without stretching the select to match hint text",
  );
  assert.equal(
    await rule.locator(".permission-advanced-limits").getAttribute("open"),
    null,
  );
  await screenshot("resource-rule-simple");
  await rule.getByLabel("Resource type", { exact: true }).selectOption("file");
  await rule
    .getByLabel("Resource type", { exact: true })
    .selectOption("collection");
  assert.equal(
    await rule.getByLabel("Edit", { exact: true }).isChecked(),
    true,
    "switching resource types must retain draft operation choices",
  );
  await rule.locator(".permission-advanced-limits > summary").click();
  await rule
    .locator("summary")
    .filter({ hasText: "Limit editable fields (optional)" })
    .click();
  await rule
    .getByLabel("Allow changes to all record fields", { exact: true })
    .uncheck();
  await rule
    .getByLabel("Editable fields · one per line", { exact: true })
    .fill("amount");
  await rule
    .locator("summary")
    .filter({ hasText: "Limit which records are allowed (optional)" })
    .click();
  const where = rule.locator("fieldset.permission-condition").first();
  await where
    .getByRole("button", { name: "Add a condition", exact: true })
    .click();
  await where.getByLabel("Record field", { exact: true }).fill("data.status");
  await where
    .getByRole("combobox", { name: "Value type", exact: true })
    .selectOption("text");
  await where
    .getByRole("textbox", { name: "Comparison value", exact: true })
    .fill("submitted");
  await rule
    .getByRole("button", { name: "Add rule to group", exact: true })
    .click();
  await addRow("Finance", "Finance");
  await box("group:finance", "Finance reviewers", "Finance").check();
  assert.equal(
    Object.keys((await policy()).roles).length,
    1,
    "creating groups and rules must not mutate the server before save",
  );
  await save();
  saved = await policy();
  const groupID = Object.keys(saved.roles).find(
    (id) => saved.roles[id].name === "Finance reviewers",
  );
  assert.ok(groupID);
  assert.ok(
    Object.values(saved.bindings).some(
      (binding) =>
        binding.role === groupID &&
        binding.principal === "user:owner" &&
        !binding.scope,
    ),
  );
  assert.ok(
    Object.values(saved.bindings).some(
      (binding) =>
        binding.role === groupID && binding.principal === "group:finance",
    ),
  );
  const createdID = Object.keys(saved.grants).find(
    (id) => saved.grants[id].resource === "invoices",
  );
  assert.deepEqual(saved.grants[createdID].subjects.roles, [groupID]);
  assert.deepEqual(saved.grants[createdID].fields, ["amount"]);
  assert.deepEqual(saved.grants[createdID].where, {
    all: [{ field: "data.status", op: "eq", value: "submitted" }],
  });
  assert.deepEqual(
    saved.grants["project-expenses"],
    initial.grants["project-expenses"],
  );
  await screenshot("groups-desktop");
  await page
    .getByRole("button", {
      name: "Edit Finance reviewers permission group",
      exact: true,
    })
    .click();
  assert.equal(
    await dialog
      .locator(".permission-group-resources input[type=checkbox]:checked")
      .count(),
    1,
  );
  await screenshot("group-editor");
  await page.setViewportSize({ width: 390, height: 844 });
  await page.emulateMedia({ colorScheme: "dark" });
  assert.equal(
    await dialog.evaluate((node) => node.scrollWidth > node.clientWidth),
    false,
    "group editor must fit narrow screens without horizontal overflow",
  );
  await screenshot("group-editor-mobile-dark");
  await page.setViewportSize({ width: 1440, height: 1000 });
  await page.emulateMedia({ colorScheme: "light" });
  await close();
  await page.locator(".permission-group-guide > summary").click();
  assert.match(
    await page.locator("[data-group-definitions]").innerText(),
    /invoices/,
  );
  await page.locator(".permission-group-guide > summary").click();

  await addRow("bea", "Bea");
  assert.equal(
    await page.locator("#permission-manager img").count(),
    0,
    "directory names must remain text",
  );
  await box(
    "default",
    "Finance reviewers",
    "Everyone with site access",
  ).check();
  assert.equal(
    await box(
      "user:bea",
      "Finance reviewers",
      "Bea <img src=x onerror=alert(1)>",
    ).isDisabled(),
    true,
  );
  await page
    .getByRole("button", { name: "Discard changes", exact: true })
    .click();
  assert.equal(
    await box(
      "default",
      "Finance reviewers",
      "Everyone with site access",
    ).isChecked(),
    false,
  );

  await row("user:alex")
    .getByRole("button", { name: "Project: A", exact: true })
    .click();
  await dialog
    .getByRole("button", {
      name: "Add another allowed identifier",
      exact: true,
    })
    .click();
  await dialog
    .getByLabel("Allowed identifier", { exact: true })
    .nth(1)
    .fill("B");
  await dialog
    .getByRole("button", { name: "Apply membership", exact: true })
    .click();
  await save();
  saved = await policy();
  assert.ok(
    saved.bindings["alex-a"],
    "existing matching scope binding must keep its stable ID",
  );
  assert.ok(
    Object.values(saved.bindings).some(
      (binding) =>
        binding.principal === "user:alex" && binding.scope?.id === "B",
    ),
  );
  assert.deepEqual(
    saved.grants["project-expenses"],
    initial.grants["project-expenses"],
  );

  // Lossless edit of a nested custom rule; the server retains untouched raw
  // conditions rather than accepting their JavaScript reconstruction.
  await page
    .getByRole("button", {
      name: "Edit Reviewers permission group",
      exact: true,
    })
    .click();
  await dialog.getByRole("button", { name: "Edit rule", exact: true }).click();
  await dialog
    .getByRole("button", { name: "Apply rule changes", exact: true })
    .click();
  await save();
  assert.deepEqual(
    (await policy()).grants["project-expenses"],
    initial.grants["project-expenses"],
  );

  // Narrow edits to unrelated entries merge, but editing the same group from
  // a stale form must keep the staged UI and fail without a partial save.
  const beforeConflict = await policy();
  const remote = structuredClone(beforeConflict);
  remote.roles[groupID].description = "Changed in another browser";
  assert.equal(
    (
      await page.request.put(`${url}/api/hex/sites/demo/access`, {
        data: remote,
        headers: { "X-Hex-Request": "1" },
      })
    ).status(),
    200,
  );
  await page
    .getByRole("button", {
      name: "Edit Finance reviewers permission group",
      exact: true,
    })
    .click();
  await dialog
    .getByLabel("Permission group name", { exact: true })
    .fill("Renamed reviewers");
  await dialog
    .getByRole("button", { name: "Apply group changes", exact: true })
    .click();
  await save(false);
  assert.match(
    await page.locator("[data-permission-error]").innerText(),
    /Nothing was saved/,
  );
  assert.equal(
    await page
      .getByRole("button", {
        name: "Edit Renamed reviewers permission group",
        exact: true,
      })
      .count(),
    1,
    "failed save replaced staged controls",
  );
  assert.deepEqual(await policy(), remote);
  await page.reload();
  await page.locator('#permission-manager[data-ready="true"]').waitFor();

  // An invalid staged rule must be retained, and correcting it must make the
  // final batch saveable rather than replaying an unfixable intermediate error.
  const beforeInvalid = await policy();
  await page
    .getByRole("button", {
      name: "Edit Finance reviewers permission group",
      exact: true,
    })
    .click();
  await dialog.getByRole("button", { name: "Edit rule", exact: true }).click();
  await dialog
    .getByLabel("Resource name or prefix", { exact: true })
    .fill("invalid/name");
  await dialog
    .getByRole("button", { name: "Apply rule changes", exact: true })
    .click();
  await save(false);
  assert.deepEqual(await policy(), beforeInvalid);
  await page
    .getByRole("button", {
      name: "Edit Finance reviewers permission group",
      exact: true,
    })
    .click();
  await dialog.getByRole("button", { name: "Edit rule", exact: true }).click();
  assert.equal(
    await dialog
      .getByLabel("Resource name or prefix", { exact: true })
      .inputValue(),
    "invalid/name",
  );
  await dialog
    .getByLabel("Resource name or prefix", { exact: true })
    .fill("invoices");
  await dialog
    .getByRole("button", { name: "Apply rule changes", exact: true })
    .click();
  await save();
  assert.deepEqual(await policy(), beforeInvalid);

  await row("group:finance")
    .getByRole("button", { name: "Remove Finance permission row", exact: true })
    .click();
  await save();
  assert.equal(
    Object.values((await policy()).bindings).some(
      (binding) => binding.principal === "group:finance",
    ),
    false,
  );
  assert.equal(
    await box("owners", "Finance reviewers", "Site owners").isChecked(),
    true,
  );
  await box("owners", "Finance reviewers", "Site owners").uncheck();
  await save();
  assert.equal(
    Object.values((await policy()).bindings).some(
      (binding) =>
        binding.role === groupID && binding.principal === "user:owner",
    ),
    false,
  );

  await page
    .getByRole("button", {
      name: "Edit Finance reviewers permission group",
      exact: true,
    })
    .click();
  await dialog
    .getByRole("button", { name: "Delete permission group", exact: true })
    .click();
  await save();
  saved = await policy();
  assert.equal(saved.roles[groupID], undefined);
  assert.equal(saved.grants[createdID], undefined);
  assert.deepEqual(saved.grants.admin, initial.grants.admin);
  assert.deepEqual(
    saved.grants["project-expenses"],
    initial.grants["project-expenses"],
  );
  assert.equal(
    await page.locator(".permission-technical").getAttribute("open"),
    null,
  );
  if (process.env.HEX_PORTAL_SCREENSHOTS)
    await page.screenshot({
      path: `${process.env.HEX_PORTAL_SCREENSHOTS}-desktop.png`,
      fullPage: true,
    });
  await page.setViewportSize({ width: 390, height: 844 });
  assert.equal(
    await page.evaluate(
      () => document.documentElement.scrollWidth > innerWidth,
    ),
    false,
    "table must scroll inside its card",
  );
  if (process.env.HEX_PORTAL_SCREENSHOTS)
    await page.screenshot({
      path: `${process.env.HEX_PORTAL_SCREENSHOTS}-mobile.png`,
      fullPage: true,
    });
  await page.setViewportSize({ width: 1440, height: 1000 });
  const empty = {
    version: 2,
    owners: ["user:owner"],
    admission: { owners: true },
    grants: { pages: initial.grants.pages },
  };
  assert.equal(
    (
      await page.request.put(`${url}/api/hex/sites/demo/access`, {
        data: empty,
        headers: { "X-Hex-Request": "1" },
      })
    ).status(),
    200,
  );
  await page.reload();
  await page.getByText("No permission groups yet.", { exact: false }).waitFor();
  assert.equal(
    await page.locator("[data-additional-access]").isVisible(),
    false,
    "do not show an irrelevant existing-access section when there are no direct app grants",
  );
  await page
    .getByRole("button", { name: "+ New permission group", exact: true })
    .click();
  await dialog
    .getByLabel("Permission group name", { exact: true })
    .fill("New group <img src=x onerror=alert(1)>");
  await dialog
    .getByRole("button", { name: "Add permission group", exact: true })
    .click();
  await close();
  assert.equal(await page.locator("#permission-manager img").count(), 0);
  await save();
  assert.equal(Object.keys((await policy()).roles).length, 1);

  // The technical editor cannot silently replace staged table work (or vice
  // versa). Its explicit discard path must not strand the user with two drafts.
  const sampleGroupName = "New group <img src=x onerror=alert(1)>";
  await box("default", sampleGroupName, "Everyone with site access").check();
  assert.equal(
    await page.evaluate(() =>
      document.activeElement?.getAttribute("aria-label"),
    ),
    `${sampleGroupName} for Everyone with site access`,
    "keyboard focus should survive table redraws",
  );
  await page.locator(".permission-technical > summary").click();
  const technical = page.getByLabel("Permission policy (JSON)", {
    exact: true,
  });
  await technical.fill(`${await technical.inputValue()}\n`);
  const beforeDrafts = await policy();
  await page.getByRole("button", { name: "Save changes", exact: true }).click();
  await page.locator("[data-permission-error]:visible").waitFor();
  assert.match(
    await page.locator("[data-permission-error]").innerText(),
    /technical editor/,
  );
  assert.deepEqual(await policy(), beforeDrafts);
  await page
    .getByRole("button", { name: "Discard technical edits", exact: true })
    .click();
  await save();

  // A gateway sign-in document must never replace the manager and discard its
  // unsaved state, even if it arrives as a successful HTTP response.
  await box("default", sampleGroupName, "Everyone with site access").uncheck();
  await page.route("**/sharing", (route) =>
    route.fulfill({
      status: 200,
      contentType: "text/html",
      body: "<html><body>Sign in</body></html>",
    }),
  );
  await Promise.all([
    page.waitForResponse(
      (response) =>
        response.url().endsWith("/sharing") &&
        response.request().method() === "PUT",
    ),
    page.getByRole("button", { name: "Save changes", exact: true }).click(),
  ]);
  await page.locator("[data-permission-error]:visible").waitFor();
  assert.match(
    await page.locator("[data-permission-error]").innerText(),
    /sign-in may have expired/,
  );
  assert.equal(
    await box(
      "default",
      sampleGroupName,
      "Everyone with site access",
    ).isChecked(),
    false,
  );
  await page.unroute("**/sharing");
  await save();
  const withAutomation = await policy();
  const sampleID = Object.keys(withAutomation.roles)[0];
  withAutomation.roles[sampleID].signedIn = true;
  withAutomation.roles.background = { name: "Background job" };
  withAutomation.bindings["automation-job"] = {
    principal: "automation:job",
    role: "background",
  };
  let deepValue = "exact value";
  for (let depth = 0; depth < 10; depth++) deepValue = { nested: deepValue };
  withAutomation.grants.deep = {
    kind: "collection",
    resource: "complex",
    operations: ["read"],
    subjects: { roles: ["background"] },
    where: { field: "data.value", op: "eq", value: deepValue },
  };
  assert.equal(
    (
      await page.request.put(`${url}/api/hex/sites/demo/access`, {
        data: withAutomation,
        headers: { "X-Hex-Request": "1" },
      })
    ).status(),
    200,
  );
  await page.reload();
  await row("automation:job").waitFor();
  assert.equal(
    await box("automation:job", sampleGroupName, "job").isChecked(),
    false,
    "signed-in defaults do not apply to automations",
  );
  assert.equal(
    await box("automation:job", sampleGroupName, "job").isEnabled(),
    true,
  );
  await page
    .getByRole("button", {
      name: "Edit Background job permission group",
      exact: true,
    })
    .click();
  await dialog.getByRole("button", { name: "Edit rule", exact: true }).click();
  await dialog
    .getByText("The existing requirement will be preserved exactly", {
      exact: false,
    })
    .waitFor();
  await dialog
    .getByLabel("Resource name or prefix", { exact: true })
    .fill("complex2");
  await dialog
    .getByRole("button", { name: "Apply rule changes", exact: true })
    .click();
  await save();
  assert.deepEqual(
    (await policy()).grants.deep.where.value,
    deepValue,
    "deep custom conditions must remain intact when editing other parts of a rule",
  );
  // Removing a checked resource permission from one group must preserve other
  // recipients of a shared rule, including its untouched custom conditions.
  const sharedPolicy = await policy();
  sharedPolicy.roles.other = { name: "Other recipients" };
  sharedPolicy.grants.shared = {
    kind: "collection",
    resource: "shared",
    operations: ["read"],
    subjects: { roles: ["background", "other"], principals: ["user:bea"] },
    where: { field: "data.value", op: "eq", value: deepValue },
  };
  assert.equal(
    (
      await page.request.put(`${url}/api/hex/sites/demo/access`, {
        data: sharedPolicy,
        headers: { "X-Hex-Request": "1" },
      })
    ).status(),
    200,
  );
  await page.reload();
  await page
    .getByRole("button", {
      name: "Edit Background job permission group",
      exact: true,
    })
    .click();
  const sharedChoice = dialog.getByLabel("Data collection · shared — view", {
    exact: true,
  });
  await sharedChoice.uncheck();
  await dialog
    .getByRole("button", { name: "Apply group changes", exact: true })
    .click();
  assert.deepEqual(
    await policy(),
    sharedPolicy,
    "unchecking a resource permission is staged until save",
  );
  await save();
  const afterShared = await policy();
  assert.deepEqual(afterShared.grants.shared.subjects.roles, ["other"]);
  assert.deepEqual(afterShared.grants.shared.subjects.principals, ["user:bea"]);
  assert.deepEqual(afterShared.grants.shared.where.value, deepValue);
  assert.deepEqual(afterShared.grants.deep, sharedPolicy.grants.deep);
  // Editing through a group forks legacy shared grants rather than changing
  // every group/person that used to share the rule.
  await page.request.put(`${url}/api/hex/sites/demo/access`, {
    data: sharedPolicy,
    headers: { "X-Hex-Request": "1" },
  });
  await page.reload();
  await page
    .getByRole("button", {
      name: "Edit Background job permission group",
      exact: true,
    })
    .click();
  await dialog
    .locator(".permission-group-option")
    .filter({ hasText: "Data collection · shared — view" })
    .getByRole("button", { name: "Edit rule", exact: true })
    .click();
  assert.match(
    await dialog.innerText(),
    /other recipients keep the original rule/,
  );
  assert.equal(await dialog.locator(".permission-audience").isVisible(), false);
  await dialog
    .getByLabel("Resource name or prefix", { exact: true })
    .fill("shared2");
  await dialog
    .getByRole("button", { name: "Apply rule changes", exact: true })
    .click();
  await save();
  const forked = await policy();
  assert.equal(forked.grants.shared.resource, "shared");
  assert.deepEqual(forked.grants.shared.subjects.roles, ["other"]);
  assert.deepEqual(forked.grants.shared.subjects.principals, ["user:bea"]);
  const independent = Object.values(forked.grants).find(
    (rule) => rule.resource === "shared2",
  );
  assert.deepEqual(independent.subjects.roles, ["background"]);
  assert.deepEqual(independent.where.value, deepValue);
  assert.deepEqual(forked.grants.deep, sharedPolicy.grants.deep);
  const designPage = await browser.newPage();
  for (const scheme of ["light", "dark"]) {
    await designPage.emulateMedia({ colorScheme: scheme });
    for (const mobile of [false, true]) {
      await designPage.setViewportSize(
        mobile ? { width: 390, height: 844 } : { width: 1440, height: 1000 },
      );
      for (const path of [
        "/",
        "/manage",
        "/manage/demo?tab=overview",
        "/manage/demo?tab=data",
        "/start",
      ]) {
        await designPage.goto(`${url}${path}`);
        const sharedDesign = await designPage.evaluate(() => {
          const root = getComputedStyle(document.documentElement);
          const main = getComputedStyle(document.querySelector(".page"));
          const button = document.querySelector(".button");
          return {
            background: root.getPropertyValue("--bg").trim(),
            maxWidth: main.maxWidth,
            radius: button ? getComputedStyle(button).borderRadius : "7px",
            overflow: document.documentElement.scrollWidth > innerWidth,
          };
        });
        assert.deepEqual(
          sharedDesign,
          {
            background: scheme === "dark" ? "#101a21" : "#f5f7f8",
            maxWidth: "1368px",
            radius: "7px",
            overflow: false,
          },
          `${path} must use shared portal components in ${scheme} ${mobile ? "mobile" : "desktop"}`,
        );
        if (process.env.HEX_PORTAL_SCREENSHOTS && !mobile)
          await designPage.screenshot({
            path: `${process.env.HEX_PORTAL_SCREENSHOTS}-shared-${path === "/" ? "home" : path.replace(/[^a-z]+/g, "-")}-${scheme}.png`,
            fullPage: true,
          });
      }
    }
  }
  await designPage.close();
  await page.goto(`${url}/manage/demo?tab=sharing`);
  await page.locator('#permission-manager[data-ready="true"]').waitFor();
  await page.goto(`${url}/manage/demo`);
  const actionCard = page.locator(
    '[data-action-card][data-name="list-expenses"]',
  );
  await actionCard.getByRole("button", { name: "Details & access ↗" }).click();
  const actionDialog = page.locator("[data-action-dialog]");
  await actionDialog
    .getByLabel("Available to", { exact: true })
    .selectOption("selected");
  await actionDialog.getByLabel("Add an app", { exact: true }).fill("consumer");
  await actionDialog
    .getByRole("button", { name: "consumer", exact: true })
    .click();
  await actionDialog
    .getByLabel("As the calling site · enables automations", { exact: true })
    .check();
  await actionDialog
    .getByText("Site-mode data restrictions", { exact: true })
    .click();
  await actionDialog
    .getByLabel("Existing record condition (JSON)", { exact: true })
    .fill('{"field":"data.project","op":"eq","value":"A"}');
  const beforeSharing = await policy();
  assert.deepEqual(
    await policy(),
    beforeSharing,
    "action access must remain staged",
  );
  await actionDialog
    .getByRole("button", { name: "Save access", exact: true })
    .click();
  await actionDialog
    .getByText("Action access saved.", { exact: true })
    .waitFor();
  assert.equal(
    Object.values((await policy()).grants).filter(
      (grant) =>
        grant.kind === "action-consumer" && grant.operations.includes("site"),
    ).length,
    1,
  );
  const appGrant = Object.values((await policy()).grants).find(
    (grant) =>
      grant.kind === "action-consumer" && grant.operations.includes("site"),
  );
  assert.deepEqual(appGrant.where, {
    field: "data.project",
    op: "eq",
    value: "A",
  });
  await page.reload();
  await actionCard.getByRole("button", { name: "Details & access ↗" }).click();
  assert.equal(
    await actionDialog.getByLabel("Available to", { exact: true }).inputValue(),
    "selected",
  );
  await actionDialog
    .getByLabel("Available to", { exact: true })
    .selectOption("all");
  await actionDialog
    .getByRole("button", { name: "Save access", exact: true })
    .click();
  await actionDialog
    .getByText("Action access saved.", { exact: true })
    .waitFor();
  assert.equal(
    Object.values((await policy()).roles).some((role) => role.workspace),
    true,
  );
  await actionDialog
    .getByRole("button", { name: "Close action details", exact: true })
    .click();
  await page.goto(`${url}/apps/demo/actions`);
  for (const colorScheme of ["light", "dark"]) {
    await page.emulateMedia({ colorScheme });
    await page.setViewportSize({ width: 390, height: 844 });
    assert.equal(
      await page.evaluate(
        () => document.documentElement.scrollWidth > innerWidth,
      ),
      false,
      "app action catalog must fit mobile screens",
    );
    await screenshot(`app-actions-${colorScheme}`);
  }
  await page
    .locator('[data-action-card][data-name="list-expenses"]')
    .getByRole("button", { name: "Details & access ↗" })
    .click();
  await actionDialog
    .getByLabel("Available to", { exact: true })
    .selectOption("none");
  await actionDialog
    .getByRole("button", { name: "Save access", exact: true })
    .click();
  await actionDialog
    .getByText("Action access saved.", { exact: true })
    .waitFor();
  assert.equal(
    Object.values((await policy()).grants).filter(
      (grant) => grant.kind === "action-consumer",
    ).length,
    0,
  );
  assert.deepEqual(errors, []);
  await actionDialog
    .getByRole("button", { name: "Close action details", exact: true })
    .click();
  await page
    .locator('[data-action-card][data-kind="event"]')
    .getByRole("button", { name: "Details & access ↗" })
    .click();
  await actionDialog
    .getByLabel("Available to", { exact: true })
    .selectOption("all");
  await actionDialog
    .getByRole("button", { name: "Save access", exact: true })
    .click();
  await actionDialog
    .getByText("Subscription access saved.", { exact: true })
    .waitFor();
  const subscriptionGrants = Object.values((await policy()).grants).filter(
    (grant) => grant.kind === "event",
  );
  assert.equal(subscriptionGrants.length, 1);
  assert.deepEqual(subscriptionGrants[0].operations, ["subscribe"]);
  await actionDialog
    .getByLabel("Available to", { exact: true })
    .selectOption("none");
  await actionDialog
    .getByRole("button", { name: "Save access", exact: true })
    .click();
  await page.waitForFunction(async () => {
    const policy = await (await fetch("/api/hex/sites/demo/access")).json();
    return !Object.values(policy.grants).some(
      (grant) => grant.kind === "event",
    );
  });
  await page.goto(`${url}/manage/conversion?tab=sharing`);
  await page.locator('#permission-manager[data-ready="true"]').waitFor();
  const conversionPolicy = async () =>
    (await page.request.get(`${url}/api/hex/sites/conversion/access`)).json();
  const beforeConversion = await conversionPolicy();
  await page.locator("[data-additional-access] > summary").click();
  await page
    .getByRole("button", { name: "Move into a permission group", exact: true })
    .click();
  assert.deepEqual(
    await conversionPolicy(),
    beforeConversion,
    "organizing groups must stay staged",
  );
  await page.getByRole("button", { name: "Save changes", exact: true }).click();
  await page.getByText("All changes saved", { exact: true }).waitFor();
  const converted = await conversionPolicy();
  const ownerGroup = Object.entries(converted.roles).find(
    ([, role]) => role.owners,
  )?.[0];
  assert.ok(
    ownerGroup,
    "automatic owner audience must be preserved, not snapshotted",
  );
  for (const [id, rule] of Object.entries(beforeConversion.grants)) {
    assert.deepEqual(
      converted.grants[id],
      id.startsWith("existing-owner-")
        ? { ...rule, subjects: { roles: [ownerGroup] } }
        : rule,
    );
  }
  assert.deepEqual(converted.admission, beforeConversion.admission);
  assert.equal(
    await page.locator("[data-additional-access]").isVisible(),
    false,
  );
  assert.deepEqual(errors, []);
  console.log(
    "Production permission matrix passed: groups and resource rules, atomic saves, defaults and owner assignments, scoped memberships, revocation, lossless custom rules, validation/stale-form/sign-in error retention, technical-editor draft protection, keyboard access, escaping, and mobile layout. Routine management required no JSON.",
  );
} catch (error) {
  console.error(error);
  if (page)
    console.error((await page.locator("body").innerText()).slice(-16000));
  throw error;
} finally {
  if (browser) await browser.close();
  if (child?.exitCode === null) {
    child.kill("SIGTERM");
    await once(child, "exit");
  }
  await rm(temporary, { recursive: true, force: true });
}

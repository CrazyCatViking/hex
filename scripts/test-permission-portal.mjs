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
    await dialog.waitFor({ state: "hidden" });
  };

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

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
  await rule
    .getByLabel("Allow changes to all record fields", { exact: true })
    .uncheck();
  await rule
    .getByLabel("Editable fields · one per line", { exact: true })
    .fill("amount");
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
  await close();
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
    .getByRole("button", { name: "Remove row", exact: true })
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

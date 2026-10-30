// Stage narrow edits against the original server baseline. A single save is
// validated and reconciled atomically; rendering never rewrites the policy.
(() => {
  const ui = window.HexPermissionEditor;
  const { element, input, select, field, checkbox, button, title, disclosure } =
    ui;
  let active;

  function setup() {
    const manager = document.getElementById("permission-manager");
    if (!manager || manager.dataset.ready) return;
    manager.dataset.ready = "true";
    try {
      active = createManager(manager);
    } catch (failure) {
      manager.prepend(
        element(
          "p",
          `Permission controls could not be loaded: ${failure.message}. No settings were changed.`,
          "banner banner-error",
        ),
      );
    }
  }

  function createManager(manager) {
    const query = (selector) => manager.querySelector(selector);
    const baseline = JSON.parse(manager.dataset.policy);
    let policy = structuredClone(baseline);
    let edits = [];
    let extraRows = new Set();
    let advancedDirty = false;
    let dialogDirty = false;
    let saving = false;
    const labels = new Map(
      JSON.parse(manager.dataset.labels || "[]").map((label) => [
        label.principal,
        label,
      ]),
    );
    const dialog = query("[data-permission-dialog]");
    const dialogBody = query("[data-permission-dialog-body]");
    const error = query("[data-permission-error]");
    const saveForm = query("[data-permission-save]");
    const advanced = query("[data-advanced-permission-form]");
    const originalAdvanced = advanced.elements.policy.value;
    const nameOf = (principal) => labels.get(principal)?.name || principal;
    const groupName = (id) => policy.roles?.[id]?.name || title(id);
    const bindings = (principal, role) =>
      Object.values(policy.bindings || {}).filter(
        (binding) => binding.principal === principal && binding.role === role,
      );
    const groupRules = (id) =>
      Object.entries(policy.grants || {}).filter(([, rule]) =>
        rule.subjects.roles?.includes(id),
      );
    const scopeKinds = (id) => [
      ...new Set(
        groupRules(id)
          .map(([, rule]) => rule.scope?.kind)
          .filter(Boolean),
      ),
    ];
    const dirty = () => edits.length > 0 || advancedDirty || dialogDirty;
    const announce = (message) => {
      query("[data-permission-status]").textContent = message;
    };
    const showError = (message) => {
      if (dialog.open) {
        let localError = dialogBody.querySelector("[data-dialog-error]");
        if (!localError) {
          localError = element("p", "", "banner banner-error");
          localError.dataset.dialogError = "";
          localError.setAttribute("role", "alert");
          localError.tabIndex = -1;
          dialogBody.prepend(localError);
        }
        localError.textContent = message;
        localError.focus();
        return;
      }
      error.textContent = message;
      error.hidden = false;
      error.focus();
    };
    // Map keys are application data, including names such as "__proto__".
    const put = (map, key, value) =>
      Object.defineProperty(map, key, {
        value,
        enumerable: true,
        configurable: true,
        writable: true,
      });
    const edit = (action, values) => ({
      action,
      values: Object.fromEntries(
        Object.entries(values).map(([key, value]) => [
          key,
          Array.isArray(value) ? value : [String(value)],
        ]),
      ),
    });

    function stage(action, values) {
      const pending = edit(action, values);
      if (action === "membership") extraRows.add(pending.values.principal[0]);
      apply(pending);
      edits.push(pending);
      manager.querySelectorAll(".banner-success").forEach((message) => {
        message.hidden = true;
      });
      render();
      announce(
        "Unsaved changes. Review the table, then save changes to apply them together.",
      );
    }

    function apply({ action, values }) {
      const get = (key) => values[key]?.[0];
      const id = get("id");
      switch (action) {
        case "workspace-sharing": {
          policy.grants ||= {};
          const kinds =
            get("kind") === "action"
              ? ["action", "action-consumer"]
              : ["event"];
          const ids = [get("grant_id"), get("consumer_id")];
          kinds.forEach((kind, index) => {
            if (get("mode") === "restricted") delete policy.grants[ids[index]];
            else
              put(policy.grants, ids[index], {
                kind,
                resource: get("resource"),
                operations: [kind === "event" ? "subscribe" : "run"],
                subjects: { workspace: true },
              });
          });
          break;
        }
        case "role":
          policy.roles ||= {};
          put(policy.roles, id, JSON.parse(get("entry")));
          if (get("creating") === "true" && get("assign_owners") === "true") {
            for (const principal of policy.owners || [])
              apply(
                edit("membership", { principal, group: id, assigned: "true" }),
              );
          }
          break;
        case "grant":
          policy.grants ||= {};
          put(policy.grants, id, JSON.parse(get("entry")));
          break;
        case "remove-grant":
          delete policy.grants[id];
          break;
        case "membership": {
          const principal = get("principal"),
            role = get("group");
          policy.bindings ||= {};
          for (const [bindingID, binding] of Object.entries(policy.bindings)) {
            if (binding.principal === principal && binding.role === role)
              delete policy.bindings[bindingID];
          }
          if (get("assigned") === "true") {
            const scopes = get("scopes") ? JSON.parse(get("scopes")) : [null];
            scopes.forEach((scope, index) =>
              put(policy.bindings, `preview-${principal}-${role}-${index}`, {
                principal,
                role,
                ...(scope ? { scope } : {}),
              }),
            );
          }
          break;
        }
        case "owner-membership":
          for (const principal of policy.owners || [])
            apply(
              edit("membership", {
                principal,
                group: get("group"),
                assigned: get("assigned"),
              }),
            );
          break;
        case "remove-principal":
          for (const [bindingID, binding] of Object.entries(
            policy.bindings || {},
          )) {
            if (binding.principal === get("principal"))
              delete policy.bindings[bindingID];
          }
          break;
        case "owners":
          policy.owners = JSON.parse(get("entry"));
          break;
        case "admission": {
          const mode = get("general");
          policy.admission =
            mode === generalMode(policy)
              ? { ...policy.admission, principals: values.principal || [] }
              : {
                  owners: true,
                  signedIn: mode === "everyone",
                  principals: mode === "owners" ? [] : values.principal || [],
                };
          break;
        }
        case "delete-group":
          delete policy.roles[id];
          for (const [bindingID, binding] of Object.entries(
            policy.bindings || {},
          )) {
            if (binding.role === id) delete policy.bindings[bindingID];
          }
          for (const [grantID, rule] of Object.entries(policy.grants || {})) {
            if (!rule.subjects.roles?.includes(id)) continue;
            rule.subjects.roles = rule.subjects.roles.filter(
              (role) => role !== id,
            );
            if (
              !rule.subjects.workspace &&
              !rule.subjects.signedIn &&
              !rule.subjects.owners &&
              !rule.subjects.roles.length &&
              !rule.subjects.principals?.length
            )
              delete policy.grants[grantID];
          }
          break;
      }
    }

    function generalMode(value) {
      if (value.admission.signedIn) return "everyone";
      if (value.admission.principals?.length) return "selected";
      return value.admission.owners ? "owners" : "custom";
    }

    const headerBadge = document.querySelector(
      "[data-permission-site-badge] .badge",
    );
    if (headerBadge) {
      const mode = generalMode(baseline);
      headerBadge.textContent = {
        owners: "Only owners",
        everyone: "Everyone",
        selected: "Selected people",
        custom: "Custom site access",
      }[mode];
      headerBadge.className = `badge badge-${mode === "owners" ? "private" : mode === "everyone" ? "everyone" : "shared"}`;
      headerBadge.title =
        "Site entry only. Application permissions are configured separately.";
    }

    function closeDialog() {
      if (
        dialogDirty &&
        !window.confirm(
          "Discard the unapplied edits in this dialog? Changes already applied to the table will be kept.",
        )
      )
        return false;
      dialogDirty = false;
      dialog.close();
      return true;
    }

    function openDialog(label, eyebrow = "SHARING & PERMISSIONS") {
      if (dialog.open && !closeDialog()) return null;
      query("#permission-dialog-title").textContent = label;
      query("[data-permission-dialog-eyebrow]").textContent = eyebrow;
      dialogBody.replaceChildren();
      dialogDirty = false;
      dialog.showModal();
      return dialogBody;
    }

    function finishDialog() {
      dialogDirty = false;
      dialog.close();
    }

    function renderAdmission() {
      query("[data-general-access]").value = generalMode(policy);
      const target = query("[data-admission-entries]");
      target.replaceChildren();
      if (policy.admission.owners && !policy.admission.signedIn)
        target.append(element("span", "Site owners", "permission-chip muted"));
      for (const principal of policy.admission.principals || []) {
        const chip = element("span", undefined, "permission-chip");
        const remove = button(
          "×",
          () => {
            stage("admission", {
              general: generalMode(policy),
              principal: (policy.admission.principals || []).filter(
                (value) => value !== principal,
              ),
            });
          },
          "permission-icon-button",
        );
        remove.setAttribute(
          "aria-label",
          `Remove ${nameOf(principal)} from site access`,
        );
        chip.append(element("span", nameOf(principal)), remove);
        target.append(chip);
      }
      if (policy.admission.signedIn)
        target.append(
          element(
            "span",
            "Everyone signed in to the workspace · not the public internet",
            "permission-chip muted",
          ),
        );
      const boundary = query("[data-page-boundaries]");
      boundary.replaceChildren();
      const pages = Object.entries(policy.grants || {}).filter(
        ([, rule]) => rule.kind === "path",
      );
      const protectedPages = pages.some(
        ([, rule]) => rule.resource !== "*" || rule.except?.length,
      );
      const summary = element("div", undefined, "permission-page-summary");
      summary.append(
        element(
          "p",
          protectedPages
            ? "Some pages have their own access settings."
            : "Page permissions are checked after site entry.",
        ),
        button(
          "Review page access",
          () => {
            const body = openDialog("Page access", "SITE ACCESS · PAGES");
            if (!body) return;
            body.append(
              element(
                "p",
                "People must be admitted to the site and allowed to open the page. These settings do not grant access to app data.",
                "permission-description",
              ),
            );
            if (!pages.length)
              body.append(
                element(
                  "p",
                  "No page rules exist. Admitted people cannot open pages until a page rule allows it.",
                  "banner banner-warning",
                ),
              );
            pages.forEach(([id, rule]) => body.append(ruleCard(id, rule)));
          },
          "link-button",
        ),
      );
      boundary.append(summary);
    }

    function renderCell(kind, principal, id) {
      const role = policy.roles[id];
      const cell = element("td");
      const content = element("div", undefined, "permission-matrix-cell");
      const control = input("true", "checkbox");
      const defaults = kind === "default",
        owners = kind === "owners";
      if (kind === "workspace") {
        control.checked = Boolean(role.workspace);
        control.setAttribute(
          "aria-label",
          `${groupName(id)} for Workspace API callers`,
        );
        control.addEventListener("change", () =>
          stage("role", {
            id,
            entry: JSON.stringify({ ...role, workspace: control.checked }),
          }),
        );
        content.append(control);
        cell.append(content);
        return cell;
      }
      const current = defaults
        ? []
        : owners
          ? (policy.owners || []).flatMap((owner) => bindings(owner, id))
          : bindings(principal, id);
      const defaultFor = (value) =>
        Boolean(
          (role.signedIn &&
            !value.startsWith("automation:") &&
            !value.startsWith("site:")) ||
          (role.owners && policy.owners?.includes(value)),
        );
      const inherited =
        !defaults &&
        (owners
          ? policy.owners?.length > 0 && policy.owners.every(defaultFor)
          : defaultFor(principal));
      const fullyAssigned = owners
        ? policy.owners?.length > 0 &&
          policy.owners.every(
            (owner) => defaultFor(owner) || bindings(owner, id).length > 0,
          )
        : current.length > 0;
      control.checked = defaults
        ? Boolean(role.signedIn)
        : Boolean(inherited || fullyAssigned);
      control.indeterminate = Boolean(
        owners &&
        !fullyAssigned &&
        (current.length || policy.owners?.some(defaultFor)),
      );
      control.disabled = Boolean(inherited);
      const label = defaults
        ? "Everyone with site access"
        : owners
          ? "Site owners"
          : nameOf(principal);
      control.setAttribute("aria-label", `${groupName(id)} for ${label}`);
      control.title = defaults
        ? "Default assignment for signed-in people admitted to the site"
        : inherited
          ? "Inherited from the default row"
          : "Explicit assignments only. Directory memberships or other resource rules may grant additional access.";
      control.addEventListener("change", () => {
        if (defaults) {
          if (
            control.checked &&
            !window.confirm(
              `Give ${groupName(id)} to everyone admitted to this site? All its scoped rules will apply without membership limits. Individual rows cannot deny this default.`,
            )
          ) {
            control.checked = false;
            return;
          }
          stage("role", {
            id,
            entry: JSON.stringify({ ...role, signedIn: control.checked }),
          });
        } else if (owners) {
          if (
            control.checked &&
            !window.confirm(
              `Assign ${groupName(id)} without membership limits to every listed site owner? Its record and field restrictions still apply. Platform administrators do not receive these memberships automatically.`,
            )
          ) {
            renderMatrix();
            return;
          }
          stage("owner-membership", { group: id, assigned: control.checked });
        } else if (control.checked && scopeKinds(id).length) {
          control.checked = false;
          editMembership(principal, id);
        } else {
          stage("membership", {
            principal,
            group: id,
            assigned: control.checked,
          });
        }
      });
      content.append(control);
      if (inherited)
        content.append(
          element(
            "small",
            role.owners && !role.signedIn ? "Owners" : "Default",
            "permission-inherited",
          ),
        );
      else if (!defaults && (current.length || control.indeterminate)) {
        const scopes = current.map((binding) => binding.scope).filter(Boolean);
        if (owners)
          content.append(
            element(
              "small",
              control.indeterminate
                ? "Some owners assigned"
                : scopes.length
                  ? "Limited / mixed memberships"
                  : "Explicit assignments",
              "field-hint",
            ),
          );
        else if (scopes.length || scopeKinds(id).length) {
          const text = current.some((binding) => !binding.scope)
            ? "No membership limit"
            : scopes
                .map((scope) => `${title(scope.kind)}: ${scope.id}`)
                .join(", ");
          const limitButton = button(
            text,
            () => editMembership(principal, id),
            "permission-scope-button",
          );
          limitButton.setAttribute("aria-label", text);
          content.append(limitButton);
        }
      }
      cell.append(content);
      return cell;
    }

    function renderMatrix() {
      const target = query("[data-permission-matrix]");
      target.replaceChildren();
      const groups = Object.keys(policy.roles || {}).sort((a, b) =>
        groupName(a).localeCompare(groupName(b)),
      );
      if (!groups.length) {
        target.append(
          element(
            "p",
            "No permission groups yet. Create a group, add resource rules, then assign it in this table.",
            "permission-empty",
          ),
        );
        return;
      }
      const table = element("table", undefined, "permission-assignment-table");
      table.append(
        element(
          "caption",
          "Explicit permission-group assignments. Directory membership may grant additional access.",
          "visually-hidden",
        ),
      );
      const head = element("thead"),
        headings = element("tr"),
        who = element("th", "Who");
      who.scope = "col";
      headings.append(who);
      for (const id of groups) {
        const heading = element("th");
        heading.scope = "col";
        const editGroup = button(
          groupName(id),
          () => openGroup(id),
          "permission-column-heading",
        );
        editGroup.setAttribute(
          "aria-label",
          `Edit ${groupName(id)} permission group`,
        );
        heading.append(
          editGroup,
          element(
            "small",
            policy.roles[id].description ||
              `${groupRules(id).length} resource rules`,
            "field-hint",
          ),
        );
        headings.append(heading);
      }
      const actionHeading = element("th");
      actionHeading.scope = "col";
      actionHeading.append(element("span", "Row actions", "visually-hidden"));
      headings.append(actionHeading);
      head.append(headings);
      const body = element("tbody");
      const principals = [
        ...new Set([
          ...Object.values(policy.bindings || {}).map(
            (binding) => binding.principal,
          ),
          ...extraRows,
        ]),
      ].sort((a, b) => nameOf(a).localeCompare(nameOf(b)));
      // Uniform owner memberships are represented by the system row. Mixed
      // memberships remain visible individually instead of being flattened.
      const owners = policy.owners || [];
      const ownerRowsNeeded = groups.some((id) => {
        const memberships = owners.map((owner) =>
          JSON.stringify(
            bindings(owner, id)
              .map((binding) => binding.scope || null)
              .sort((a, b) =>
                JSON.stringify(a).localeCompare(JSON.stringify(b)),
              ),
          ),
        );
        return (
          new Set(memberships).size > 1 ||
          (policy.roles[id].signedIn &&
            owners.some((owner) => owner.startsWith("automation:")) &&
            owners.some((owner) => !owner.startsWith("automation:"))) ||
          owners.some((owner) =>
            bindings(owner, id).some((binding) => binding.scope),
          )
        );
      });
      const rows = [
        ["default", ""],
        ["owners", ""],
        ...(groups.some((id) => policy.roles[id].workspace)
          ? [["workspace", ""]]
          : []),
        ...[...new Set([...principals, ...(ownerRowsNeeded ? owners : [])])]
          .filter(
            (principal) =>
              !owners.includes(principal) ||
              ownerRowsNeeded ||
              extraRows.has(principal),
          )
          .map((principal) => ["custom", principal]),
      ];
      who.append(
        element(
          "small",
          `${rows.filter(([kind]) => kind === "custom").length} custom rows · no automatic user list`,
        ),
      );
      for (const [kind, principal] of rows) {
        const row = element("tr");
        if (kind !== "custom") row.className = "permission-system-row";
        row.dataset.permissionRow = kind === "custom" ? principal : kind;
        const heading = element("th");
        heading.scope = "row";
        if (kind === "custom")
          heading.append(
            element(
              "span",
              principal.startsWith("group:")
                ? "DIRECTORY GROUP"
                : principal.startsWith("automation:")
                  ? "AUTOMATION"
                  : principal.startsWith("site:")
                    ? "APP"
                    : principal.startsWith("role:")
                      ? "DIRECTORY ROLE"
                      : "PERSON",
              "permission-row-kind",
            ),
          );
        heading.append(
          element(
            "strong",
            kind === "default"
              ? "Everyone with site access"
              : kind === "owners"
                ? "Site owners"
                : kind === "workspace"
                  ? "Workspace API callers"
                  : nameOf(principal),
          ),
        );
        heading.append(
          element(
            "small",
            kind === "default"
              ? "Default permissions · no directory listing"
              : kind === "owners"
                ? owners.map(nameOf).join(", ") || "No listed owners"
                : kind === "workspace"
                  ? "Brokered calls only · no page admission"
                  : labels.get(principal)?.detail || principal,
            "field-hint",
          ),
        );
        if (kind === "owners") {
          const ownerRules = Object.entries(policy.grants || {}).filter(
            ([, rule]) => rule.kind !== "path" && rule.subjects.owners,
          );
          if (ownerRules.length)
            heading.append(
              button(
                "Also has existing access ↗",
                () => openAccessRules("Site owners", ownerRules),
                "link-button",
              ),
            );
          if (
            owners.length &&
            groups.some(
              (id) =>
                !policy.roles[id].signedIn &&
                owners.some((owner) => !bindings(owner, id).length),
            )
          )
            heading.append(
              button(
                "Assign all groups",
                () => {
                  if (
                    !window.confirm(
                      "Assign every current permission group without membership limits to all listed site owners? Record and field conditions still apply. Existing rules are unchanged.",
                    )
                  )
                    return;
                  for (const id of groups)
                    if (!policy.roles[id].signedIn)
                      stage("owner-membership", {
                        group: id,
                        assigned: "true",
                      });
                },
                "link-button",
              ),
            );
        }
        if (
          kind === "custom" &&
          (principal.startsWith("automation:") || principal.startsWith("site:"))
        ) {
          heading.append(
            element(
              "small",
              "Execution identity · signed-in defaults do not apply",
              "field-hint",
            ),
          );
        } else if (
          kind === "custom" &&
          !policy.admission.signedIn &&
          !policy.admission.principals?.includes(principal) &&
          !(policy.admission.owners && owners.includes(principal))
        ) {
          heading.append(
            element(
              "small",
              "No direct site entry; other memberships may admit them",
              "field-hint",
            ),
          );
        }
        row.append(heading);
        for (const id of groups) row.append(renderCell(kind, principal, id));
        const actions = element("td");
        if (kind === "custom") {
          const remove = button(
            "×",
            () => {
              if (
                !window.confirm(
                  `Remove all direct permission-group assignments for ${nameOf(principal)}? Defaults, directory-group memberships, direct resource rules, and site entry are unchanged.`,
                )
              )
                return;
              extraRows.delete(principal);
              stage("remove-principal", { principal });
            },
            "permission-icon-button",
          );
          remove.setAttribute(
            "aria-label",
            `Remove ${nameOf(principal)} permission row`,
          );
          actions.append(remove);
        }
        row.append(actions);
        body.append(row);
      }
      table.append(head, body);
      target.append(table);
    }

    function resourceName(rule) {
      const all = {
        path: "All site pages",
        collection: "All data collections",
        file: "All uploaded files",
        channel: "All realtime channels",
        action: "All app actions",
        integration: "All integration endpoints",
      };
      if (rule.resource === "*") return all[rule.kind];
      return `${ui.kindLabels[rule.kind]} · ${rule.resource}`;
    }

    function ruleSummary(rule) {
      const actions = rule.operations
        .map((operation) => ui.operationLabels[operation] || operation)
        .join(", ");
      return `${resourceName(rule)} — ${actions.toLowerCase()}`;
    }

    function ruleCard(id, rule) {
      const card = element("div", undefined, "permission-rule-card");
      card.append(
        element("h4", resourceName(rule)),
        element(
          "p",
          rule.operations
            .map((operation) => ui.operationLabels[operation] || operation)
            .join(" · "),
        ),
      );
      const limits = ui.limitsText(rule);
      if (limits.length) {
        const list = element("ul", undefined, "permission-explanations");
        limits.forEach((text) => list.append(element("li", text)));
        card.append(list);
      }
      card.append(
        element(
          "p",
          `Who: ${ui.audienceText(rule.subjects, labels, policy.roles)}`,
          "field-hint",
        ),
      );
      const actions = element("div", undefined, "form-actions");
      actions.append(
        button("Edit rule", () => openRule(id, rule)),
        button(
          "Delete rule",
          () => {
            if (
              !window.confirm(
                `Delete this rule for ${rule.resource} for ALL its audiences? Other rules can still grant access.`,
              )
            )
              return;
            stage("remove-grant", { id });
            finishDialog();
          },
          "link-button",
        ),
      );
      card.append(actions);
      return card;
    }

    function openAccessRules(audience, rules) {
      const target = openDialog(audience, "ACCESS OUTSIDE PERMISSION GROUPS");
      if (!target) return;
      target.append(
        element(
          "p",
          "These existing rules grant access independently of the permission table. They may have been created by an earlier sharing setup or by the app’s configuration. They stay in effect until explicitly edited or deleted here.",
          "permission-description",
        ),
      );
      target.append(
        element(
          "p",
          "Site entry is still required. Deleting a rule affects every audience listed on it; other rules may continue to grant access.",
          "field-hint",
        ),
      );
      rules.forEach(([id, rule]) => target.append(ruleCard(id, rule)));
    }

    function renderIndependentRules() {
      const target = query("[data-independent-rules]");
      target.replaceChildren();
      const rules = Object.entries(policy.grants || {}).filter(
        ([, rule]) =>
          rule.kind !== "path" &&
          (rule.subjects.workspace ||
            rule.subjects.signedIn ||
            rule.subjects.owners ||
            rule.subjects.principals?.length),
      );
      query("[data-additional-access]").hidden = !rules.length;
      query("[data-additional-summary]").textContent =
        `· ${rules.length} existing ${rules.length === 1 ? "rule" : "rules"}`;
      const audiences = new Map();
      for (const [id, rule] of rules) {
        const subjects = {
          workspace: Boolean(rule.subjects.workspace),
          signedIn: Boolean(rule.subjects.signedIn),
          owners: Boolean(rule.subjects.owners),
          principals: [...(rule.subjects.principals || [])].sort(),
        };
        const key = JSON.stringify(subjects);
        if (!audiences.has(key)) audiences.set(key, { subjects, rules: [] });
        audiences.get(key).rules.push([id, rule]);
      }
      for (const { subjects, rules } of audiences.values()) {
        const name = ui.audienceText(subjects, labels, policy.roles);
        const row = element("div", undefined, "permission-access-row");
        const description = element("div");
        description.append(element("strong", name));
        const resources = [
          ...new Set(rules.map(([, rule]) => resourceName(rule))),
        ];
        description.append(element("p", resources.join(" · ")));
        if (
          rules.some(
            ([, rule]) =>
              rule.where ||
              rule.check ||
              rule.scope ||
              Array.isArray(rule.fields) ||
              rule.except?.length,
          )
        )
          description.append(
            element(
              "p",
              "Some permissions have record, field or resource limits. Review access for details.",
            ),
          );
        row.append(
          description,
          button(
            "Review access",
            () => openAccessRules(name, rules),
            "button button-secondary",
          ),
          button(
            "Move into a permission group",
            () => {
              const id = `portal-${crypto.randomUUID()}`;
              const baseName = subjects.signedIn
                ? "Default permissions"
                : subjects.workspace
                  ? "Workspace app permissions"
                  : subjects.owners
                    ? "Site owner permissions"
                    : subjects.principals.length === 1
                      ? `${name.slice(0, 100)} permissions`
                      : "Selected people permissions";
              let groupLabel = baseName;
              let suffix = 2;
              while (
                Object.keys(policy.roles || {}).some(
                  (group) =>
                    groupName(group).toLowerCase() === groupLabel.toLowerCase(),
                )
              ) {
                groupLabel = `${baseName} ${suffix++}`;
              }
              const group = {
                name: groupLabel,
                signedIn: subjects.signedIn,
                owners: subjects.owners,
                workspace: subjects.workspace,
              };
              stage("role", {
                id,
                creating: "true",
                entry: JSON.stringify(group),
              });
              for (const principal of subjects.principals)
                stage("membership", { principal, group: id, assigned: "true" });
              for (const [ruleID, rule] of rules)
                stage("grant", {
                  id: ruleID,
                  keep_where: "true",
                  keep_check: "true",
                  entry: JSON.stringify({
                    ...rule,
                    subjects: { roles: [...(rule.subjects.roles || []), id] },
                  }),
                });
              render();
              announce(
                "Existing access is represented by a permission group in the draft. Save changes to apply.",
              );
            },
            "button button-secondary",
          ),
        );
        target.append(row);
      }
    }

    function renderDefinitions() {
      const target = query("[data-group-definitions]");
      target.replaceChildren();
      for (const id of Object.keys(policy.roles || {})) {
        const section = element(
          "section",
          undefined,
          "permission-group-definition",
        );
        const heading = element("div", undefined, "permission-heading");
        heading.append(
          element("h4", groupName(id)),
          button("Edit group", () => openGroup(id), "link-button"),
        );
        const rules = groupRules(id);
        const list = element("ul");
        for (const [, rule] of rules) {
          const item = element("li", ruleSummary(rule));
          if (
            rule.scope ||
            rule.where ||
            rule.check ||
            Array.isArray(rule.fields) ||
            rule.except?.length
          )
            item.append(
              element("span", ". Limits apply — open the group for details."),
            );
          list.append(item);
        }
        if (!rules.length)
          list.append(
            element(
              "li",
              "No resource permissions yet. Assigning this group does not grant data or action access.",
            ),
          );
        section.append(heading);
        if (policy.roles[id].description)
          section.append(
            element("p", policy.roles[id].description, "field-hint"),
          );
        section.append(list);
        target.append(section);
      }
      if (!target.children.length)
        target.append(
          element(
            "p",
            "Create a permission group to define what it allows.",
            "field-hint",
          ),
        );
    }

    function render() {
      manager
        .querySelectorAll("[data-workspace-sharing]")
        .forEach((control) => {
          const pending = edits
            .filter(
              (edit) =>
                edit.action === "workspace-sharing" &&
                edit.values.kind[0] === control.dataset.kind &&
                edit.values.resource[0] === control.dataset.resource,
            )
            .at(-1);
          control.value = pending
            ? pending.values.mode[0]
            : control.dataset.originalMode;
          const status = manager.querySelector(
            `[data-workspace-status="${control.dataset.grantId}"]`,
          );
          status.textContent =
            control.value === "workspace"
              ? "Shared with workspace"
              : "Access by permission";
        });
      const matrixFocus = query("[data-permission-matrix]").contains(
        document.activeElement,
      )
        ? document.activeElement.getAttribute("aria-label")
        : null;
      renderAdmission();
      renderMatrix();
      renderIndependentRules();
      renderDefinitions();
      query("[data-save-state]").textContent = edits.length
        ? "You have unsaved changes"
        : "All changes saved";
      query("[data-save-permissions]").disabled = !edits.length || saving;
      query("[data-discard-permissions]").disabled = !edits.length || saving;
      saveForm.classList.toggle("has-changes", Boolean(edits.length));
      saveForm.elements.entry.value = JSON.stringify(edits);
      if (matrixFocus)
        [...query("[data-permission-matrix]").querySelectorAll("[aria-label]")]
          .find((control) => control.getAttribute("aria-label") === matrixFocus)
          ?.focus({ preventScroll: true });
    }

    function openPicker(forAdmission) {
      const target = openDialog(
        forAdmission ? "Add site access" : "Add a custom permission row",
      );
      if (!target) return;
      const alsoAdmit = checkbox(
        "Also give this person or directory group site access",
      );
      const status = element("p", "", "field-hint");
      status.setAttribute("role", "status");
      const originalAdmission = new Set(policy.admission.principals || []);
      const originalRows = new Set(extraRows);
      const selected = new Set(forAdmission ? originalAdmission : []);
      const members = element("ul", undefined, "permission-members");
      members.setAttribute("aria-label", "Selected people and groups");
      const empty = element(
        "p",
        "No people or groups selected yet.",
        "field-hint",
      );
      const focusSearch = () => {
        target.querySelector("input[type=search]").focus({
          preventScroll: true,
        });
      };
      const renderSelected = () => {
        members.replaceChildren();
        empty.hidden = selected.size > 0;
        for (const principal of selected) {
          const row = element("li");
          const description = element("div");
          description.append(element("strong", nameOf(principal)));
          const detail = labels.get(principal)?.detail;
          if (detail) description.append(element("span", detail, "muted"));
          const remove = button(
            "Remove",
            () => {
              const index = [...selected].indexOf(principal);
              selected.delete(principal);
              if (
                (forAdmission || !originalAdmission.has(principal)) &&
                policy.admission.principals?.includes(principal)
              ) {
                stage("admission", {
                  general: generalMode(policy),
                  principal: policy.admission.principals.filter(
                    (value) => value !== principal,
                  ),
                });
              }
              if (!forAdmission && !originalRows.has(principal)) {
                extraRows.delete(principal);
              }
              dialogDirty = false;
              render();
              renderSelected();
              status.textContent = `${nameOf(principal)} removed from the selection. Save changes to apply.`;
              announce(status.textContent);
              const nextRow =
                members.children[Math.min(index, members.children.length - 1)];
              (nextRow?.querySelector("button") || done).focus({
                preventScroll: true,
              });
            },
            "link-button",
          );
          remove.setAttribute(
            "aria-label",
            `Remove ${nameOf(principal)} from selection`,
          );
          row.append(description, remove);
          members.append(row);
        }
      };
      target.append(
        element(
          "p",
          forAdmission
            ? "Choose who may enter. They receive the default app permissions, not every permission group."
            : "Add only people or directory groups needing extra permissions. Then tick their permission groups in the table.",
          "muted",
        ),
      );
      target.append(
        ui.picker("Search people or directory groups", (entry) => {
          labels.set(entry.principal, entry);
          if (forAdmission || alsoAdmit.control.checked) {
            const mode = generalMode(policy);
            stage("admission", {
              general: mode === "owners" ? "selected" : mode,
              principal: [
                ...new Set([
                  ...(policy.admission.principals || []),
                  entry.principal,
                ]),
              ],
            });
          }
          if (!forAdmission) extraRows.add(entry.principal);
          selected.add(entry.principal);
          dialogDirty = false;
          render();
          renderSelected();
          status.textContent = forAdmission
            ? `${entry.name} added to site access in the draft. Add more or choose Done, then save changes to apply.`
            : `${entry.name} added to the table. Add more or choose Done, then tick their permission groups and save changes.`;
          announce(status.textContent);
          focusSearch();
        }),
      );
      if (!forAdmission) target.append(alsoAdmit.wrapper);
      target.append(
        element(
          "p",
          "Search includes people who have signed in and directory groups known to the platform. Directory groups' membership comes from sign-in claims, not this table.",
          "field-hint",
        ),
      );
      target.append(
        element(
          "h3",
          forAdmission
            ? "Selected people and groups"
            : "Selected in this dialog",
        ),
        members,
        empty,
      );
      renderSelected();
      const actions = element("div", undefined, "form-actions");
      const done = button("Done", finishDialog, "button");
      actions.append(done);
      target.append(status, actions);
      target.querySelector("input[type=search]").focus();
    }

    function openGroup(id = "") {
      const target = openDialog(
        id
          ? `Edit ${groupName(id)} permission group`
          : "Create a permission group",
        "OWNED BY THIS SITE",
      );
      if (!target) return;
      const original = policy.roles?.[id] || {};
      const name = input(original.name || (id ? title(id) : ""));
      name.required = true;
      name.maxLength = 120;
      const description = input(original.description || "");
      const owners = checkbox(
        id
          ? "Assign this permission group to all listed site owners"
          : "Assign this new group to all listed site owners",
        !id ||
          original.owners ||
          (policy.owners?.length > 0 &&
            policy.owners.every((owner) => bindings(owner, id).length > 0)),
      );
      if (id && !owners.control.checked)
        owners.control.indeterminate = policy.owners?.some(
          (owner) => bindings(owner, id).length > 0,
        );
      let ownersEdited = false;
      owners.control.addEventListener("change", () => {
        ownersEdited = true;
      });
      const form = element(
        "form",
        undefined,
        "permission-edit-form permission-group-form",
      );
      form.append(
        field("Permission group name", name),
        field("Description", description),
        owners.wrapper,
      );
      if (id && original.signedIn)
        form.append(
          element(
            "p",
            "The default row also gives this group to admitted people, including owners. Unchecking an owner assignment does not deny that default.",
            "field-hint",
          ),
        );
      if (!id)
        form.append(
          element(
            "p",
            "A new group is empty until you add resource rules. Owner assignments are explicit, not a bypass of those rules.",
            "field-hint",
          ),
        );
      const ruleChoices = [];
      if (id) {
        form.append(
          element("h4", "What does this permission group allow?"),
          element(
            "p",
            "These are this group’s resource permissions. Untick one to remove it from this group, or edit it to change its operations and limits.",
            "field-hint",
          ),
        );
        const rules = groupRules(id);
        const resources = element(
          "div",
          undefined,
          "permission-group-resources",
        );
        for (const kind of new Set(rules.map(([, rule]) => rule.kind))) {
          const section = element("fieldset");
          section.append(element("legend", ui.kindLabels[kind]));
          for (const [ruleID, rule] of rules.filter(
            ([, rule]) => rule.kind === kind,
          )) {
            const choice = checkbox(ruleSummary(rule), true);
            ruleChoices.push({ id: ruleID, rule, control: choice.control });
            const option = element("div", undefined, "permission-group-option");
            option.append(choice.wrapper);
            const limits = ui.limitsText(rule);
            if (limits.length)
              option.append(
                disclosure(
                  `${limits.length} ${limits.length === 1 ? "limit" : "limits"} apply`,
                  element("p", limits.join(". ")),
                  "permission-rule-limits",
                ),
              );
            option.append(
              button(
                "Edit rule",
                () => openRule(ruleID, rule, id),
                "link-button",
              ),
            );
            section.append(option);
          }
          resources.append(section);
        }
        if (!rules.length)
          resources.append(
            element(
              "p",
              "No resource rules yet. This group grants no data access.",
              "field-hint",
            ),
          );
        form.append(
          resources,
          button(
            "+ Add resource rule",
            () => openRule("", {}, id),
            "button button-secondary",
          ),
        );
      }
      const submit = element(
        "button",
        id ? "Apply group changes" : "Add permission group",
        "button",
      );
      submit.type = "submit";
      const actions = element("div", undefined, "form-actions");
      actions.append(submit);
      if (id)
        actions.append(
          button(
            "Delete permission group",
            () => {
              if (
                !window.confirm(
                  `Delete ${groupName(id)} and its app permissions and assignments? Other groups and site admission are unchanged.`,
                )
              )
                return;
              stage("delete-group", { id });
              finishDialog();
            },
            "link-button permission-delete",
          ),
        );
      form.append(actions);
      form.addEventListener("submit", (event) => {
        event.preventDefault();
        const groupID = id || `portal-${crypto.randomUUID()}`;
        if (!name.value.trim()) {
          name.focus();
          return;
        }
        if (
          Object.entries(policy.roles || {}).some(
            ([otherID]) =>
              otherID !== id &&
              groupName(otherID).toLowerCase() ===
                name.value.trim().toLowerCase(),
          )
        ) {
          showError("A permission group with that name already exists.");
          return;
        }
        if (
          ownersEdited &&
          owners.control.checked &&
          !window.confirm(
            "Assign this group without membership limits to all listed site owners? Record and field conditions still apply.",
          )
        )
          return;
        stage("role", {
          id: groupID,
          entry: JSON.stringify({
            ...original,
            name: name.value.trim(),
            description: description.value.trim(),
            ...(original.owners && ownersEdited
              ? { owners: owners.control.checked }
              : {}),
          }),
          ...(id
            ? {}
            : { creating: "true", assign_owners: owners.control.checked }),
        });
        if (id && ownersEdited)
          stage("owner-membership", {
            group: id,
            assigned: owners.control.checked,
          });
        for (const choice of ruleChoices) {
          if (choice.control.checked) continue;
          const rule = choice.rule;
          const subjects = {
            ...rule.subjects,
            roles: (rule.subjects.roles || []).filter((role) => role !== id),
          };
          if (
            !subjects.workspace &&
            !subjects.signedIn &&
            !subjects.owners &&
            !subjects.principals?.length &&
            !subjects.roles.length
          )
            stage("remove-grant", { id: choice.id });
          else
            stage("grant", {
              id: choice.id,
              entry: JSON.stringify({ ...rule, subjects }),
              keep_where: "true",
              keep_check: "true",
            });
        }
        finishDialog();
        if (!id) openGroup(groupID);
      });
      target.append(form);
      if (id) {
        target.append(
          disclosure(
            "App identifier",
            element(
              "p",
              `Stable identifier: ${id}. Changing the display name does not change app code or existing rules.`,
              "field-hint",
            ),
            "permission-identifier",
          ),
        );
      }
      name.focus();
    }

    function openRule(id, rule, groupID = "") {
      if (
        id &&
        rule.subjects.roles?.length === 1 &&
        !rule.subjects.workspace &&
        !rule.subjects.signedIn &&
        !rule.subjects.owners &&
        !rule.subjects.principals?.length
      )
        groupID = rule.subjects.roles[0];
      const target = openDialog(
        id ? "Edit resource rule" : "Add resource rule",
        groupID
          ? `PERMISSION GROUP · ${groupName(groupID)}`
          : "CUSTOM RESOURCE ACCESS",
      );
      if (!target) return;
      if (id && !groupID)
        target.append(
          element(
            "p",
            "Editing this rule changes it for every audience listed in it. Other rules and their limits are unchanged.",
            "banner banner-warning",
          ),
        );
      else
        target.append(
          element(
            "p",
            "Choose an app resource and the operations this group may perform. The app defines resources and actions; this form only grants access to them.",
            "field-hint",
          ),
        );
      const shared =
        groupID &&
        id &&
        (rule.subjects.roles?.length > 1 ||
          rule.subjects.workspace ||
          rule.subjects.signedIn ||
          rule.subjects.owners ||
          rule.subjects.principals?.length);
      const groupRule = groupID
        ? { ...rule, subjects: { roles: [groupID] } }
        : rule;
      if (shared)
        target.append(
          element(
            "p",
            "This existing rule is shared. Saving here creates an independent permission for this group; other recipients keep the original rule.",
            "field-hint",
          ),
        );
      const form = ui.grantEditor(
        manager,
        policy,
        labels,
        id,
        groupRule,
        groupID,
      );
      if (shared) {
        form.dataset.splitGroup = groupID;
        form.dataset.splitRule = id;
      }
      target.append(form);
      target.querySelector("select")?.focus();
    }

    manager.permissionStage = (action, form) => {
      const values = {};
      for (const [key, value] of new FormData(form)) {
        if (key === "base" || key === "editor_action") continue;
        if (!Object.hasOwn(values, key)) put(values, key, []);
        values[key].push(String(value));
      }
      if (action === "grant" && !values.id?.[0]) {
        values.id = [`portal-${crypto.randomUUID()}`];
        values.creating = ["true"];
      }
      if (action === "grant" && form.dataset.splitGroup) {
        const source = form.dataset.splitRule;
        const rule = policy.grants[source];
        const subjects = {
          ...rule.subjects,
          roles: rule.subjects.roles.filter(
            (role) => role !== form.dataset.splitGroup,
          ),
        };
        stage("grant", {
          id: source,
          entry: JSON.stringify({ ...rule, subjects }),
          keep_where: "true",
          keep_check: "true",
        });
        values.id = [`portal-${crypto.randomUUID()}`];
        values.creating = ["true"];
        values.copy_from = [source];
      }
      stage(action, values);
      finishDialog();
    };

    function editMembership(principal, id) {
      const target = openDialog(
        `Membership limits · ${nameOf(principal)} · ${groupName(id)}`,
      );
      if (!target) return;
      const current = bindings(principal, id);
      const mode = select(
        [
          ["all", "No membership limit"],
          ["limited", "Limit this assignment"],
        ],
        current.length && current.every((binding) => binding.scope)
          ? "limited"
          : "all",
      );
      const options = element("div", undefined, "permission-scope-options");
      const kinds = scopeKinds(id);
      const rows = element("div");
      const add = (scope = {}) => {
        const row = element("div", undefined, "permission-form-grid");
        const kind = input(scope.kind || kinds[0] || "");
        kind.required = true;
        const value = input(scope.id || "");
        value.required = true;
        row.append(
          field("Limit type", kind),
          field("Allowed identifier", value),
          button(
            "Remove limit",
            () => {
              row.remove();
              dialogDirty = true;
            },
            "link-button",
          ),
        );
        row.read = () => ({ kind: kind.value.trim(), id: value.value.trim() });
        rows.append(row);
      };
      current
        .filter((binding) => binding.scope)
        .forEach((binding) => add(binding.scope));
      if (!rows.children.length) add();
      options.append(
        rows,
        button("Add another allowed identifier", () => {
          add();
          dialogDirty = true;
        }),
      );
      const update = () => {
        options.hidden = mode.value === "all";
        options.querySelectorAll("input").forEach((control) => {
          control.disabled = options.hidden;
        });
      };
      mode.addEventListener("change", update);
      update();
      const form = element("form", undefined, "permission-edit-form");
      form.append(
        field("Where does this membership apply?", mode),
        options,
        element(
          "p",
          `Available rule limit types: ${kinds.join(", ") || "none configured"}. Limited memberships match only rules with the same limit type; they do not grant unscoped rules such as ordinary pages. Defaults and other memberships may still give wider access.`,
          "field-hint",
        ),
      );
      const submit = element("button", "Apply membership", "button");
      submit.type = "submit";
      form.append(submit);
      form.addEventListener("submit", (event) => {
        event.preventDefault();
        const scopes =
          mode.value === "all"
            ? [null]
            : [...rows.children].map((row) => row.read());
        if (
          !scopes.length ||
          scopes.some((scope) => scope && (!scope.kind || !scope.id))
        ) {
          showError(
            "Choose at least one complete limit, or use No membership limit.",
          );
          return;
        }
        if (
          new Set(scopes.map((scope) => JSON.stringify(scope))).size !==
          scopes.length
        ) {
          showError("Each membership limit must be unique.");
          return;
        }
        stage("membership", {
          principal,
          group: id,
          assigned: "true",
          scopes: JSON.stringify(scopes),
        });
        finishDialog();
      });
      target.append(form);
      mode.focus();
    }

    manager.querySelectorAll("[data-workspace-sharing]").forEach((control) => {
      control.addEventListener("change", () => {
        stage("workspace-sharing", {
          kind: control.dataset.kind,
          resource: control.dataset.resource,
          mode: control.value,
          grant_id: control.dataset.grantId,
          consumer_id: control.dataset.consumerId,
        });
      });
    });
    query("[data-general-access]").addEventListener("change", (event) => {
      const mode = event.target.value;
      if (
        mode !== generalMode(policy) &&
        !window.confirm(
          "Replace the current site-entry selectors with this setting? Existing app permissions and memberships will be kept; protected pages keep their exclusions.",
        )
      ) {
        renderAdmission();
        return;
      }
      stage("admission", {
        general: mode,
        principal: mode === "owners" ? [] : policy.admission.principals || [],
      });
    });
    query("[data-add-admission]").addEventListener("click", () =>
      openPicker(true),
    );
    query("[data-add-permission-row]").addEventListener("click", () =>
      openPicker(false),
    );
    query("[data-new-permission-group]").addEventListener("click", () =>
      openGroup(),
    );
    query("[data-add-direct-rule]").addEventListener("click", () =>
      openRule("", {}),
    );
    query("[data-edit-owners]").addEventListener("click", () => {
      const target = openDialog("Manage site owners");
      if (!target) return;
      target.append(
        element(
          "p",
          "Owners can publish and change permissions. This does not automatically assign application groups, grant data access, or add site entry when the admission policy excludes owners.",
          "field-hint",
        ),
      );
      const owners = ui.principalList(
        policy.owners || [],
        labels,
        "Add site owner",
      );
      target.append(
        owners.wrapper,
        button(
          "Apply site owners",
          () => {
            if (!owners.value().length) {
              showError("Keep at least one site owner.");
              return;
            }
            stage("owners", { entry: JSON.stringify(owners.value()) });
            finishDialog();
          },
          "button",
        ),
      );
    });
    query("[data-close-permission-dialog]").addEventListener(
      "click",
      closeDialog,
    );
    dialog.addEventListener("cancel", (event) => {
      event.preventDefault();
      closeDialog();
    });
    dialog.addEventListener("input", () => {
      dialogDirty = true;
    });
    dialog.addEventListener("change", () => {
      dialogDirty = true;
    });
    dialog.addEventListener("permissionDraftChanged", () => {
      dialogDirty = true;
    });
    advanced.addEventListener("input", () => {
      advancedDirty = advanced.elements.policy.value !== originalAdvanced;
    });
    advanced.append(
      button(
        "Discard technical edits",
        () => {
          if (
            advancedDirty &&
            !window.confirm(
              "Discard unsaved changes in the technical editor? Table changes will be kept.",
            )
          )
            return;
          advanced.elements.policy.value = originalAdvanced;
          advancedDirty = false;
          announce("Technical edits discarded. Table changes were kept.");
        },
        "link-button",
      ),
    );
    advanced.addEventListener(
      "submit",
      (event) => {
        if (edits.length) {
          event.preventDefault();
          event.stopImmediatePropagation();
          showError(
            "Save or discard the table changes before saving the separate technical editor. Your edits have been kept.",
          );
        }
      },
      true,
    );
    saveForm.addEventListener(
      "submit",
      (event) => {
        if (advancedDirty) {
          event.preventDefault();
          event.stopImmediatePropagation();
          showError(
            "The technical editor also has unsaved changes. Discard technical edits to save the table, or discard table changes to save the technical editor. Your edits have been kept.",
          );
        }
      },
      true,
    );
    query("[data-discard-permissions]").addEventListener("click", () => {
      if (
        !window.confirm(
          "Discard all unsaved table changes? Saved permissions and the separate technical editor are unchanged.",
        )
      )
        return;
      policy = structuredClone(baseline);
      edits = [];
      extraRows = new Set();
      render();
      error.hidden = true;
      announce("Unsaved table changes discarded.");
    });
    manager.addEventListener("permissionError", (event) => {
      saving = false;
      manager.inert = false;
      render();
      showError(
        event.detail.message || "Nothing was saved. Your edits have been kept.",
      );
    });
    manager.addEventListener("htmx:beforeRequest", (event) => {
      if (event.detail.elt !== saveForm && event.detail.elt !== advanced)
        return;
      if (saving) {
        event.preventDefault();
        return;
      }
      saving = true;
      manager.inert = true;
      query("[data-save-permissions]").disabled = true;
      query("[data-discard-permissions]").disabled = true;
      error.hidden = true;
    });
    manager.addEventListener("htmx:afterRequest", () => {
      saving = false;
      manager.inert = false;
      render();
    });
    manager.addEventListener(
      "click",
      (event) => {
        if (saving && !event.target.closest("[data-permission-save]")) {
          event.preventDefault();
          event.stopImmediatePropagation();
        }
      },
      true,
    );
    render();
    window.htmx?.process(manager);
    return { manager, dirty, showError };
  }

  document.addEventListener("htmx:beforeSwap", (event) => {
    if (event.detail.target?.id !== "permission-manager") return;
    if (event.detail.xhr.getResponseHeader("HX-Reswap") === "none") return;
    const fragment = new DOMParser().parseFromString(
      event.detail.xhr.responseText,
      "text/html",
    );
    if (!fragment.querySelector("#permission-manager[data-policy]")) {
      event.detail.shouldSwap = false;
      active?.showError(
        "The response was not a permission editor. Your sign-in may have expired. Edits were kept; reload only after copying any changes you need.",
      );
    }
  });
  window.addEventListener("beforeunload", (event) => {
    if (!active?.manager.isConnected || !active.dirty()) return;
    event.preventDefault();
    event.returnValue = "";
  });
  document.addEventListener("htmx:afterSwap", setup);
  setup();
})();

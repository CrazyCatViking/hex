// Visual resource-rule controls shared by the permission matrix. Policy and
// directory values are data, never markup. Forms stage narrow server edits.
(() => {
  let sequence = 0;
  const element = (tag, text, className) => {
    const result = document.createElement(tag);
    if (text !== undefined) result.textContent = text;
    if (className) result.className = className;
    return result;
  };
  const input = (value = "", type = "text") => {
    const control = element("input");
    control.type = type;
    control.value = value;
    return control;
  };
  const hidden = (form, name, value = "") => {
    const control = input(value, "hidden");
    control.name = name;
    form.append(control);
    return control;
  };
  const select = (options, value) => {
    const control = element("select");
    for (const [id, label] of options) {
      const option = element("option", label);
      option.value = id;
      option.selected = id === value;
      control.append(option);
    }
    return control;
  };
  const field = (label, control, hint = "") => {
    const wrapper = element("label", undefined, "field");
    const name = element("span", label, "field-label");
    name.id = `permission-field-${++sequence}`;
    control.setAttribute("aria-labelledby", name.id);
    wrapper.append(name, control);
    if (hint) {
      const description = element("span", hint, "field-hint");
      description.id = `${name.id}-hint`;
      control.setAttribute("aria-describedby", description.id);
      wrapper.append(description);
    }
    return wrapper;
  };
  const checkbox = (label, checked = false) => {
    const control = input("true", "checkbox");
    control.checked = checked;
    const wrapper = element("label", undefined, "permission-check");
    wrapper.append(control, document.createTextNode(label));
    return { control, wrapper };
  };
  const button = (
    label,
    action,
    className = "button button-secondary button-small",
  ) => {
    const control = element("button", label, className);
    control.type = "button";
    control.addEventListener("click", action);
    return control;
  };
  const lines = (control) =>
    control.value
      .split(/\r?\n/)
      .map((value) => value.trim())
      .filter(Boolean);
  const area = (values) => {
    const control = element("textarea");
    control.rows = 3;
    control.value = (values || []).join("\n");
    return control;
  };
  const title = (id) =>
    id
      .replace(/^legacy-/, "Previous ")
      .replace(/[-_]/g, " ")
      .replace(/\b\w/g, (letter) => letter.toUpperCase());
  const kindLabels = {
    path: "Site pages",
    collection: "Data collection",
    file: "Uploaded files",
    channel: "Realtime channel",
    action: "Business action",
    integration: "Integration endpoint",
  };
  const operations = {
    path: ["read"],
    collection: ["read", "create", "update", "delete"],
    file: ["read", "upload", "delete"],
    channel: ["read", "send"],
    action: ["run"],
    integration: ["read", "write"],
  };
  const operationLabels = {
    read: "View",
    create: "Create",
    update: "Edit",
    delete: "Delete",
    upload: "Upload / replace",
    send: "Send messages",
    run: "Run action",
    write: "Change external data",
  };

  function editorForm(manager, action, id, saveLabel, prepare) {
    const form = element("form", undefined, "permission-edit-form");
    form.dataset.permissionForm = "";
    hidden(form, "editor_action", action);
    hidden(form, "base", manager.dataset.policy);
    hidden(form, "id", id || "");
    const entry = hidden(form, "entry");
    const error = element("p", "", "banner banner-error");
    error.hidden = true;
    error.setAttribute("role", "alert");
    error.tabIndex = -1;
    const footer = element("div", undefined, "form-actions");
    const save = element("button", saveLabel, "button");
    save.type = "submit";
    footer.append(save);
    const finish = () => form.append(error, footer);
    form.addEventListener(
      "submit",
      (event) => {
        try {
          const value = prepare?.(form);
          if (value !== undefined) entry.value = JSON.stringify(value);
          error.hidden = true;
          event.preventDefault();
          manager.permissionStage(action, form);
        } catch (failure) {
          event.preventDefault();
          event.stopImmediatePropagation();
          error.textContent = failure.message;
          error.hidden = false;
          error.focus();
        }
      },
      true,
    );
    return { form, finish };
  }

  function disclosure(label, content, className = "permission-card") {
    const details = element("details", undefined, className);
    details.append(element("summary", label));
    if (content) details.append(content);
    return details;
  }

  // Keyboard-accessible directory picker, scoped to its own form. No guessed
  // email-to-ID matching and no HTML interpolation of directory values.
  function setupPicker(wrapper, choose) {
    const control = wrapper.querySelector("input");
    const list = wrapper.querySelector("[role=listbox]");
    const id = `permission-options-${++sequence}`;
    list.id = id;
    control.setAttribute("aria-controls", id);
    let items = [];
    let active = -1;
    let request = 0;
    let timer;
    const status = element("p", "", "field-hint");
    status.setAttribute("role", "status");
    wrapper.append(status);
    const close = () => {
      list.hidden = true;
      control.setAttribute("aria-expanded", "false");
      control.removeAttribute("aria-activedescendant");
    };
    const highlight = (index) => {
      active = index;
      items.forEach((item, position) =>
        item.node.setAttribute("aria-selected", String(index === position)),
      );
      if (items[index]) {
        control.setAttribute("aria-activedescendant", items[index].node.id);
        items[index].node.scrollIntoView({ block: "nearest" });
      }
    };
    const pick = (index) => {
      if (!items[index]) return;
      clearTimeout(timer);
      request++;
      choose(items[index].entry);
      close();
      control.value = "";
      control.focus();
    };
    const search = async () => {
      const current = ++request;
      status.textContent = "";
      try {
        const response = await fetch(
          `/api/hex/directory?limit=20&query=${encodeURIComponent(control.value.trim())}`,
          {
            headers: { "X-Hex-Request": "1" },
            credentials: "same-origin",
            redirect: "error",
          },
        );
        if (!response.ok)
          throw new Error(
            "Could not load people. Check your sign-in and try again.",
          );
        const found = await response.json();
        if (current !== request || document.activeElement !== control) return;
        items = [];
        list.replaceChildren();
        const entries = [
          ...(found.groups || []).map((group) => ({
            principal: `group:${group.id}`,
            name: group.name,
            detail: "Directory group",
            kind: "group",
          })),
          ...(found.people || []).map((person) => ({
            principal: `user:${person.id}`,
            name: person.name || person.email || person.id,
            detail: person.email || "Person",
            kind: "user",
          })),
        ];
        for (const entry of entries) {
          const option = element("li", undefined, "combobox-option");
          option.id = `${id}-${items.length}`;
          option.setAttribute("role", "option");
          const main = element("span", undefined, "option-main");
          main.append(
            element("strong", entry.name),
            element("span", entry.detail),
          );
          option.append(main);
          const index = items.length;
          option.addEventListener("mousedown", (event) => {
            event.preventDefault();
            pick(index);
          });
          items.push({ node: option, entry });
          list.append(option);
        }
        if (!items.length)
          list.append(
            element(
              "li",
              "No match. Ask the person to sign in first, or choose a known directory group.",
              "combobox-empty",
            ),
          );
        list.hidden = false;
        control.setAttribute("aria-expanded", "true");
        highlight(items.length ? 0 : -1);
      } catch (error) {
        if (current === request) status.textContent = error.message;
      }
    };
    control.addEventListener("input", () => {
      clearTimeout(timer);
      request++;
      items = [];
      list.replaceChildren();
      close();
      timer = setTimeout(search, 150);
    });
    control.addEventListener("focus", search);
    control.addEventListener("blur", () => setTimeout(close, 100));
    control.addEventListener("keydown", (event) => {
      if (event.key === "ArrowDown" || event.key === "ArrowUp") {
        event.preventDefault();
        if (list.hidden) search();
        else
          highlight(
            Math.max(
              0,
              Math.min(
                items.length - 1,
                active + (event.key === "ArrowDown" ? 1 : -1),
              ),
            ),
          );
      } else if (event.key === "Enter") {
        event.preventDefault();
        if (!list.hidden) pick(active);
      } else if (event.key === "Escape") {
        close();
      }
    });
  }

  function picker(label, choose) {
    const wrapper = element("div", undefined, "permission-picker");
    const control = input("", "search");
    control.placeholder = "Search people or directory groups";
    control.autocomplete = "off";
    control.setAttribute("role", "combobox");
    control.setAttribute("aria-autocomplete", "list");
    control.setAttribute("aria-expanded", "false");
    const list = element("ul", undefined, "combobox-options");
    list.setAttribute("role", "listbox");
    list.setAttribute("aria-label", "People and groups");
    list.hidden = true;
    wrapper.append(field(label, control), list);
    setupPicker(wrapper, choose);
    return wrapper;
  }

  function principalList(initial, labels, label) {
    const wrapper = element("div", undefined, "permission-principals");
    const values = new Set(initial || []);
    const list = element("ul", undefined, "permission-members");
    const render = () => {
      list.replaceChildren();
      for (const principal of values) {
        const row = element("li");
        row.append(element("span", labels.get(principal)?.name || principal));
        row.append(
          button(
            "Remove",
            () => {
              values.delete(principal);
              render();
              wrapper.dispatchEvent(new Event("change", { bubbles: true }));
            },
            "link-button",
          ),
        );
        list.append(row);
      }
    };
    wrapper.append(
      picker(label, (entry) => {
        labels.set(entry.principal, entry);
        values.add(entry.principal);
        render();
        wrapper.dispatchEvent(new Event("change", { bubbles: true }));
      }),
      list,
    );
    render();
    return { wrapper, value: () => [...values] };
  }

  function audienceText(subjects, labels, roles = {}) {
    return (
      [
        ...(subjects.signedIn ? ["Everyone signed in"] : []),
        ...(subjects.owners
          ? ["Site managers and platform administrators"]
          : []),
        ...(subjects.roles || []).map(
          (id) => `Group: ${roles[id]?.name || title(id)}`,
        ),
        ...(subjects.principals || []).map(
          (principal) => labels.get(principal)?.name || principal,
        ),
      ].join(" OR ") || "Nobody (no audience selected)"
    );
  }

  function hiddenValue(form, name, value) {
    const existing = form.querySelector(`input[name="${name}"]`);
    if (existing) existing.value = value;
    else hidden(form, name, value);
  }

  // Structured values support the full condition value shape. Exact existing
  // conditions remain untouched on the server unless this editor is changed.
  function valueEditor(original, changed, depth = 0) {
    if (depth > 8)
      throw new Error(
        "Condition values are too deeply nested for the visual editor. They are preserved; use technical tools to change this value.",
      );
    const wrapper = element("div", undefined, "permission-value");
    const originalType =
      original === "$actor.id"
        ? "actor"
        : original === null
          ? "null"
          : Array.isArray(original)
            ? "list"
            : typeof original === "object"
              ? "object"
              : typeof original === "number"
                ? "number"
                : typeof original === "boolean"
                  ? "boolean"
                  : "text";
    const type = select(
      [
        ["text", "Text"],
        ["number", "Number"],
        ["boolean", "True / false"],
        ["null", "Empty (null)"],
        ["actor", "Current person's ID"],
        ["list", "List of values"],
        ["object", "Structured value"],
      ],
      originalType,
    );
    type.setAttribute("aria-label", "Value type");
    if (depth >= 8) {
      for (const option of type.options)
        if (["list", "object"].includes(option.value)) option.disabled = true;
    }
    const content = element("div", undefined, "permission-value-content");
    let read;
    const render = (value) => {
      content.replaceChildren();
      if (type.value === "actor" || type.value === "null") {
        content.append(
          element(
            "span",
            type.value === "actor"
              ? "The server substitutes the signed-in person's stable ID."
              : "Matches an explicit null value, not a missing field.",
            "field-hint",
          ),
        );
        read = () => (type.value === "actor" ? "$actor.id" : null);
      } else if (type.value === "boolean") {
        const control = select(
          [
            ["true", "True"],
            ["false", "False"],
          ],
          String(value === true),
        );
        control.setAttribute("aria-label", "Comparison value");
        content.append(control);
        read = () => control.value === "true";
      } else if (type.value === "list" || type.value === "object") {
        const object = type.value === "object";
        const entries = element("div", undefined, "permission-value-entries");
        const add = (key, item) => {
          const row = element("div", undefined, "permission-value-row");
          const name = input(key || "");
          name.placeholder = "Property name";
          name.setAttribute("aria-label", "Property name");
          const editor = valueEditor(item, changed, depth + 1);
          if (object) row.append(name);
          row.append(
            editor.wrapper,
            button(
              "Remove value",
              () => {
                row.remove();
                changed();
              },
              "link-button",
            ),
          );
          row.read = () => [name.value, editor.value()];
          entries.append(row);
        };
        for (const [key, item] of object
          ? Object.entries(
              value && typeof value === "object" && !Array.isArray(value)
                ? value
                : {},
            )
          : (Array.isArray(value) ? value : []).map((item, index) => [
              String(index),
              item,
            ]))
          add(key, item);
        content.append(
          entries,
          button(object ? "Add property" : "Add value", () => {
            add("", "");
            changed();
          }),
        );
        read = () => {
          const values = [...entries.children].map((row) => row.read());
          if (!object) return values.map(([, item]) => item);
          if (
            values.some(([key]) => !key) ||
            new Set(values.map(([key]) => key)).size !== values.length
          )
            throw new Error(
              "Structured comparison values need unique, non-empty property names.",
            );
          return Object.fromEntries(values);
        };
      } else {
        const control = input(
          value === undefined || value === null || typeof value === "object"
            ? ""
            : String(value),
        );
        control.setAttribute("aria-label", "Comparison value");
        if (type.value === "number") control.inputMode = "decimal";
        content.append(control);
        let edited = false;
        control.addEventListener("input", () => {
          edited = true;
        });
        read = () => {
          if (type.value !== "number") return control.value;
          const number = Number(control.value);
          if (
            !control.value.trim() ||
            !Number.isFinite(number) ||
            (Number.isInteger(number) && !Number.isSafeInteger(number))
          )
            throw new Error(
              "Use a finite, safely representable number. Existing exact large numbers are preserved unless their conditions are edited; use technical tools for those values.",
            );
          if (
            !edited &&
            typeof value === "number" &&
            !Number.isSafeInteger(value) &&
            Number.isInteger(value)
          )
            throw new Error(
              "This rule contains an exact large number. Leave its conditions unchanged or use technical tools to edit it.",
            );
          return number;
        };
      }
    };
    type.addEventListener("change", () => {
      changed();
      render(undefined);
    });
    wrapper.addEventListener("input", changed);
    wrapper.addEventListener("change", changed);
    wrapper.append(type, content);
    render(original);
    return { wrapper, value: () => read() };
  }

  function conditionEditor(label, original) {
    let dirty = false;
    const changed = () => {
      dirty = true;
      wrapper.dispatchEvent(
        new Event("permissionDraftChanged", { bubbles: true }),
      );
    };
    const wrapper = element("fieldset", undefined, "permission-condition");
    wrapper.append(element("legend", label));
    const content = element("div");
    let root;
    const build = (
      condition = { field: "createdBy", op: "eq", value: "$actor.id" },
      depth = 0,
    ) => {
      if (depth > 8)
        throw new Error(
          "This condition is too deeply nested for the visual editor.",
        );
      const box = element("div", undefined, "permission-condition-node");
      const group = Boolean(condition.all || condition.any);
      let value;
      if (group) {
        const mode = select(
          [
            ["all", "All of these must match"],
            ["any", "At least one must match"],
          ],
          condition.all ? "all" : "any",
        );
        mode.setAttribute("aria-label", "Condition group matching");
        const children = element(
          "div",
          undefined,
          "permission-condition-children",
        );
        const add = (next) => {
          const child = build(next, depth + 1);
          child.box.append(
            button(
              "Remove condition",
              () => {
                child.box.remove();
                changed();
              },
              "link-button",
            ),
          );
          child.box.read = child.value;
          children.append(child.box);
        };
        (condition.all || condition.any).forEach(add);
        const addCondition = button("Add condition", () => {
          add();
          changed();
        });
        addCondition.disabled = depth >= 7;
        const addGroup = button("Add nested group", () => {
          add({ all: [{ field: "data.status", op: "eq", value: "draft" }] });
          changed();
        });
        addGroup.disabled = depth >= 6;
        box.append(mode, children, addCondition, addGroup);
        value = () => {
          if (!children.children.length)
            throw new Error(
              "Each condition group needs at least one condition, or remove the whole condition.",
            );
          return {
            [mode.value]: [...children.children].map((child) => child.read()),
          };
        };
      } else {
        const path = input(condition.field);
        path.placeholder = "data.projectId or createdBy";
        const op = select(
          [
            ["eq", "Equals"],
            ["ne", "Does not equal"],
            ["in", "Is one of (a list)"],
            ["contains", "Array contains"],
          ],
          condition.op,
        );
        const comparison = valueEditor(condition.value, changed);
        box.append(
          field("Record field", path),
          field("Comparison", op),
          comparison.wrapper,
        );
        value = () => ({
          field: path.value.trim(),
          op: op.value,
          value: comparison.value(),
        });
      }
      box.addEventListener("input", changed);
      box.addEventListener("change", changed);
      return { box, value };
    };
    const show = (next) => {
      content.replaceChildren();
      root = next ? build(next) : null;
      if (root)
        content.append(
          root.box,
          button(
            "Remove this requirement",
            () => {
              show(null);
              changed();
            },
            "link-button",
          ),
        );
      else
        content.append(
          element("p", "No record requirement.", "field-hint"),
          button("Add a condition", () => {
            show({
              all: [{ field: "createdBy", op: "eq", value: "$actor.id" }],
            });
            changed();
          }),
        );
    };
    wrapper.append(content);
    try {
      show(original);
    } catch (failure) {
      content.replaceChildren(
        element(
          "p",
          `${failure.message} The existing requirement will be preserved exactly unless you explicitly replace it. Other parts of this rule can still be edited.`,
          "banner banner-warning",
        ),
        button("Replace this requirement", () => {
          if (
            !window.confirm(
              "Replace this existing requirement? Its custom structure will be removed from the draft.",
            )
          )
            return;
          show(null);
          changed();
        }),
      );
    }
    return {
      wrapper,
      changed: () => dirty,
      value: () => (dirty ? root?.value() : original),
    };
  }

  function grantEditor(manager, policy, labels, id, grant = {}, groupID = "") {
    const kind = select(Object.entries(kindLabels), grant.kind || "collection");
    const resource = input(grant.resource || "");
    resource.required = true;
    const operationBox = element(
      "fieldset",
      undefined,
      "permission-operations",
    );
    operationBox.append(element("legend", "Allowed operations"));
    const operationControls = element(
      "div",
      undefined,
      "permission-check-grid",
    );
    operationBox.append(operationControls);
    const renderOperations = () => {
      operationControls.replaceChildren();
      for (const operation of operations[kind.value]) {
        const option = checkbox(
          operationLabels[operation],
          (grant.operations || []).includes(operation),
        );
        option.control.dataset.operation = operation;
        operationControls.append(option.wrapper);
      }
    };
    renderOperations();
    const who = element("fieldset", undefined, "permission-audience");
    who.append(element("legend", "Who receives this permission?"));
    const all = checkbox(
      "Everyone admitted to this site",
      grant.subjects?.signedIn,
    );
    const owners = checkbox(
      "Site managers and platform administrators",
      grant.subjects?.owners,
    );
    who.append(all.wrapper, owners.wrapper);
    const groups = [];
    for (const role of Object.keys(policy.roles || {}).sort()) {
      const option = checkbox(
        policy.roles[role].name || title(role),
        (grant.subjects?.roles || [groupID]).includes(role),
      );
      groups.push([role, option.control]);
      who.append(option.wrapper);
    }
    const people = principalList(
      grant.subjects?.principals,
      labels,
      "Also allow specific people or directory groups",
    );
    who.append(people.wrapper);
    if (groupID) who.hidden = true;
    const except = area(grant.except);
    const scopeKind = input(grant.scope?.kind || "");
    const scopeField = input(grant.scope?.field || "");
    const allFields = checkbox(
      "Allow changes to all record fields",
      grant.fields === null || grant.fields === undefined,
    );
    const fields = area(grant.fields);
    const where = conditionEditor(
      "Existing record must match (proposed record on create)",
      grant.where,
    );
    const check = conditionEditor(
      "Proposed record must match (existing record on delete)",
      grant.check,
    );
    const limits = element("div", undefined, "permission-record-limits");
    const scopes = element("div", undefined, "permission-form-grid");
    scopes.append(
      field(
        "Membership scope type (optional)",
        scopeKind,
        "For example project. Leave both scope fields empty for an unscoped rule.",
      ),
      field(
        "Record field containing the scope ID",
        scopeField,
        "For example data.projectId. Before and after values must match the member's scope.",
      ),
    );
    limits.append(
      disclosure("Optional membership limits", scopes, "advanced"),
      allFields.wrapper,
      field(
        "Editable fields · one per line",
        fields,
        "An empty list with unrestricted changes turned off means no fields may change. This does not restrict deletion.",
      ),
      where.wrapper,
      check.wrapper,
    );
    const updateKind = () => {
      const record = ["collection", "action"].includes(kind.value);
      limits.hidden = !record;
      resource.placeholder = {
        path: "/reports/ or *",
        collection: "expenses or *",
        file: "exports/ or *",
        channel: "updates or *",
        action: "approve-request or *",
        integration: "crm.deals or crm.*",
      }[kind.value];
    };
    kind.addEventListener("change", () => {
      renderOperations();
      updateKind();
    });
    updateKind();
    allFields.control.addEventListener("change", () => {
      fields.disabled = allFields.control.checked;
    });
    fields.disabled = allFields.control.checked;
    const { form, finish } = editorForm(
      manager,
      "grant",
      id,
      id
        ? "Apply rule changes"
        : groupID
          ? "Add rule to group"
          : "Add resource rule",
      (form) => {
        const selectedOperations = [
          ...operationControls.querySelectorAll("input:checked"),
        ].map((control) => control.dataset.operation);
        if (!selectedOperations.length)
          throw new Error("Select at least one allowed operation.");
        const subjects = {
          signedIn: all.control.checked,
          owners: owners.control.checked,
          roles: groups
            .filter(([, control]) => control.checked)
            .map(([role]) => role),
          principals: people.value(),
        };
        if (
          !subjects.signedIn &&
          !subjects.owners &&
          !subjects.roles.length &&
          !subjects.principals.length
        )
          throw new Error("Choose who receives this permission.");
        const record = ["collection", "action"].includes(kind.value);
        if (
          !record &&
          (grant.scope ||
            grant.where ||
            grant.check ||
            (grant.fields !== undefined && grant.fields !== null))
        )
          throw new Error(
            "This rule has record limits. Remove them explicitly before changing to a resource type without record limits, or create a new rule.",
          );
        if (
          Boolean(scopeKind.value.trim()) !== Boolean(scopeField.value.trim())
        )
          throw new Error("Set both scope fields, or leave both empty.");
        if (
          record &&
          scopeKind.value.trim() &&
          (subjects.signedIn ||
            subjects.owners ||
            subjects.principals.length ||
            !subjects.roles.length)
        )
          throw new Error(
            "Scoped rules must select only permission groups, not everyone, managers, or direct people. Assign scoped memberships to those groups instead.",
          );
        hiddenValue(form, "keep_where", String(!where.changed()));
        hiddenValue(form, "keep_check", String(!check.changed()));
        return {
          kind: kind.value,
          resource: resource.value.trim(),
          operations: selectedOperations,
          subjects,
          except: lines(except),
          ...(record
            ? {
                ...(scopeKind.value.trim()
                  ? {
                      scope: {
                        kind: scopeKind.value.trim(),
                        field: scopeField.value.trim(),
                      },
                    }
                  : {}),
                fields: allFields.control.checked ? null : lines(fields),
                where: where.value() || null,
                check: check.value() || null,
              }
            : {}),
        };
      },
    );
    const selectors = element("div", undefined, "permission-form-grid");
    selectors.append(
      field("Resource type", kind),
      field(
        "Resource name or prefix",
        resource,
        "Choose an existing policy resource or enter the identifier supplied by the app. This does not create the resource. * means all matching resources.",
      ),
    );
    const suggestions = element("datalist");
    suggestions.id = `permission-resources-${++sequence}`;
    resource.setAttribute("list", suggestions.id);
    const updateSuggestions = () => {
      suggestions.replaceChildren();
      for (const name of new Set(
        Object.values(policy.grants || {})
          .filter((rule) => rule.kind === kind.value)
          .map((rule) => rule.resource),
      )) {
        const option = element("option");
        option.value = name;
        suggestions.append(option);
      }
    };
    kind.addEventListener("change", updateSuggestions);
    updateSuggestions();
    form.append(suggestions);
    form.append(
      selectors,
      operationBox,
      who,
      disclosure(
        "Excluded resources (optional)",
        field(
          "Exclude resources · one name or prefix per line",
          except,
          "Exclusions apply only to this rule, not to other rules.",
        ),
        "advanced",
      ),
      limits,
      element(
        "p",
        "Grant only the operations needed. Approval actions and integrations can have additional server-side contracts and platform approvals; this rule does not bypass them.",
        "field-hint",
      ),
    );
    finish();
    return form;
  }

  function conditionText(condition) {
    if (!condition) return "No record requirement";
    if (condition.all || condition.any)
      return `(${(condition.all || condition.any).map(conditionText).join(condition.all ? " AND " : " OR ")})`;
    const value =
      condition.value === "$actor.id"
        ? "the current person's ID"
        : JSON.stringify(condition.value);
    return `${condition.field} ${{ eq: "equals", ne: "does not equal", in: "is one of", contains: "contains" }[condition.op]} ${value}`;
  }

  function limitsText(grant) {
    const limits = [];
    if (grant.scope)
      limits.push(
        `Membership scope ${grant.scope.kind} must match ${grant.scope.field} before and after changes`,
      );
    if (grant.where)
      limits.push(
        `Existing record (new record on create): ${conditionText(grant.where)}`,
      );
    if (grant.check)
      limits.push(
        `Proposed record (old record on delete): ${conditionText(grant.check)}`,
      );
    if (Array.isArray(grant.fields))
      limits.push(
        grant.fields.length
          ? `Only these fields may change: ${grant.fields.join(", ")}`
          : "No record fields may change (deletion is separate)",
      );
    if (grant.except?.length)
      limits.push(`Excluding: ${grant.except.join(", ")}`);
    return limits;
  }

  window.HexPermissionEditor = {
    element,
    input,
    hidden,
    select,
    field,
    checkbox,
    button,
    title,
    disclosure,
    picker,
    principalList,
    grantEditor,
    conditionText,
    limitsText,
    audienceText,
    kindLabels,
    operationLabels,
  };
})();

(() => {
  function element(tag, text, className) {
    const node = document.createElement(tag);
    if (text !== undefined) node.textContent = text;
    if (className) node.className = className;
    return node;
  }
  function button(text, click, className = "button button-secondary") {
    const node = element("button", text, className);
    node.type = "button";
    node.addEventListener("click", click);
    return node;
  }
  function field(label, control) {
    const wrapper = element("label", undefined, "field");
    wrapper.append(element("span", label, "field-label"), control);
    control.setAttribute("aria-label", label);
    return wrapper;
  }
  function choice(label, checked) {
    const control = element("input");
    control.type = "checkbox";
    control.checked = checked;
    const wrapper = element("label", undefined, "permission-check");
    wrapper.append(control, document.createTextNode(label));
    return { control, wrapper };
  }
  async function request(url, init) {
    const response = await fetch(url, { credentials: "same-origin", ...init });
    const value = await response.json();
    if (!response.ok)
      throw new Error(value.error || "Could not load action permissions");
    return value;
  }
  for (const catalog of document.querySelectorAll("[data-action-catalog]")) {
    const site = catalog.dataset.site;
    const owner = catalog.dataset.owner === "true";
    const dialog = catalog.querySelector("[data-action-dialog]");
    const body = catalog.querySelector("[data-action-body]");
    let trigger;
    let generation = 0;
    catalog
      .querySelector("[data-action-close]")
      .addEventListener("click", () => dialog.close());
    dialog.addEventListener("close", () => {
      generation++;
      trigger?.focus();
    });
    catalog
      .querySelector("[data-action-search]")
      .addEventListener("input", (event) => {
        const query = event.target.value.trim().toLowerCase();
        let count = 0;
        for (const card of catalog.querySelectorAll("[data-action-card]")) {
          card.hidden = !card.textContent.toLowerCase().includes(query);
          if (!card.hidden) count++;
        }
        catalog.querySelector("[data-action-empty]").hidden = count !== 0;
      });
    for (const control of catalog.querySelectorAll("[data-action-layout]"))
      control.addEventListener("click", () => {
        catalog.dataset.layout = control.dataset.actionLayout;
        for (const other of catalog.querySelectorAll("[data-action-layout]"))
          other.setAttribute("aria-pressed", String(other === control));
      });
    async function open(card, source) {
      trigger = source;
      const current = ++generation;
      const name = card.dataset.name;
      const isEvent = card.dataset.kind === "event";
      catalog.querySelector("#action-access-title").textContent = name;
      catalog.querySelector("[data-action-description]").textContent =
        card.querySelector(".integration-card-description").textContent;
      body.replaceChildren();
      body.append(
        element("h3", isEvent ? "Event publication" : "People & agents"),
      );
      body.append(
        element("p", card.querySelector(".integration-status").textContent),
      );
      if (owner) {
        const link = element("a", "Manage permission groups");
        link.href = `/manage/${encodeURIComponent(site)}?tab=sharing`;
        body.append(link);
      }
      dialog.showModal();
      if (card.dataset.exported !== "true") {
        body.append(
          element(
            "p",
            "This action is not exported to other apps.",
            "field-hint",
          ),
        );
        return;
      }
      if (!owner) {
        body.append(
          element(
            "p",
            isEvent
              ? "Ask the app’s owners for subscription access for your app."
              : "Ask the app’s owners about access for another app. Interactive calls use your permissions; automation calls require a site-mode grant.",
            "field-hint",
          ),
        );
        return;
      }
      const loading = element("p", "Loading connection permissions…", "muted");
      body.append(loading);
      const url = `/api/hex/manage/sites/${encodeURIComponent(site)}/${isEvent ? "events" : "actions"}/${encodeURIComponent(name)}/access`;
      try {
        const data = await request(url);
        if (current !== generation) return;
        loading.remove();
        renderConnections(data, url);
      } catch (error) {
        if (current === generation) {
          loading.textContent = error.message;
          loading.setAttribute("role", "alert");
        }
      }
    }
    function renderConnections(data, url) {
      const form = element("form", undefined, "permission-edit-form");
      const status = element("p", "", "field-hint");
      status.setAttribute("role", "status");
      const original = new Map(
        data.connections.map((entry) => [entry.source, entry]),
      );
      const entries = new Map(
        data.connections
          .filter(
            (entry) =>
              entry.source !== "*" &&
              (entry.user || entry.site || entry.subscribe),
          )
          .map((entry) => [entry.source, entry]),
      );
      const workspace = data.connections.find(
        (entry) => entry.source === "*",
      ) || {
        source: "*",
        user: !data.event,
        site: false,
        subscribe: Boolean(data.event),
      };
      const mode = element("select");
      for (const [value, label] of [
        ["none", "No other apps"],
        ["selected", "Selected apps"],
        ["all", "All workspace apps"],
      ]) {
        const option = element("option", label);
        option.value = value;
        mode.append(option);
      }
      mode.value =
        original.get("*")?.user ||
        original.get("*")?.site ||
        original.get("*")?.subscribe
          ? "all"
          : entries.size
            ? "selected"
            : "none";
      form.append(element("h3", "Other apps"), field("Available to", mode));
      if (data.custom) {
        const warning = element(
          "p",
          "Existing or custom connection grants also apply. These controls preserve them; review their permission groups before restricting access.",
          "banner banner-warning",
        );
        const link = element("a", " Review in Sharing");
        link.href = `/manage/${encodeURIComponent(site)}?tab=sharing`;
        warning.append(link);
        form.append(warning);
      }
      const picker = element("div", undefined, "permission-picker");
      const search = element("input");
      search.type = "search";
      search.placeholder = "Search apps";
      search.autocomplete = "off";
      const results = element("div");
      results.setAttribute("aria-live", "polite");
      picker.append(field("Add an app", search), results);
      form.append(picker);
      const rows = element("div");
      form.append(rows);
      const controls = new Map();
      const drafts = new Map();
      const render = () => {
        rows.replaceChildren();
        controls.clear();
        picker.hidden = mode.value !== "selected";
        const displayed =
          mode.value === "all"
            ? [workspace]
            : mode.value === "selected"
              ? [...entries.values()]
              : [];
        for (const entry of displayed) {
          const section = element("section", undefined, "permission-card");
          section.append(
            element(
              "h3",
              entry.source === "*" ? "All workspace apps" : entry.source,
            ),
          );
          if (data.event) {
            const subscription = choice("Allow subscriptions", entry.subscribe);
            subscription.control.addEventListener("change", () => {
              entry.subscribe = subscription.control.checked;
            });
            section.append(subscription.wrapper);
            controls.set(entry.source, () => ({
              source: entry.source,
              subscribe: subscription.control.checked,
            }));
            if (entry.source !== "*")
              section.append(
                button(
                  "Remove app",
                  () => {
                    entries.delete(entry.source);
                    render();
                  },
                  "link-button",
                ),
              );
            rows.append(section);
            continue;
          }
          const user = choice("On behalf of a user", entry.user);
          const automation = choice(
            "As the calling site · enables automations",
            entry.site,
          );
          automation.control.disabled = !data.siteMode;
          section.append(user.wrapper, automation.wrapper);
          const limits = element("details");
          limits.append(element("summary", "Site-mode data restrictions"));
          const hint = element(
            "p",
            entry.where || entry.check || entry.fields
              ? "Restricted records or fields. Existing limits are retained unless edited."
              : "All records eligible for this action. Its operations and declared effects still apply.",
            "field-hint",
          );
          limits.append(hint);
          const where = element("textarea");
          const draft = drafts.get(entry.source) || { changed: new Set() };
          where.rows = 4;
          where.value =
            draft.where ??
            (entry.where ? JSON.stringify(entry.where, null, 2) : "");
          const check = element("textarea");
          check.rows = 4;
          check.value =
            draft.check ??
            (entry.check ? JSON.stringify(entry.check, null, 2) : "");
          const fields = element("input");
          fields.value = draft.fields ?? (entry.fields?.join(", ") || "");
          const allFields = choice(
            "Allow all fields permitted by the action",
            draft.allFields ?? entry.fields == null,
          );
          fields.disabled = allFields.control.checked;
          limits.append(
            field("Existing record condition (JSON)", where),
            field("Resulting record condition (JSON)", check),
            field(
              "Writable fields (comma-separated; empty list allows no field changes)",
              fields,
            ),
          );
          limits.append(allFields.wrapper);
          for (const [name, control] of [
            ["where", where],
            ["check", check],
            ["fields", fields],
          ])
            control.addEventListener("input", () => {
              draft.changed.add(name);
              draft[name] = control.value;
              drafts.set(entry.source, draft);
            });
          allFields.control.addEventListener("change", () => {
            fields.disabled = allFields.control.checked;
            draft.allFields = allFields.control.checked;
            draft.changed.add("fields");
            drafts.set(entry.source, draft);
          });
          limits.hidden = !entry.site;
          automation.control.addEventListener("change", () => {
            entry.site = automation.control.checked;
            limits.hidden = !entry.site;
          });
          user.control.addEventListener("change", () => {
            entry.user = user.control.checked;
          });
          section.append(limits);
          if (!data.siteMode)
            section.append(
              element(
                "p",
                "This platform handler supports user mode. Site-mode data authority requires a declared action.",
                "field-hint",
              ),
            );
          controls.set(entry.source, () => ({
            source: entry.source,
            user: user.control.checked,
            site: automation.control.checked,
            keepWhere: !draft.changed.has("where"),
            keepCheck: !draft.changed.has("check"),
            keepFields: !draft.changed.has("fields"),
            ...(draft.changed.has("where")
              ? { where: where.value.trim() ? JSON.parse(where.value) : null }
              : {}),
            ...(draft.changed.has("check")
              ? { check: check.value.trim() ? JSON.parse(check.value) : null }
              : {}),
            ...(draft.changed.has("fields") && !allFields.control.checked
              ? {
                  fields: fields.value
                    .split(",")
                    .map((value) => value.trim())
                    .filter(Boolean),
                }
              : {}),
          }));
          if (entry.source !== "*")
            section.append(
              button(
                "Remove app",
                () => {
                  entries.delete(entry.source);
                  render();
                },
                "link-button",
              ),
            );
          rows.append(section);
        }
      };
      search.addEventListener("input", () => {
        results.replaceChildren();
        const query = search.value.trim().toLowerCase();
        if (!query) return;
        const matches = data.sources.filter(
          (source) =>
            source !== "*" &&
            !entries.has(source) &&
            source.toLowerCase().includes(query),
        );
        for (const source of matches.slice(0, 30))
          results.append(
            button(
              source,
              () => {
                entries.set(
                  source,
                  original.get(source) || {
                    source,
                    user: !data.event,
                    site: false,
                    subscribe: Boolean(data.event),
                  },
                );
                search.value = "";
                results.replaceChildren();
                render();
                search.focus();
              },
              "combobox-option",
            ),
          );
        if (!matches.length)
          results.append(element("p", "No matching apps", "field-hint"));
      });
      mode.addEventListener("change", render);
      render();
      form.append(
        element(
          "p",
          data.event
            ? "Subscription access applies to all versions of this event. Subscribers pin a version and choose a receiving automation; publication permissions are separate."
            : "User mode uses the person’s permission groups. Site mode uses this app grant. The calling app must also allow the outgoing action.",
          "field-hint",
        ),
      );
      const actions = element("div", undefined, "form-actions");
      const submit = element("button", "Save access", "button");
      submit.type = "submit";
      actions.append(
        button("Cancel", () => dialog.close()),
        submit,
      );
      form.append(status, actions);
      form.addEventListener("submit", async (event) => {
        event.preventDefault();
        submit.disabled = true;
        status.textContent = "Saving…";
        try {
          const changes = new Map(
            [...original.keys()].map((source) => [
              source,
              { source, user: false, site: false, keepLimits: true },
            ]),
          );
          for (const [source, read] of controls) changes.set(source, read());
          await request(url, {
            method: "PUT",
            headers: {
              "Content-Type": "application/json",
              "X-Hex-Request": "1",
            },
            body: JSON.stringify({
              base: data.base,
              connections: [...changes.values()],
            }),
          });
          // Refresh the baseline before allowing another edit.
          const updated = await request(url);
          form.remove();
          renderConnections(updated, url);
          const message = element(
            "p",
            data.event ? "Subscription access saved." : "Action access saved.",
            "banner banner-success",
          );
          message.setAttribute("role", "status");
          body.prepend(message);
        } catch (error) {
          status.textContent = error.message;
          status.setAttribute("role", "alert");
          submit.disabled = false;
        }
      });
      body.append(form);
    }
    catalog.addEventListener("click", (event) => {
      const target = event.target.closest("[data-action-open]");
      if (!target) return;
      event.preventDefault();
      open(target.closest("[data-action-card]"), target);
    });
    let name;
    try {
      name = decodeURIComponent(location.hash.slice(1));
    } catch {
      name = "";
    }
    const initial = [...catalog.querySelectorAll("[data-action-card]")].find(
      (card) =>
        (card.dataset.kind === "event" ? "event:" : "") + card.dataset.name ===
        name,
    );
    if (initial) open(initial, initial.querySelector("[data-action-open]"));
  }
})();

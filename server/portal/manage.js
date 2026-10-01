// Management pages: instant filtering of your sites, and the sharing editor
// with a searchable people and group picker. Everything is built with DOM
// methods so names and emails are never parsed as HTML.

const node = (tag, className, text) => {
  const element = document.createElement(tag);
  if (className) {
    element.className = className;
  }
  if (text !== undefined) {
    element.textContent = text;
  }
  return element;
};

const svgNamespace = "http://www.w3.org/2000/svg";
function groupIcon() {
  const svg = document.createElementNS(svgNamespace, "svg");
  svg.setAttribute("viewBox", "0 0 24 24");
  for (const definition of [
    ["circle", { cx: 9, cy: 8, r: 3.5 }],
    ["path", { d: "M2.5 20a6.5 6.5 0 0 1 13 0" }],
    ["path", { d: "M16 4.5a3.5 3.5 0 0 1 0 7M18 14a6.5 6.5 0 0 1 3.5 6" }],
  ]) {
    const shape = document.createElementNS(svgNamespace, definition[0]);
    for (const [name, value] of Object.entries(definition[1])) {
      shape.setAttribute(name, value);
    }
    svg.append(shape);
  }
  return svg;
}

function initials(name) {
  let text = name.trim();
  if (text.includes("@") && !text.includes(" ")) {
    text = text.split("@")[0].replace(/[._-]+/g, " ");
  }
  const words = text.split(/[^\p{L}\p{N}]+/u).filter(Boolean);
  if (words.length === 0) {
    return "?";
  }
  const first = [...words[0]][0];
  const last = words.length > 1 ? [...words[words.length - 1]][0] : "";
  return (first + last).toUpperCase();
}

function tone(value) {
  // FNV-1a, matching the server, so a person keeps their colour.
  let hash = 0x811c9dc5;
  for (const byte of new TextEncoder().encode(value)) {
    hash ^= byte;
    hash = Math.imul(hash, 0x01000193) >>> 0;
  }
  return `tone-${hash % 6}`;
}

function avatar(entry) {
  const box = node("span", `avatar ${tone(entry.principal)}`);
  box.setAttribute("aria-hidden", "true");
  if (entry.kind === "group") {
    box.append(groupIcon());
  } else {
    box.textContent = initials(entry.name);
  }
  return box;
}

// --- Your sites: instant filter while typing ---------------------------------

const manageSearch = document.querySelector("[data-filter-target]");
if (manageSearch) {
  const list = document.querySelector(manageSearch.dataset.filterTarget);
  const empty = document.getElementById("manage-list-empty");
  manageSearch.addEventListener("input", () => {
    const query = manageSearch.value.trim().toLowerCase();
    let shown = 0;
    for (const row of list?.children ?? []) {
      const match = row.dataset.filterText.toLowerCase().includes(query);
      row.hidden = !match;
      shown += match ? 1 : 0;
    }
    if (empty) {
      empty.hidden = shown > 0;
    }
  });
}

// --- Sharing editor ----------------------------------------------------------

function markDirty(form) {
  const notice = form.querySelector("#sharing-dirty");
  if (notice) {
    notice.hidden = false;
  }
}

function addedPrincipals(form) {
  return new Set(
    [...form.querySelectorAll('#share-people input[name="principal"]')].map(
      (input) => input.value,
    ),
  );
}

function addPerson(form, entry) {
  const people = form.querySelector("#share-people");
  const existing = [...people.children].find(
    (row) => row.dataset.principal === entry.principal,
  );
  if (existing) {
    existing.classList.remove("is-new");
    void existing.offsetWidth;
    existing.classList.add("is-new");
    existing.querySelector("select").focus();
    return;
  }

  const row = node("li", "person is-new");
  row.dataset.principal = entry.principal;
  const main = node("span", "person-main");
  main.append(node("strong", "", entry.name));
  if (entry.detail) {
    main.append(node("span", "muted", entry.detail));
  }
  const principal = node("input");
  principal.type = "hidden";
  principal.name = "principal";
  principal.value = entry.principal;

  const role = node("select", "role-select");
  role.name = "role";
  role.setAttribute("aria-label", `Role for ${entry.name}`);
  for (const [value, label] of [
    ["owner", "Owner"],
    ["editor", "Can edit"],
    ["viewer", "Can view"],
  ]) {
    const option = node("option", "", label);
    option.value = value;
    option.selected = value === "viewer";
    role.append(option);
  }

  const remove = node("button", "icon-button");
  remove.type = "button";
  remove.dataset.removePerson = "";
  remove.setAttribute("aria-label", `Remove ${entry.name}`);
  const cross = document.createElementNS(svgNamespace, "svg");
  cross.setAttribute("viewBox", "0 0 24 24");
  const path = document.createElementNS(svgNamespace, "path");
  path.setAttribute("d", "M6 6l12 12M18 6 6 18");
  cross.append(path);
  remove.append(cross);

  row.append(avatar(entry), main, principal, role, remove);
  people.append(row);
  form.querySelector("#share-people-empty").hidden = true;
  markDirty(form);
  updateGeneralWarning(form);
  window.hexToast?.(`${entry.name} can view. Save to apply.`);
}

// A WAI-ARIA combobox: type to search, arrow keys to move, Enter to add,
// Escape to close. Results come from the platform's directory of people who
// have signed in and the configured groups.
function setupCombobox(form) {
  const input = form.querySelector('[role="combobox"]');
  const listbox = form.querySelector('[role="listbox"]');
  if (!input || input.dataset.ready) {
    return;
  }
  input.dataset.ready = "true";

  let options = [];
  let active = -1;
  let request = 0;
  let timer;

  const close = () => {
    listbox.hidden = true;
    input.setAttribute("aria-expanded", "false");
    input.removeAttribute("aria-activedescendant");
    active = -1;
  };

  const highlight = (index) => {
    options.forEach((option, position) =>
      option.element.setAttribute("aria-selected", String(position === index)),
    );
    active = index;
    if (index >= 0) {
      input.setAttribute("aria-activedescendant", options[index].element.id);
      options[index].element.scrollIntoView({ block: "nearest" });
    } else {
      input.removeAttribute("aria-activedescendant");
    }
  };

  const choose = (index) => {
    const option = options[index];
    if (!option) {
      return;
    }
    // Drop pending and in-flight searches so they cannot reopen the list.
    clearTimeout(timer);
    request++;
    addPerson(form, option.entry);
    input.value = "";
    close();
    input.focus();
  };

  const render = (result, query) => {
    listbox.replaceChildren();
    options = [];
    const added = addedPrincipals(form);
    const sections = [
      [
        "Groups",
        result.groups.map((group) => ({
          principal: `group:${group.id}`,
          name: group.name,
          detail: "Group",
          kind: "group",
        })),
      ],
      [
        "People",
        result.people.map((person) => ({
          principal: `user:${person.id}`,
          name: person.name || person.email || person.id,
          detail: person.email,
          kind: "user",
        })),
      ],
    ];

    for (const [title, entries] of sections) {
      if (entries.length === 0) {
        continue;
      }
      const heading = node("li", "combobox-group", title);
      heading.setAttribute("role", "presentation");
      listbox.append(heading);
      for (const entry of entries) {
        const element = node("li", "combobox-option");
        element.id = `share-option-${options.length}`;
        element.setAttribute("role", "option");
        element.setAttribute("aria-selected", "false");
        const main = node("span", "option-main");
        main.append(node("strong", "", entry.name));
        if (entry.detail) {
          main.append(node("span", "", entry.detail));
        }
        element.append(avatar(entry), main);
        if (added.has(entry.principal)) {
          element.append(node("span", "added", "Added"));
        }
        const index = options.length;
        element.addEventListener("mousedown", (event) => {
          event.preventDefault();
          choose(index);
        });
        options.push({ element, entry });
        listbox.append(element);
      }
    }

    if (options.length === 0) {
      listbox.append(
        node(
          "li",
          "combobox-empty",
          query
            ? "No one found. Ask them to sign in to the platform first, or share with a group."
            : "No people or groups to show yet.",
        ),
      );
    }
    listbox.hidden = false;
    input.setAttribute("aria-expanded", "true");
    highlight(options.length > 0 ? 0 : -1);
  };

  const search = async () => {
    const query = input.value.trim();
    const current = ++request;
    try {
      const response = await fetch(
        `/api/hex/directory?limit=20&query=${encodeURIComponent(query)}`,
        {
          headers: { "X-Hex-Request": "1" },
          credentials: "same-origin",
        },
      );
      if (!response.ok) {
        throw new Error(String(response.status));
      }
      const result = await response.json();
      if (current === request && document.activeElement === input) {
        render(result, query);
      }
    } catch {
      if (current === request) {
        window.hexError?.(
          "People could not be loaded. Reload the page to check your sign-in.",
        );
      }
    }
  };

  input.addEventListener("input", () => {
    clearTimeout(timer);
    timer = setTimeout(search, 150);
  });
  input.addEventListener("focus", search);
  input.addEventListener("blur", () => setTimeout(close, 100));
  input.addEventListener("keydown", (event) => {
    if (event.key === "ArrowDown") {
      event.preventDefault();
      if (listbox.hidden) {
        search();
      } else {
        highlight(Math.min(active + 1, options.length - 1));
      }
    } else if (event.key === "ArrowUp") {
      event.preventDefault();
      highlight(Math.max(active - 1, 0));
    } else if (event.key === "Enter") {
      event.preventDefault();
      if (!listbox.hidden) {
        choose(active);
      }
    } else if (event.key === "Escape") {
      if (!listbox.hidden) {
        event.preventDefault();
        close();
      }
    }
  });
}

// Explain before saving when general access makes listed roles redundant:
// those entries cannot be stored and disappear on save.
function updateGeneralWarning(form) {
  const warning = form.querySelector("#general-warning");
  const general = form.querySelector("#general-access")?.value;
  if (!warning || !general) {
    return;
  }
  const roles = [...form.querySelectorAll("#share-people select")].map(
    (select) => select.value,
  );
  const platform = form.dataset.platform;
  let message = "";
  if (general === "view" && roles.includes("viewer")) {
    message = `Everyone at ${platform} can already view, so the people marked "Can view" will be removed when you save.`;
  } else if (
    general === "edit" &&
    roles.some((role) => role === "viewer" || role === "editor")
  ) {
    message = `Everyone at ${platform} can already view and edit, so only owners will stay listed when you save.`;
  }
  warning.textContent = message;
  warning.hidden = message === "";
}

function setupSharing() {
  const form = document.getElementById("sharing-form");
  if (!form) {
    return;
  }
  setupCombobox(form);
  updateGeneralWarning(form);
}

document.addEventListener("click", (event) => {
  const remove = event.target.closest("[data-remove-person]");
  if (!remove) {
    return;
  }
  const form = remove.closest("form");
  remove.closest(".person").remove();
  updateGeneralWarning(form);
  form.querySelector("#share-people-empty").hidden =
    form.querySelector("#share-people").children.length > 0;
  markDirty(form);
});

document.addEventListener("change", (event) => {
  const form = event.target.closest("#sharing-form");
  if (form && event.target.matches("select, textarea")) {
    markDirty(form);
    updateGeneralWarning(form);
  }
});

// The sharing form is replaced after saving; set up the new one.
document.addEventListener("htmx:afterSwap", setupSharing);
setupSharing();

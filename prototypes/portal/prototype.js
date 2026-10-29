// Standalone, fictional design review. No Hex APIs or real app URLs are used.
const $ = (id) => document.getElementById(id);
const concepts = {
  launchpad: {
    name: "Home · existing Hex design",
    title: "Welcome back, Jamie.",
    lead: "Open your pinned tools, find what’s shared with you, and keep track of your own.",
    notes: [
      [
        "Your pins come first",
        "Pin and unpin apps using the star on each card or row. Home shows four shortcuts, with View all for the rest. Pins are personal shortcuts, never ownership or access grants.",
      ],
      [
        "One search, one honest result set",
        "Typing replaces every home section with unified results. Shared apps, your apps, company apps and files use the same search. Scope chips refine that set.",
      ],
      [
        "Personal, without making assumptions",
        "Pins are explicitly chosen; recent items are personal, not the company’s most visited apps. This demo saves pins only in this browser. Production storage and retention need a decision.",
      ],
      [
        "The familiar Hex design",
        "The actual portal stylesheet supplies the header, typography, colors, cards, fields and buttons. This is a discovery improvement, not a new visual identity or an admin redesign.",
      ],
    ],
  },
  library: {
    name: "App library · existing Hex design",
    title: "App library",
    lead: "Find apps and files you can access, including what’s shared with you and what you own.",
    notes: [
      [
        "A natural extension of Home",
        "Browse the complete library from Home without a new sidebar or competing navigation system. Keep the current header and use existing segmented controls for scopes.",
      ],
      [
        "Shared with you is a real destination",
        "All, Shared with you, Owned by you and Company-wide are overlapping, explicit scopes. Company-wide is not mislabeled “everything you can access”.",
      ],
      [
        "A finite page with a clear total",
        "Twelve results per page. Search, scope, sorting, view and page are reflected in the URL, with working back/forward navigation. Search matches the complete fictional dataset, not only this page.",
      ],
      [
        "Keep useful shortcuts close",
        "Pin from either grid or list view, then return to Home to find your shortcuts. The Pinned scope shows the full set, including pins beyond the home preview.",
      ],
    ],
  },
  operations: {
    name: "Your sites · existing Hex design",
    title: "Everything you’ve published",
    lead: "Apps and files you own. Open one to decide who can see it, look at its data, or take it down.",
    notes: [
      [
        "Keep the existing Your sites list",
        "Use the portal’s current site rows, audience badges and Open, Copy link, Share and Analytics actions. The improvement is complete search and finite pages, not a new management table.",
      ],
      [
        "Search all your sites, not just this page",
        "Twelve rows per page, app/file filters and stable sorting. Clearing a query restores all matching owned sites, not only records already loaded in the browser.",
      ],
      [
        "Your per-app management page stays",
        "Site titles still lead to /manage/<site>, with the existing overview, tabs and permission editor. The previous tabbed modal has been removed; a review-only note explains that handoff instead of inventing a replacement page.",
      ],
      [
        "Platform administration is deferred",
        "No new admin workspace, All sites switch or role model is proposed in this revision. The existing Admin destination is left alone for the separate platform-admin discussion.",
      ],
    ],
  },
};

const seeds = [
  [
    "Project pulse",
    "A clear view of milestones, risks and team delivery.",
    "Maya Chen",
    "Operations",
    "shared",
  ],
  [
    "Team capacity",
    "Plan workloads and see who can help next week.",
    "Alex Morgan",
    "People",
    "everyone",
  ],
  [
    "Customer insights",
    "Turn customer feedback into your next improvement.",
    "Jamie Davis",
    "Sales",
    "owned",
  ],
  [
    "Expense desk",
    "Submit receipts and keep reimbursements moving.",
    "Sam Rivera",
    "Finance",
    "everyone",
  ],
  [
    "Release room",
    "Everything shipping this week, in one place.",
    "Maya Chen",
    "Engineering",
    "shared",
  ],
  [
    "Weekly briefing",
    "A short report on the decisions that matter.",
    "Taylor Brooks",
    "Operations",
    "shared",
  ],
  [
    "Sales compass",
    "Explore pipeline health and account opportunities.",
    "Alex Morgan",
    "Sales",
    "everyone",
  ],
  [
    "Onboarding guide",
    "A friendly starting point for your first weeks.",
    "Sam Rivera",
    "People",
    "everyone",
  ],
  [
    "Quality notebook",
    "Track inspections, findings and follow-up tasks.",
    "Jamie Davis",
    "Operations",
    "owned",
  ],
  [
    "Budget planner",
    "Build forecasts and compare spending with your plan.",
    "Jamie Davis",
    "Finance",
    "owned",
  ],
  [
    "Service status",
    "See service availability and current incidents.",
    "Taylor Brooks",
    "Engineering",
    "everyone",
  ],
  [
    "Brand assets",
    "The latest templates, logos and presentation files.",
    "Maya Chen",
    "Design",
    "everyone",
  ],
];
const apps = Array.from({ length: 156 }, (_, index) => {
  const seed = seeds[index % seeds.length];
  const edition = Math.floor(index / seeds.length);
  return {
    id: index,
    title:
      seed[0] +
      (edition
        ? ` · ${["North", "South", "East", "West", "Central", "Field", "Support", "Growth", "Product", "Partner", "Regional", "Enterprise"][edition - 1]}`
        : ""),
    description: seed[1],
    author: seed[2],
    team: seed[3],
    scope: seed[4],
    company: seed[4] === "everyone" || index % 9 === 2,
    kind: index % 13 === 5 ? "file" : "app",
    days: (index * 7) % 90,
    slug:
      seed[0].toLowerCase().replaceAll(" ", "-") +
      (edition ? `-${edition}` : ""),
  };
});
let pins;
try {
  pins = new Set(
    JSON.parse(localStorage.getItem("hex-portal-review-pins") || "[0,1,2,3]"),
  );
} catch {
  pins = new Set([0, 1, 2, 3]);
}
const state = {
  concept: "launchpad",
  scope: "all",
  q: "",
  sort: "relevance",
  page: 1,
  mode: "grid",
  kind: "all",
  scenario: "ready",
};
const labels = {
  all: "All accessible",
  shared: "Shared with you",
  owned: "Owned by you",
  everyone: "Company-wide",
  pinned: "Pinned",
  recent: "Recently opened",
};
const pageSize = 12;
let toastTimer;

function el(tag, className, text) {
  const result = document.createElement(tag);
  if (className) result.className = className;
  if (text !== undefined) result.textContent = text;
  return result;
}

function button(text, action, className = "button button-secondary") {
  const result = el("button", className, text);
  result.type = "button";
  result.addEventListener("click", action);
  return result;
}

function link(text, changes, className) {
  const result = el("a", className, text);
  result.href = urlFor(changes);
  result.addEventListener("click", (event) => {
    if (event.ctrlKey || event.metaKey || event.shiftKey || event.altKey)
      return;
    event.preventDefault();
    navigate(changes);
  });
  return result;
}

function urlFor(changes = {}) {
  const next = { ...state, ...changes };
  const query = new URLSearchParams();
  for (const key of ["concept", "scope", "q", "sort", "page", "mode", "kind"]) {
    query.set(key, next[key]);
  }
  return `?${query}`;
}

function navigate(changes, replace = false) {
  Object.assign(state, changes);
  history[replace ? "replaceState" : "pushState"]({}, "", urlFor());
  render();
}

function readURL() {
  const query = new URLSearchParams(location.search);
  const concept = query.get("concept") || document.body.dataset.concept;
  state.concept = concepts[concept] ? concept : "launchpad";
  const scope =
    query.get("scope") || (state.concept === "operations" ? "owned" : "all");
  state.scope = labels[scope] ? scope : "all";
  if (state.concept === "operations") state.scope = "owned";
  state.q = query.get("q") || "";
  state.sort = ["relevance", "name", "recent"].includes(query.get("sort"))
    ? query.get("sort")
    : "relevance";
  state.page = Math.max(1, Number.parseInt(query.get("page"), 10) || 1);
  state.mode = query.get("mode") === "list" ? "list" : "grid";
  state.kind = ["app", "file"].includes(query.get("kind"))
    ? query.get("kind")
    : "all";
}

function toast(text) {
  $("toast").textContent = text;
  $("toast").hidden = false;
  clearTimeout(toastTimer);
  toastTimer = setTimeout(() => {
    $("toast").hidden = true;
  }, 2600);
}

function icon(app) {
  return el(
    "span",
    `site-icon tone-${app.id % 6}`,
    app.kind === "file" ? "▤" : app.title[0],
  );
}

function pinButton(app) {
  const pinned = pins.has(app.id);
  const result = button(
    pinned ? "★" : "☆",
    () => {
      if (pins.has(app.id)) pins.delete(app.id);
      else pins.add(app.id);
      savePins();
      renderContent();
      const next = document.querySelector(`[data-pin="${app.id}"]`);
      (next || $("search")).focus();
    },
    "icon-button prototype-pin",
  );
  result.setAttribute("aria-pressed", String(pinned));
  result.setAttribute("aria-label", `Pin ${app.title}`);
  result.title = pinned ? "Remove from pinned apps" : "Pin to your home page";
  result.dataset.pin = String(app.id);
  const star = document.createElementNS("http://www.w3.org/2000/svg", "svg");
  star.setAttribute("viewBox", "0 0 24 24");
  star.setAttribute("aria-hidden", "true");
  const path = document.createElementNS("http://www.w3.org/2000/svg", "path");
  path.setAttribute(
    "d",
    "m12 3 2.8 5.7 6.2.9-4.5 4.4 1.1 6.2-5.6-3-5.6 3 1.1-6.2L3 9.6l6.2-.9z",
  );
  path.setAttribute("fill", pinned ? "currentColor" : "none");
  star.append(path);
  result.replaceChildren(star);
  return result;
}

function savePins() {
  try {
    localStorage.setItem("hex-portal-review-pins", JSON.stringify([...pins]));
  } catch {
    toast("Pins are available for this preview session only.");
  }
}

function titleButton(app, management = false) {
  return button(
    app.title,
    () => openApp(app, management),
    "prototype-title app-title site-title",
  );
}

function badge(app) {
  const text =
    app.scope === "owned"
      ? "Owned by you"
      : app.scope === "shared"
        ? "Shared with you"
        : "Company-wide";
  return el(
    "span",
    `badge ${app.scope === "shared" ? "badge-shared" : ""}`,
    text,
  );
}

function card(app) {
  const result = el("article", "site-card prototype-card card");
  const top = el("div", "site-card-top");
  top.append(icon(app), pinButton(app));
  const meta = el("div", "site-card-meta");
  meta.append(el("span", "", app.author), badge(app));
  result.append(
    top,
    el("h3"),
    el("p", "site-description", app.description),
    meta,
  );
  result.querySelector("h3").append(titleButton(app));
  return result;
}

function compact(app, recent = false) {
  const result = el("div", "site-row prototype-compact-row");
  const main = el("div", "site-row-main");
  main.append(
    titleButton(app),
    el(
      "p",
      "muted",
      recent
        ? `Opened ${app.id % 2 ? "yesterday" : "today"} · ${app.team}`
        : `${app.kind === "file" ? "File" : "App"} · ${app.author}`,
    ),
  );
  result.append(icon(app), main, pinButton(app));
  return result;
}

function section(title, subtitle, selected, scope, layout = "grid") {
  const result = el("section", "section");
  const heading = el("div", "section-heading");
  const copy = el("div");
  copy.append(el("h2", "", title));
  if (subtitle) copy.append(el("p", "muted", subtitle));
  heading.append(
    copy,
    link("View all →", { concept: "library", scope, q: "", page: 1 }),
  );
  const list = el(
    "div",
    layout === "compact-list"
      ? scope === "recent"
        ? "prototype-recent-list"
        : "site-list"
      : `card-grid ${layout}`,
  );
  for (const app of selected)
    list.append(
      layout === "compact-list"
        ? compact(app, title === "Recently opened")
        : card(app),
    );
  result.append(heading, list);
  return result;
}

function matchesScope(app, scope) {
  if (scope === "pinned") return pins.has(app.id);
  if (scope === "recent")
    return [4, 6, 8, 10, 0, 2, 3, 12, 14, 20, 21, 22, 27, 35].includes(app.id);
  if (scope === "everyone") return app.company;
  return scope === "all" || app.scope === scope;
}

function normalize(text) {
  return text
    .normalize("NFD")
    .replace(/\p{Diacritic}/gu, "")
    .toLowerCase();
}

function score(app) {
  const tokens = normalize(state.q).trim().split(/\s+/).filter(Boolean);
  const title = normalize(app.title);
  const secondary = normalize(
    `${app.description} ${app.author} ${app.team} ${app.slug}`,
  );
  if (
    !tokens.every((token) => title.includes(token) || secondary.includes(token))
  )
    return -1;
  return tokens.reduce(
    (total, token) =>
      total +
      (title === token
        ? 100
        : title.startsWith(token)
          ? 40
          : title.includes(token)
            ? 20
            : 3),
    0,
  );
}

function filtered(scope = state.scope) {
  return apps
    .filter(
      (app) =>
        matchesScope(app, scope) &&
        (state.kind === "all" || state.kind === app.kind) &&
        score(app) >= 0,
    )
    .sort((left, right) => {
      if (state.sort === "name")
        return left.title.localeCompare(right.title) || left.id - right.id;
      if (state.sort === "recent")
        return left.days - right.days || left.id - right.id;
      return score(right) - score(left) || left.id - right.id;
    });
}

function render() {
  const concept = concepts[state.concept];
  document.title = `Hex · ${concept.name} · Design preview`;
  $("concept").value = state.concept;
  $("search").value = state.q;
  $("clear-search").hidden = !state.q;
  $("heading").replaceChildren();
  const heading = el(
    "div",
    state.concept === "launchpad" ? "hero" : "page-heading",
  );
  const text = el("div");
  text.append(
    el(
      "span",
      "eyebrow",
      state.concept === "launchpad"
        ? "YOUR EVERYDAY WORKSPACE"
        : state.concept === "library"
          ? "APP LIBRARY"
          : "YOUR SITES",
    ),
  );
  text.append(el("h1", "", concept.title), el("p", "lead", concept.lead));
  heading.append(text);
  if (state.concept === "operations")
    heading.append(
      button("＋ Publish something", () =>
        showDialog(
          "Build & publish",
          "The existing installer and publishing flow stays available here. This prototype does not publish apps or change workspace data.",
        ),
      ),
    );
  $("heading").append(heading);
  $("search").placeholder =
    state.concept === "operations"
      ? "Search your sites by title or name…"
      : "Search apps by name, purpose or person…";
  $("search-hint").textContent =
    state.concept === "operations"
      ? "Search all your sites, including results beyond the current page."
      : "Search everything you can access, including shared and owned apps.";
  const nav = $("main-nav");
  nav.replaceChildren();
  for (const [name, id] of [
    ["Home", "launchpad"],
    ["Your sites", "operations"],
  ]) {
    const item = link(name, {
      concept: id,
      scope: id === "operations" ? "owned" : "all",
      q: "",
      page: 1,
      kind: "all",
    });
    item.setAttribute(
      "aria-current",
      String(
        (
          id === "launchpad"
            ? state.concept !== "operations"
            : state.concept === id
        )
          ? "page"
          : "false",
      ),
    );
    nav.append(item);
  }
  nav.append(unchangedLink("Integrations"));
  const help = el("a", "", "Build & publish");
  help.href = "#build-and-publish";
  help.addEventListener("click", (event) => {
    event.preventDefault();
    showDialog(
      "Build & publish",
      "Keep the existing CLI installer and onboarding page. This exploration focuses on finding and managing apps.",
    );
  });
  nav.append(help);
  nav.append(unchangedLink("Admin"));
  renderContent();
}

function unchangedLink(name) {
  const item = el("a", "", name);
  item.href = `#existing-${name.toLowerCase()}`;
  item.addEventListener("click", (event) => {
    event.preventDefault();
    showDialog(
      `${name} stays unchanged`,
      name === "Admin"
        ? "The existing Admin navigation is shown for continuity only. Platform-admin roles, permissions and screens are deferred to a separate discussion."
        : "This destination keeps its current design and behavior. It is outside this discovery prototype.",
    );
  });
  return item;
}

function renderContent() {
  const content = $("content");
  content.replaceChildren();
  $("clear-search").hidden = !state.q;
  if (state.scenario !== "ready") {
    renderScenario();
    return;
  }
  if (
    state.concept === "launchpad" &&
    !state.q.trim() &&
    state.scope === "all"
  ) {
    renderHome();
    return;
  }
  if (state.concept === "operations") renderOperations();
  else renderLibrary();
}

function renderHome() {
  const content = $("content");
  const pinned = apps.filter((app) => pins.has(app.id)).slice(0, 4);
  if (pinned.length)
    content.append(
      section(
        "Pinned for you",
        "Your shortcuts. Always within reach.",
        pinned,
        "pinned",
        "grid pins",
      ),
    );
  else {
    const empty = el("div", "panel prototype-explore");
    empty.append(
      el("p", "", "Make this space yours. Pin an app from the library."),
      link("Find apps →", { concept: "library", scope: "all", page: 1 }),
    );
    content.append(empty);
  }
  content.append(
    section(
      "Recently opened",
      "Get back to what you were doing.",
      [apps[4], apps[6], apps[8], apps[10]],
      "recent",
      "compact-list",
    ),
  );
  const split = el("div", "prototype-split");
  split.append(
    section(
      "Shared with you",
      "For you and your teams.",
      apps.filter((app) => app.scope === "shared").slice(0, 3),
      "shared",
      "compact-list",
    ),
  );
  split.append(
    section(
      "Your sites",
      "Apps and files you have published.",
      apps.filter((app) => app.scope === "owned").slice(0, 3),
      "owned",
      "compact-list",
    ),
  );
  content.append(split);
  const explore = el("div", "panel prototype-explore");
  const text = el("div");
  text.append(
    el("h2", "", "Browse all apps"),
    el(
      "p",
      "muted",
      "Explore 156 accessible apps and files, a page at a time.",
    ),
  );
  explore.append(
    text,
    link("Browse the library →", {
      concept: "library",
      scope: "all",
      q: "",
      page: 1,
    }),
  );
  content.append(explore);
  $("announcement").textContent =
    "Personal home. Search replaces these sections with results from all accessible apps and files.";
}

function selectControl(label, values, selected, change) {
  const wrapper = el("label", "field");
  wrapper.append(el("span", "field-label", label));
  const select = el("select");
  select.setAttribute("aria-label", label);
  for (const [value, text] of values) {
    const option = el("option", "", text);
    option.value = value;
    option.selected = value === selected;
    select.append(option);
  }
  select.addEventListener("change", () => change(select.value));
  wrapper.append(select);
  return wrapper;
}

function toolbar(management = false) {
  const result = el("div", "toolbar prototype-controls");
  const scopes = el("nav", "segmented scope-tabs");
  scopes.setAttribute(
    "aria-label",
    management ? "Filter by kind" : "Search scope",
  );
  if (management) {
    for (const [kind, label] of [
      ["all", "All types"],
      ["app", "Apps"],
      ["file", "Files"],
    ]) {
      const item = link(label, { kind, page: 1 });
      item.setAttribute("aria-current", state.kind === kind ? "page" : "false");
      scopes.append(item);
    }
  } else {
    for (const scope of [
      "all",
      "pinned",
      "recent",
      "shared",
      "owned",
      "everyone",
    ]) {
      const item = link(labels[scope], { scope, page: 1 });
      item.append(el("span", "", String(filtered(scope).length)));
      item.setAttribute(
        "aria-current",
        scope === state.scope ? "page" : "false",
      );
      scopes.append(item);
    }
  }
  result.append(
    scopes,
    selectControl(
      "Sort",
      [
        ["relevance", state.q ? "Best match" : "Default order"],
        ["name", "Name A–Z"],
        ["recent", "Recently updated"],
      ],
      state.sort,
      (sort) => navigate({ sort, page: 1 }),
    ),
  );
  return result;
}

function resultPage() {
  const all = filtered();
  const pages = Math.max(1, Math.ceil(all.length / pageSize));
  state.page = Math.min(state.page, pages);
  const start = (state.page - 1) * pageSize;
  return { all, pages, start, items: all.slice(start, start + pageSize) };
}

function summary(page, management = false) {
  const result = el("div", "prototype-summary result-summary");
  const text = el("p");
  text.append(
    el("strong", "", `${page.all.length} ${management ? "sites" : "results"}`),
  );
  text.append(
    document.createTextNode(
      state.q
        ? ` for “${state.q}” · ${labels[state.scope]}`
        : ` · ${labels[state.scope]}`,
    ),
  );
  result.append(text);
  if (!management)
    result.append(
      button(state.mode === "grid" ? "☷ List view" : "⊞ Grid view", () =>
        navigate({ mode: state.mode === "grid" ? "list" : "grid" }),
      ),
    );
  $("announcement").textContent =
    `${page.all.length} results${state.q ? ` for ${state.q}` : ""}. ${labels[state.scope]}. Page ${state.page} of ${page.pages}.`;
  return result;
}

function pagination(page) {
  const result = el("nav", "prototype-pagination");
  result.setAttribute("aria-label", "Results pagination");
  result.append(
    el(
      "span",
      "",
      `Showing ${page.start + 1}–${page.start + page.items.length} of ${page.all.length} · ${pageSize} per page`,
    ),
  );
  const actions = el("div");
  if (state.page > 1)
    actions.append(
      link(
        "← Previous",
        { page: state.page - 1 },
        "button button-secondary button-small",
      ),
    );
  actions.append(el("span", "", `Page ${state.page} of ${page.pages}`));
  if (state.page < page.pages)
    actions.append(
      link(
        "Next →",
        { page: state.page + 1 },
        "button button-secondary button-small",
      ),
    );
  result.append(actions);
  return result;
}

function renderLibrary() {
  const content = $("content");
  const page = resultPage();
  content.append(toolbar(), summary(page));
  if (!page.items.length) {
    content.append(
      emptyState(
        "No matches. Let’s widen the search.",
        "Try a different name, purpose or person, or reset the scope.",
        "Clear search & filters",
        () => navigate({ q: "", scope: "all", kind: "all", page: 1 }),
      ),
    );
    return;
  }
  const list = el(
    "div",
    state.mode === "grid" ? "card-grid grid" : "site-list",
  );
  for (const app of page.items) {
    if (state.mode === "grid") list.append(card(app));
    else {
      const row = el("article", "site-row result-row");
      const main = el("div", "site-row-main");
      main.append(titleButton(app), el("span", "muted", app.description));
      row.append(
        icon(app),
        main,
        el("span", "muted", app.author),
        badge(app),
        pinButton(app),
      );
      list.append(row);
    }
  }
  content.append(list, pagination(page));
}

function renderOperations() {
  const content = $("content");
  const preserved = el("p", "prototype-preserved");
  preserved.append(
    el("strong", "", "Your app management pages stay as they are. "),
    document.createTextNode(
      "This preview changes only how you find a site in this list. Platform administration is not being redesigned.",
    ),
  );
  const page = resultPage();
  content.append(preserved, toolbar(true), summary(page, true));
  if (!page.items.length) {
    content.append(
      emptyState(
        "Nothing matches these filters.",
        "Your apps are still here. Try removing the search or type filter.",
        "Reset filters",
        () => navigate({ q: "", kind: "all", page: 1 }),
      ),
    );
    return;
  }
  const list = el("ul", "site-list");
  list.id = "manage-list";
  for (const app of page.items) {
    const row = el("li", "site-row prototype-manage-row");
    const main = el("div", "site-row-main");
    main.append(
      titleButton(app, true),
      el(
        "span",
        "muted",
        `${app.kind === "file" ? "File" : "App"} · ${app.slug} · updated ${app.days ? `${app.days} days ago` : "today"}`,
      ),
    );
    const access = el(
      "span",
      `badge ${app.company ? "" : "badge-private"}`,
      app.company ? "Everyone" : "Only owners",
    );
    const actions = el("div", "row-actions");
    actions.append(
      button(
        "Open",
        () => openApp(app, false),
        "button button-secondary button-small",
      ),
      button(
        "Copy link",
        () =>
          showDialog(
            "Copy link · prototype only",
            "In the portal this copies the actual app URL. Fictional app links are not copied from this design preview.",
          ),
        "button button-secondary button-small",
      ),
      button(
        "Share",
        () => existingManagement(app, "sharing"),
        "button button-small",
      ),
      button(
        "Analytics",
        () => existingManagement(app, "analytics"),
        "link-button",
      ),
    );
    row.append(icon(app), main, access, actions, pinButton(app));
    list.append(row);
  }
  content.append(list, pagination(page));
}

function emptyState(title, description, action, handler) {
  const result = el("div", "empty-state prototype-empty");
  result.append(
    el("h2", "", title),
    el("p", "", description),
    button(action, handler, "button"),
  );
  return result;
}

function renderScenario() {
  const content = $("content");
  if (state.scenario === "loading") {
    content.setAttribute("aria-busy", "true");
    content.append(
      el("p", "loading-label", "Finding your tools… (paused loading preview)"),
    );
    const grid = el("div", "card-grid");
    for (let index = 0; index < 6; index++)
      grid.append(el("div", "prototype-skeleton"));
    content.append(grid, button("Finish loading preview", resetScenario));
  } else {
    content.removeAttribute("aria-busy");
    if (state.scenario === "empty")
      content.append(
        emptyState(
          "Your workspace starts here.",
          "No accessible apps yet. Publish your first app, or ask a colleague to share one.",
          "See publishing guide",
          () =>
            showDialog(
              "Your first app",
              "The production flow links to the existing Build & publish guide. This is a fictional empty-workspace preview.",
            ),
        ),
      );
    else
      content.append(
        emptyState(
          "We couldn’t load your apps.",
          "Check your connection and try again. This is an error, not an empty library.",
          "Try again",
          resetScenario,
        ),
      );
  }
}

function resetScenario() {
  state.scenario = "ready";
  $("scenario").value = "ready";
  $("content").removeAttribute("aria-busy");
  renderContent();
}

function showDialog(title, description) {
  $("dialog-content").replaceChildren(
    el("h2", "", title),
    el("p", "", description),
  );
  $("dialog-content").firstElementChild.id = "dialog-title";
  $("dialog").showModal();
}

function openApp(app, management) {
  if (management) {
    existingManagement(app);
    return;
  }
  const content = $("dialog-content");
  content.replaceChildren();
  const heading = el("div", "site-header");
  const text = el("div");
  const title = el("h2", "", app.title);
  title.id = "dialog-title";
  text.append(title, el("p", "", "App preview · no live destination"));
  heading.append(icon(app), text);
  content.append(heading);
  content.append(
    el("p", "", app.description),
    badge(app),
    el(
      "p",
      "",
      `Built by ${app.author} · ${app.team}. In the real portal, the app title opens its site. No real app is opened here.`,
    ),
    button(pins.has(app.id) ? "Remove from pins" : "Pin this app", () => {
      if (pins.has(app.id)) pins.delete(app.id);
      else pins.add(app.id);
      savePins();
      $("dialog").close();
      renderContent();
      toast("Your preview pins have been updated.");
    }),
  );
  $("dialog").showModal();
}

function existingManagement(app, tab = "overview") {
  showDialog(
    "Your existing app management page",
    `In the portal, this opens ${app.title} on its current full management page. The page layout, tabs, sharing editor and behavior are preserved—not replaced by this dialog.`,
  );
  const path = `/manage/${app.slug}${tab === "overview" ? "" : `?tab=${tab}`}`;
  $("dialog-content").append(
    el("p", "prototype-route", `Existing destination: ${path}`),
    el(
      "p",
      "",
      "This is only a handoff note for fictional data. No live app, policy or platform-admin role is changed.",
    ),
  );
}

$("review-notes").addEventListener("click", () => {
  const concept = concepts[state.concept];
  showDialog(
    concept.name,
    "A reviewable interaction prototype, not a performance implementation. All 156 records are fictional and local.",
  );
  const list = el("ol", "prototype-notes");
  for (const [title, description] of concept.notes) {
    const item = el("li");
    item.append(el("strong", "", title), el("span", "", description));
    list.append(item);
  }
  $("dialog-content").append(list);
});
$("account").addEventListener("click", () =>
  showDialog(
    "Jamie Davis · Demo account",
    "A fictional workspace member. Admin navigation is shown only to preserve the existing header; no admin redesign or role changes are part of this revision. No real identity, app metadata or company content is included.",
  ),
);
$("close-dialog").addEventListener("click", () => $("dialog").close());
$("dialog").addEventListener("click", (event) => {
  if (event.target === $("dialog")) $("dialog").close();
});
$("concept").addEventListener("change", () =>
  navigate({
    concept: $("concept").value,
    scope: $("concept").value === "operations" ? "owned" : "all",
    q: "",
    page: 1,
    kind: "all",
  }),
);
$("search").addEventListener("input", () =>
  navigate({ q: $("search").value, page: 1 }, true),
);
$("search-form").addEventListener("submit", (event) => {
  event.preventDefault();
  navigate({ q: $("search").value, page: 1 });
});
$("clear-search").addEventListener("click", () => {
  navigate({ q: "", page: 1 });
  $("search").focus();
});
$("scenario").addEventListener("change", () => {
  $("content").removeAttribute("aria-busy");
  state.scenario = $("scenario").value;
  renderContent();
});
$("theme").addEventListener("click", () => {
  const dark = document.documentElement.dataset.theme
    ? document.documentElement.dataset.theme === "dark"
    : matchMedia("(prefers-color-scheme: dark)").matches;
  document.documentElement.dataset.theme = dark ? "light" : "dark";
  toast(`${dark ? "Light" : "Dark"} theme preview`);
});
document.addEventListener("keydown", (event) => {
  if (
    event.key === "/" &&
    !["INPUT", "TEXTAREA", "SELECT"].includes(document.activeElement.tagName) &&
    !$("dialog").open
  ) {
    event.preventDefault();
    $("search").focus();
  }
  if (
    event.key === "Escape" &&
    !$("dialog").open &&
    document.activeElement === $("search")
  ) {
    navigate({ q: "", page: 1 });
    $("search").focus();
  }
});
window.addEventListener("popstate", () => {
  readURL();
  render();
});
readURL();
render();

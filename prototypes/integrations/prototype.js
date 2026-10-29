// Fictional catalog and grants. This prototype never contacts the platform.
const string = (description) => ({ type: "string", description });
const object = (properties, required = []) => ({
  type: "object",
  properties,
  required,
  additionalProperties: false,
});
const endpoint = (name, title, description, granted, write, input, output) => ({
  name,
  title,
  description,
  granted,
  write,
  input,
  output,
});
const integrations = [
  {
    id: "slack",
    title: "Slack",
    mark: "#",
    category: "Communication",
    restricted: false,
    connector: null,
    description:
      "Bring conversations, channel updates and notifications into your workflows.",
    endpoints: [
      endpoint(
        "channels",
        "List channels",
        "Discover channels your workspace can use.",
        true,
        false,
        object({ limit: { type: "integer", minimum: 1, maximum: 100 } }),
        object(
          {
            channels: {
              type: "array",
              items: object(
                { id: string("Channel ID"), name: string("Channel name") },
                ["id", "name"],
              ),
            },
          },
          ["channels"],
        ),
      ),
      endpoint(
        "users",
        "Find people",
        "Look up people in your Slack workspace.",
        true,
        false,
        object({ query: string("Name to look up") }),
        object(
          {
            users: {
              type: "array",
              items: object(
                { id: string("User ID"), name: string("Display name") },
                ["id", "name"],
              ),
            },
          },
          ["users"],
        ),
      ),
      endpoint(
        "post-message",
        "Send a message",
        "Post a message to a channel.",
        true,
        true,
        object(
          { channel: string("Channel ID"), text: string("Message text") },
          ["channel", "text"],
        ),
        object({ messageId: string("Created message ID") }, ["messageId"]),
      ),
    ],
  },
  {
    id: "github",
    title: "GitHub",
    mark: "GH",
    category: "Development",
    restricted: false,
    connector: null,
    description:
      "Track repositories, review open issues and create work for your team.",
    endpoints: [
      endpoint(
        "repositories",
        "List repositories",
        "Discover repositories available to the platform.",
        true,
        false,
        object({ organization: string("Organization name") }, ["organization"]),
        object({ repositories: { type: "array", items: { type: "string" } } }, [
          "repositories",
        ]),
      ),
      endpoint(
        "issues",
        "Read issues",
        "Read and filter issues in a repository.",
        true,
        false,
        object(
          {
            repository: string("owner/repository"),
            state: { type: "string", enum: ["open", "closed"] },
          },
          ["repository"],
        ),
        object(
          {
            issues: {
              type: "array",
              items: object(
                { number: { type: "integer" }, title: string("Issue title") },
                ["number", "title"],
              ),
            },
          },
          ["issues"],
        ),
      ),
      endpoint(
        "create-issue",
        "Create an issue",
        "Open an issue with a title and description.",
        false,
        true,
        object(
          {
            repository: string("owner/repository"),
            title: string("Issue title"),
            body: string("Issue description"),
          },
          ["repository", "title"],
        ),
        object({ url: string("Created issue URL") }, ["url"]),
      ),
    ],
  },
  {
    id: "azuredevops",
    title: "Azure DevOps",
    mark: "A",
    category: "Development",
    restricted: false,
    connector: "Microsoft",
    connected: false,
    description:
      "Explore projects and work items using your own Azure DevOps account.",
    endpoints: [
      endpoint(
        "projects",
        "List projects",
        "See the projects your linked account can access.",
        true,
        false,
        object({ organization: string("Organization name") }, ["organization"]),
        object({ projects: { type: "array", items: { type: "string" } } }, [
          "projects",
        ]),
      ),
      endpoint(
        "work-items",
        "Read work items",
        "Find bugs, tasks and stories in a project.",
        true,
        false,
        object(
          { project: string("Project name"), query: string("Work item query") },
          ["project"],
        ),
        object(
          {
            items: {
              type: "array",
              items: object(
                { id: { type: "integer" }, title: string("Work item title") },
                ["id", "title"],
              ),
            },
          },
          ["items"],
        ),
      ),
      endpoint(
        "update-work-item",
        "Update a work item",
        "Change a work item’s title or state.",
        false,
        true,
        object(
          {
            id: { type: "integer" },
            title: string("New title"),
            state: string("New state"),
          },
          ["id"],
        ),
        object({ id: { type: "integer" }, updated: { type: "boolean" } }, [
          "id",
          "updated",
        ]),
      ),
    ],
  },
  {
    id: "hubspot",
    title: "HubSpot",
    mark: "H",
    category: "Customer relationships",
    restricted: true,
    connector: null,
    audited: true,
    description:
      "Work with customer records, sales pipelines and deal activity.",
    endpoints: [
      endpoint(
        "contacts",
        "Read contacts",
        "Look up customer contact records.",
        false,
        false,
        object({ query: string("Contact search") }, ["query"]),
        object(
          {
            contacts: {
              type: "array",
              items: object(
                { id: string("Contact ID"), name: string("Contact name") },
                ["id", "name"],
              ),
            },
          },
          ["contacts"],
        ),
      ),
      endpoint(
        "deals",
        "Read deals",
        "Explore deals in a sales pipeline.",
        false,
        false,
        object({ pipeline: string("Pipeline ID") }),
        object(
          {
            deals: {
              type: "array",
              items: object(
                { id: string("Deal ID"), amount: { type: "number" } },
                ["id", "amount"],
              ),
            },
          },
          ["deals"],
        ),
      ),
      endpoint(
        "update-deal",
        "Update a deal",
        "Move a deal to a new pipeline stage.",
        false,
        true,
        object({ id: string("Deal ID"), stage: string("Target stage") }, [
          "id",
          "stage",
        ]),
        object({ updated: { type: "boolean" } }, ["updated"]),
      ),
    ],
  },
  {
    id: "sentry",
    title: "Sentry",
    mark: "S",
    category: "Monitoring",
    restricted: true,
    connector: null,
    audited: true,
    description:
      "Understand application errors, spot trends and triage issues.",
    endpoints: [
      endpoint(
        "issues",
        "Read issues",
        "List recent errors for a project.",
        true,
        false,
        object(
          { project: string("Project slug"), query: string("Issue search") },
          ["project"],
        ),
        object(
          {
            issues: {
              type: "array",
              items: object(
                { id: string("Issue ID"), title: string("Error title") },
                ["id", "title"],
              ),
            },
          },
          ["issues"],
        ),
      ),
      endpoint(
        "issue-counts",
        "Summarize error counts",
        "Get issue totals for a reporting period.",
        true,
        false,
        object(
          {
            project: string("Project slug"),
            statsPeriod: string("For example, 7d"),
          },
          ["project"],
        ),
        object({ total: { type: "integer" } }, ["total"]),
      ),
      endpoint(
        "resolve-issue",
        "Resolve an issue",
        "Mark an error as resolved in Sentry.",
        true,
        true,
        object({ id: string("Issue ID") }, ["id"]),
        object({ resolved: { type: "boolean" } }, ["resolved"]),
      ),
    ],
  },
  {
    id: "google-tasks",
    title: "Google Tasks",
    mark: "✓",
    category: "Productivity",
    restricted: false,
    connector: "Google",
    connected: true,
    description:
      "Read and manage personal tasks with your linked Google account.",
    endpoints: [
      endpoint(
        "lists",
        "List task lists",
        "Browse your personal task lists.",
        true,
        false,
        object({}),
        object(
          {
            lists: {
              type: "array",
              items: object(
                { id: string("List ID"), title: string("List title") },
                ["id", "title"],
              ),
            },
          },
          ["lists"],
        ),
      ),
      endpoint(
        "tasks",
        "Read tasks",
        "Read tasks from one of your lists.",
        true,
        false,
        object({ list: string("Task list ID") }, ["list"]),
        object(
          {
            tasks: {
              type: "array",
              items: object(
                { id: string("Task ID"), title: string("Task title") },
                ["id", "title"],
              ),
            },
          },
          ["tasks"],
        ),
      ),
      endpoint(
        "create-task",
        "Create a task",
        "Add a task to a personal task list.",
        true,
        true,
        object({ list: string("Task list ID"), title: string("Task title") }, [
          "list",
          "title",
        ]),
        object({ id: string("Created task ID") }, ["id"]),
      ),
    ],
  },
];

const catalog = document.querySelector("#catalog");
const detailDialog = document.querySelector("#detail-dialog");
const notesDialog = document.querySelector("#notes-dialog");
let filter = "all";
let layout = "grid";
let previewState = "normal";
let toastTimer;

function escapeHTML(value) {
  return String(value).replace(
    /[&<>"']/g,
    (character) =>
      ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" })[
        character
      ],
  );
}

function status(label, tone = "muted") {
  return `<span class="status tone-${tone}">${escapeHTML(label)}</span>`;
}

function grantedCount(integration) {
  return integration.endpoints.filter((feature) => feature.granted).length;
}

function accessStatus(integration) {
  const granted = grantedCount(integration);
  if (granted === 0) return status("No permission", "denied");
  if (integration.connector && !integration.connected)
    return status("Link login to use", "warn");
  if (granted < integration.endpoints.length)
    return status(
      `${granted} of ${integration.endpoints.length} features`,
      "warn",
    );
  return status("All features granted", "good");
}

function loginStatus(integration) {
  if (!integration.connector) return "Not required";
  return integration.connected
    ? status("Connected", "good")
    : status("Not connected", "warn");
}

function mark(integration) {
  return `<span class="integration-mark mark-${integration.id}" aria-hidden="true">${integration.mark}</span>`;
}

function card(integration) {
  return `<article class="integration-card">
    <div class="card-heading">${mark(integration)}<div><h2><button type="button" class="card-link" data-integration="${integration.id}" aria-haspopup="dialog">${integration.title}</button></h2><span class="card-category">${integration.category}</span></div></div>
    <p class="card-description">${integration.description}</p>
    <dl class="integration-facts">
      <div class="fact"><dt>Sites</dt><dd>${integration.restricted ? status("Approval required", "warn") : "All sites"}</dd></div>
      <div class="fact"><dt>Automations</dt><dd>${integration.connector ? '<span class="tone-muted">Not supported</span>' : "Supported"}</dd></div>
      <div class="fact"><dt>Agents</dt><dd>${integration.connector ? "Supported · as you" : "Supported"}</dd></div>
      <div class="fact"><dt>Your access</dt><dd>${accessStatus(integration)}</dd></div>
      <div class="fact"><dt>Linked login</dt><dd>${loginStatus(integration)}</dd></div>
    </dl>
    <div class="card-bottom"><span>${integration.endpoints.length} features</span><span aria-hidden="true">View details ↗</span></div>
  </article>`;
}

function renderCatalog() {
  catalog.dataset.layout = layout;
  if (previewState === "loading") {
    catalog.setAttribute("aria-busy", "true");
    catalog.innerHTML =
      '<div class="skeleton" aria-hidden="true"></div>'.repeat(6);
    document.querySelector("#result-count").textContent =
      "Loading integrations…";
    return;
  }
  catalog.removeAttribute("aria-busy");
  if (previewState !== "normal") {
    const isError = previewState === "error";
    catalog.innerHTML = `<div class="catalog-state"><h2>${isError ? "We couldn’t load the catalog" : "No integrations yet"}</h2><p>${isError ? "Your access hasn’t changed. Try loading the catalog again." : "Integrations configured by your platform team will appear here."}</p><button class="button button-secondary" type="button" data-reset>${isError ? "Try again" : "Restore sample catalog"}</button></div>`;
    document.querySelector("#result-count").textContent = isError
      ? "Catalog unavailable"
      : "0 integrations";
    return;
  }
  const visible = integrations.filter((integration) => {
    if (filter === "access") return grantedCount(integration) > 0;
    if (filter === "automation") return !integration.connector;
    if (filter === "login") return Boolean(integration.connector);
    return true;
  });
  catalog.innerHTML = visible.map(card).join("");
  document.querySelector("#result-count").textContent =
    `Showing ${visible.length} of ${integrations.length} integrations`;
}

function endpointView(integration, feature) {
  let label = status("Granted to you", "good");
  let reason =
    "Your platform permission is granted. The calling site must also allow this operation.";
  if (!feature.granted) {
    label = status("Permission needed", "denied");
    reason = `Ask a platform administrator for ${integration.id}.${feature.name}. Linking an account or approving a site does not grant this permission.`;
  } else if (integration.connector && !integration.connected) {
    label = status("Link login", "warn");
    reason = `Your platform permission is granted. Link your ${integration.connector} login before using this feature. The linked account’s own permissions also apply.`;
  }
  const context = !feature.granted
    ? "Not granted to you"
    : integration.restricted
      ? "Site approval also required"
      : integration.connector
        ? "Uses your linked account"
        : "Subject to site policy";
  return `<details class="endpoint">
    <summary><div class="endpoint-identity"><div class="endpoint-name"><strong>${feature.title}</strong><span class="effect ${feature.write ? "effect-write" : ""}">${feature.write ? "Write" : "Read"}</span></div><p class="endpoint-description">${feature.description}</p></div><div class="endpoint-status">${label}<small>${context}</small></div></summary>
    <div class="endpoint-contract"><p>${escapeHTML(reason)}</p><p><strong>Endpoint &amp; required permission</strong><br><code>${integration.id}.${feature.name}</code></p><div class="contract-grid"><div><h4>Input schema · sample</h4><pre>${escapeHTML(JSON.stringify(feature.input, null, 2))}</pre></div><div><h4>Output schema · sample</h4><pre>${escapeHTML(JSON.stringify(feature.output, null, 2))}</pre></div></div></div>
  </details>`;
}

function renderDetail(integration) {
  const granted = grantedCount(integration);
  const siteText = integration.restricted
    ? "Each site needs platform-admin approval. Approval does not grant endpoint permissions."
    : "No platform-admin approval needed. The site’s own integration grants still apply.";
  const automationText = integration.connector
    ? "Requires a person’s linked account; unattended automations cannot use this integration."
    : "Runs as the site. Needs site-principal permissions and any required approval.";
  const agentText = integration.connector
    ? "Agents can act as you, using your linked account and endpoint permissions."
    : "Agents can invoke permitted endpoints. They do not bypass caller or site access rules.";
  const loginText = !integration.connector
    ? "No linked login needed. Calls use a platform-managed credential."
    : integration.connected
      ? `Your sample ${integration.connector} account is connected. Its own permissions apply to every call.`
      : `A linked ${integration.connector} login is required. Your endpoint grants are already shown below.`;
  document.querySelector("#detail-content").innerHTML = `
    <div class="detail-header">
      <div class="detail-identity">${mark(integration)}<div><h2 id="detail-title">${integration.title}</h2><span class="card-category">${integration.category} · <code>${integration.id}</code></span></div></div>
      <p class="detail-description">${integration.description}</p>
      <div class="detail-summary"><div><h3>Site availability</h3><strong>${integration.restricted ? "Restricted · approval required" : "Available to all sites"}</strong><p>${siteText}</p></div><div><h3>Automations</h3><strong>${integration.connector ? "Not supported" : "Supported"}</strong><p>${automationText}</p></div><div><h3>Agent invocation</h3><strong>${integration.connector ? "Supported · as you" : "Supported"}</strong><p>${agentText}</p></div></div>
      <div class="detail-context"><p><strong>Linked login${integration.connector ? ` · ${integration.connected ? "Connected" : "Not connected"}` : " · Not required"}</strong><br>${loginText}</p>${integration.connector && !integration.connected ? '<button type="button" class="button button-secondary button-small" id="preview-connect">Preview linking login</button>' : ""}</div>
    </div>
    <div class="detail-body"><div class="features-heading"><h3>Features &amp; your access</h3><span>${granted} of ${integration.endpoints.length} permissions granted</span></div><p class="features-caption">These are your personal platform permissions, not a site-specific access check. Expand a feature for its permission and sample contract.</p>
      ${integration.endpoints.map((feature) => endpointView(integration, feature)).join("")}
      <p class="detail-footnote">${integration.audited ? "Calls to this integration are audited. " : ""}Write features change data in the connected system.${integration.restricted ? " Manage approval for a particular site from its Integrations tab in Your sites." : ""} This preview does not execute features or change permissions.</p>
    </div>`;
}

function openIntegration(id) {
  const integration = integrations.find((item) => item.id === id);
  if (!integration) return;
  renderDetail(integration);
  if (!detailDialog.open) detailDialog.showModal();
  detailDialog.scrollTop = 0;
}

function toast(message) {
  clearTimeout(toastTimer);
  const element = document.querySelector("#toast");
  element.textContent = message;
  element.hidden = false;
  toastTimer = setTimeout(() => {
    element.hidden = true;
  }, 5000);
}

document.addEventListener("click", (event) => {
  const integrationButton = event.target.closest("[data-integration]");
  if (integrationButton) {
    window.location.hash = integrationButton.dataset.integration;
    return;
  }
  const filterButton = event.target.closest("[data-filter]");
  if (filterButton) {
    filter = filterButton.dataset.filter;
    document
      .querySelectorAll("[data-filter]")
      .forEach((button) =>
        button.setAttribute("aria-pressed", String(button === filterButton)),
      );
    renderCatalog();
    return;
  }
  const layoutButton = event.target.closest("[data-layout]");
  if (layoutButton?.tagName === "BUTTON") {
    layout = layoutButton.dataset.layout;
    document
      .querySelectorAll("button[data-layout]")
      .forEach((button) =>
        button.setAttribute("aria-pressed", String(button === layoutButton)),
      );
    renderCatalog();
    return;
  }
  if (event.target.closest("[data-close]")) {
    event.target.closest("dialog").close();
    return;
  }
  if (event.target.closest("#review-notes")) {
    notesDialog.showModal();
    return;
  }
  if (event.target.closest("[data-preview-nav]")) {
    event.preventDefault();
    toast(
      "Only the Integrations page is included in this prototype. Other portal pages are unchanged.",
    );
    return;
  }
  if (event.target.closest("#catalog-nav")) {
    event.preventDefault();
    filter = "all";
    document
      .querySelectorAll("[data-filter]")
      .forEach((button) =>
        button.setAttribute(
          "aria-pressed",
          String(button.dataset.filter === "all"),
        ),
      );
    renderCatalog();
    return;
  }
  if (event.target.closest("[data-reset]")) {
    previewState = "normal";
    document.querySelector("#preview-state").value = previewState;
    renderCatalog();
    return;
  }
  if (event.target.closest("#preview-connect")) {
    const integration = integrations.find(
      (item) => `#${item.id}` === window.location.hash,
    );
    if (!integration) return;
    integration.connected = true;
    renderCatalog();
    renderDetail(integration);
    detailDialog.querySelector("[data-close]").focus();
    toast("Preview only: sample login linked. No real account was connected.");
  }
});

document.querySelector("#preview-state").addEventListener("change", (event) => {
  previewState = event.target.value;
  renderCatalog();
});

for (const dialog of [detailDialog, notesDialog]) {
  dialog.addEventListener("click", (event) => {
    if (event.target !== dialog) return;
    const bounds = dialog.getBoundingClientRect();
    if (
      event.clientX < bounds.left ||
      event.clientX > bounds.right ||
      event.clientY < bounds.top ||
      event.clientY > bounds.bottom
    )
      dialog.close();
  });
}

detailDialog.addEventListener("close", () => {
  if (window.location.hash)
    history.replaceState(
      null,
      "",
      window.location.pathname + window.location.search,
    );
});
window.addEventListener("hashchange", () => {
  if (!window.location.hash) {
    detailDialog.close();
    return;
  }
  openIntegration(window.location.hash.slice(1));
});

renderCatalog();
if (window.location.hash) openIntegration(window.location.hash.slice(1));

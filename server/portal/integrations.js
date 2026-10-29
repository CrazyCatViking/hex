(() => {
  const catalog = document.getElementById("integration-catalog");
  if (!catalog) return;

  const cards = [...catalog.querySelectorAll("[data-integration-card]")];
  const links = [...catalog.querySelectorAll("[data-integration-open]")];
  const filters = [...catalog.querySelectorAll("[data-integration-filter]")];
  const layouts = [...catalog.querySelectorAll("[data-integration-layout]")];
  const grid = catalog.querySelector("[data-integration-grid]");
  const dialog = document.getElementById("integration-dialog");
  const content = dialog.querySelector("[data-integration-dialog-content]");
  const loading = dialog.querySelector("[data-integration-loading]");
  const errorBox = dialog.querySelector("[data-integration-error]");
  const title = document.getElementById("integration-dialog-title");
  let current;
  let controller;

  catalog.querySelector("[data-integration-controls]").hidden = false;

  function filterCatalog(filter) {
    let visible = 0;
    for (const card of cards) {
      const matches =
        filter === "all" ||
        (filter === "access" && Number(card.dataset.granted) > 0) ||
        (filter === "automation" && card.dataset.automation === "true") ||
        (filter === "login" && card.dataset.login === "true");
      card.hidden = !matches;
      if (matches) visible++;
    }
    for (const button of filters) {
      button.setAttribute(
        "aria-pressed",
        String(button.dataset.integrationFilter === filter),
      );
    }
    catalog.querySelector("[data-integration-count]").textContent =
      `Showing ${visible} of ${cards.length} integrations`;
    catalog.querySelector("[data-integration-empty]").hidden = visible !== 0;
  }

  for (const button of filters) {
    button.addEventListener("click", () =>
      filterCatalog(button.dataset.integrationFilter),
    );
  }
  catalog
    .querySelector("[data-integration-reset]")
    .addEventListener("click", () => {
      filterCatalog("all");
      filters[0].focus();
    });
  for (const button of layouts) {
    button.addEventListener("click", () => {
      grid.dataset.layout = button.dataset.integrationLayout;
      for (const choice of layouts) {
        choice.setAttribute("aria-pressed", String(choice === button));
      }
    });
  }

  // Ordinary detail-page links remain usable without JavaScript or in a new tab.
  if (typeof dialog.showModal !== "function") return;
  for (const link of links) {
    link.setAttribute("aria-haspopup", "dialog");
    link.addEventListener("click", (event) => {
      if (
        event.button !== 0 ||
        event.metaKey ||
        event.ctrlKey ||
        event.shiftKey ||
        event.altKey
      )
        return;
      event.preventDefault();
      history.pushState(null, "", `#${link.dataset.integrationOpen}`);
      syncDialog();
    });
  }

  async function loadDetails(link) {
    controller?.abort();
    const request = new AbortController();
    controller = request;
    loading.hidden = false;
    errorBox.hidden = true;
    content.replaceChildren();
    content.setAttribute("aria-busy", "true");
    title.textContent = `${link.textContent.trim()} · Integration details`;
    dialog.querySelector("[data-integration-page]").href = link.href;
    try {
      const response = await fetch(
        `/api/hex/manage/integrations/${encodeURIComponent(link.dataset.integrationOpen)}`,
        {
          headers: { Accept: "text/html", "X-Hex-Request": "1" },
          cache: "no-store",
          signal: request.signal,
        },
      );
      if (!response.ok) {
        let message = "The integration details could not be loaded. Try again.";
        if (
          response.headers.get("Content-Type")?.includes("application/json")
        ) {
          const result = await response.json();
          if (typeof result.error === "string") message = result.error;
        }
        throw new Error(message);
      }
      if (
        response.headers.get("X-Hex-Portal") !== "1" ||
        !response.headers.get("Content-Type")?.includes("text/html")
      ) {
        throw new Error(
          "Your session may have expired. Reload this page to sign in again.",
        );
      }
      const document = new DOMParser().parseFromString(
        await response.text(),
        "text/html",
      );
      const detail = document.querySelector("[data-integration-detail]");
      if (
        !detail ||
        detail.dataset.integrationDetail !== link.dataset.integrationOpen
      ) {
        throw new Error(
          "The platform returned unexpected integration details. Reload the page and try again.",
        );
      }
      if (controller !== request || request.signal.aborted) return;
      content.replaceChildren(detail);
    } catch (error) {
      if (controller !== request || request.signal.aborted) return;
      errorBox.querySelector("[data-integration-error-message]").textContent =
        error.message ||
        "The platform could not be reached. Check your connection and try again.";
      errorBox.hidden = false;
    } finally {
      if (controller === request) {
        loading.hidden = true;
        content.removeAttribute("aria-busy");
      }
    }
  }

  function syncDialog() {
    const link = links.find(
      (item) => `#${item.dataset.integrationOpen}` === location.hash,
    );
    if (!link) {
      if (dialog.open) dialog.close();
      return;
    }
    if (dialog.open && current === link) return;
    current = link;
    if (!dialog.open) dialog.showModal();
    dialog.scrollTop = 0;
    loadDetails(link);
  }

  dialog
    .querySelector("[data-integration-close]")
    .addEventListener("click", () => dialog.close());
  dialog
    .querySelector("[data-integration-retry]")
    .addEventListener("click", () => {
      if (current) loadDetails(current);
    });
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
  dialog.addEventListener("close", () => {
    controller?.abort();
    controller = undefined;
    content.replaceChildren();
    if (current && location.hash === `#${current.dataset.integrationOpen}`) {
      history.replaceState(null, "", location.pathname + location.search);
    }
    if (current?.closest("[data-integration-card]").hidden) {
      filters
        .find((button) => button.getAttribute("aria-pressed") === "true")
        ?.focus();
    } else {
      current?.focus({ preventScroll: true });
    }
    current = undefined;
  });
  window.addEventListener("hashchange", syncDialog);
  window.addEventListener("popstate", syncDialog);
  syncDialog();
})();

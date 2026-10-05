const element = (id) => document.getElementById(id);

// Keep initials visible when an icon request fails, including in HTMX content.
document.addEventListener(
  "error",
  (event) => {
    if (event.target.matches?.("img[data-site-icon]")) {
      event.target.remove();
    }
  },
  true,
);
for (const image of document.querySelectorAll("img[data-site-icon]")) {
  if (image.complete && image.naturalWidth === 0) {
    image.remove();
  }
}

// Brief confirmation messages, such as after copying a link.
let toastTimer;
function toast(message) {
  const box = element("toast");
  if (!box) {
    return;
  }
  box.textContent = message;
  box.hidden = false;
  clearTimeout(toastTimer);
  toastTimer = setTimeout(() => {
    box.hidden = true;
  }, 2400);
}
window.hexToast = toast;

function showError(message) {
  const banner = element("page-error");
  if (!banner) {
    return;
  }
  banner.textContent = message;
  banner.hidden = false;
}
window.hexError = showError;

document.addEventListener("click", async (event) => {
  const button = event.target.closest("[data-copy]");
  if (!button) {
    return;
  }
  event.preventDefault();
  try {
    await navigator.clipboard.writeText(button.dataset.copy);
    toast("Link copied");
  } catch {
    toast("Select the address bar to copy the link");
  }
});

// The account menu closes on Escape and when clicking elsewhere.
const account = document.querySelector(".account");
if (account) {
  document.addEventListener("click", (event) => {
    if (account.open && !account.contains(event.target)) {
      account.open = false;
    }
  });
  document.addEventListener("keydown", (event) => {
    if (event.key === "Escape" && account.open) {
      account.open = false;
      account.querySelector("summary").focus();
    }
  });
}

// Installer downloads on the build and publish page.
const commands = {
  macos: 'bash "$HOME/Downloads/install-hex-macos.sh"',
  linux: 'bash "$HOME/Downloads/install-hex-linux.sh"',
  windows:
    'powershell.exe -NoProfile -ExecutionPolicy Bypass -File "$HOME\\Downloads\\install-hex-windows.ps1"',
};

function updateInstaller() {
  const os = element("os").value;
  element("install-command").textContent = commands[os];
  element("download-installer").href = `/api/hex/install/${os}`;
  element("copy-status").textContent = "";
}

if (element("installer-controls")) {
  const platform =
    navigator.userAgentData?.platform ||
    navigator.platform ||
    navigator.userAgent;
  element("os").value = /win/i.test(platform)
    ? "windows"
    : /mac/i.test(platform)
      ? "macos"
      : "linux";
  element("os").addEventListener("change", updateInstaller);
  element("copy-command").addEventListener("click", async () => {
    try {
      await navigator.clipboard.writeText(
        element("install-command").textContent,
      );
      element("copy-status").textContent = "Command copied.";
    } catch {
      element("copy-status").textContent = "Select and copy the command above.";
    }
  });
  updateInstaller();
}

// A day's tooltip: its own lines from data-tooltip ("title|line|…"), or
// page views and people for analytics charts.
function tooltipLines(day) {
  const lines = day.dataset.tooltip
    ? day.dataset.tooltip.split("|")
    : [
        day.dataset.label,
        `${day.dataset.views} page views`,
        `${day.dataset.visitors} people`,
      ];
  return lines.map((text, index) => {
    const element = document.createElement(index === 0 ? "strong" : "span");
    element.textContent = text;
    return element;
  });
}

// Analytics charts: a tooltip for the day under the pointer. The SVG keeps
// <title> elements for browsers without JavaScript; they are removed here
// so two tooltips never show at once.
function setupTrendCharts(root) {
  for (const plot of root.querySelectorAll("[data-trend]")) {
    if (plot.dataset.ready) {
      continue;
    }
    plot.dataset.ready = "true";
    const tooltip = plot.querySelector(".trend-tooltip");
    const days = [...plot.querySelectorAll(".trend-day")];
    for (const day of days) {
      day.querySelector("title")?.remove();
    }
    let active;
    const show = (day) => {
      active?.classList.remove("is-active");
      active = day;
      day.classList.add("is-active");
      const bounds = plot.getBoundingClientRect();
      const box = day.querySelector(".trend-hit").getBoundingClientRect();
      tooltip.replaceChildren(...tooltipLines(day));
      tooltip.hidden = false;
      const center = box.left - bounds.left + box.width / 2;
      const half = tooltip.offsetWidth / 2;
      tooltip.style.left = `${Math.min(Math.max(center, half), bounds.width - half)}px`;
    };
    const hide = () => {
      active?.classList.remove("is-active");
      active = undefined;
      tooltip.hidden = true;
    };
    plot.addEventListener("pointermove", (event) => {
      const bounds = plot.getBoundingClientRect();
      const index = Math.floor(
        ((event.clientX - bounds.left) / bounds.width) * days.length,
      );
      const day = days[Math.min(Math.max(index, 0), days.length - 1)];
      if (day && day !== active) {
        show(day);
      }
    });
    plot.addEventListener("pointerleave", hide);
    // Keyboard: focus the chart and step through days with the arrow keys.
    plot.tabIndex = 0;
    plot.setAttribute(
      "aria-description",
      "Use the left and right arrow keys to read each day.",
    );
    plot.addEventListener("keydown", (event) => {
      if (event.key !== "ArrowLeft" && event.key !== "ArrowRight") {
        return;
      }
      event.preventDefault();
      const current = days.indexOf(active);
      const step = event.key === "ArrowRight" ? 1 : -1;
      const next =
        current < 0
          ? days.length - 1
          : Math.min(Math.max(current + step, 0), days.length - 1);
      show(days[next]);
    });
    plot.addEventListener("blur", hide);
  }
}
setupTrendCharts(document);
document.addEventListener("htmx:afterSettle", () => setupTrendCharts(document));

// HTMX errors: show the API's message, or a sign-in hint when a session
// expired and the gateway answered with its login page instead.
document.addEventListener("htmx:responseError", (event) => {
  let message = "That did not work. Reload the page and try again.";
  try {
    message = JSON.parse(event.detail.xhr.responseText).error || message;
  } catch {
    // Not a JSON error; keep the generic message.
  }
  showError(message);
});
document.addEventListener("htmx:sendError", () => {
  showError("The platform could not be reached. Check your connection.");
});
document.addEventListener("htmx:beforeSwap", (event) => {
  const text = event.detail.xhr.responseText.trim();
  const expected = event.detail.target?.id === "catalog";
  if (
    expected &&
    event.detail.xhr.status === 200 &&
    !text.startsWith('<div id="catalog">')
  ) {
    event.detail.shouldSwap = false;
    showError(
      "Your session may have expired. Reload this page to sign in again.",
    );
  }
});
document.addEventListener("htmx:afterRequest", (event) => {
  if (event.detail.successful && element("page-error")) {
    element("page-error").hidden = true;
  }
});

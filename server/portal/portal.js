const element = (id) => document.getElementById(id);

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

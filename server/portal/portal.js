const element = (id) => document.getElementById(id);
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

for (const event of [
  "htmx:responseError",
  "htmx:sendError",
  "htmx:swapError",
]) {
  document.addEventListener(event, () => {
    element("catalog-error").hidden = false;
    element("catalog-error").textContent =
      "The overview could not be refreshed. Reload this page to check your company sign-in, then try again.";
  });
}
document.addEventListener("htmx:beforeSwap", (event) => {
  if (
    event.detail.xhr.status === 200 &&
    !event.detail.xhr.responseText.trim().startsWith('<div id="catalog">')
  ) {
    event.detail.shouldSwap = false;
    element("catalog-error").hidden = false;
    element("catalog-error").textContent =
      "Your session may have expired. Reload this page to sign in again.";
  }
});
document.addEventListener("htmx:afterSwap", () => {
  element("catalog-error").hidden = true;
});

const errorBox = document.getElementById("manage-error");

// Adds a picked person or group to its list as a removable chip with a
// hidden form value. Built with DOM methods so names are never parsed as HTML.
function addChip({ list, principal, name, detail }) {
  const target = document.getElementById(`list-${list}`);
  if (!target) {
    return;
  }
  const values = [...target.querySelectorAll("input")].map(
    (input) => input.value,
  );
  if (!values.includes(principal)) {
    const chip = document.createElement("li");
    chip.className = "chip";
    const label = document.createElement("span");
    label.textContent = name;
    if (detail) {
      const muted = document.createElement("span");
      muted.className = "muted";
      muted.textContent = detail;
      label.append(" ", muted);
    }
    const input = document.createElement("input");
    input.type = "hidden";
    input.name = list;
    input.value = principal;
    const remove = document.createElement("button");
    remove.type = "button";
    remove.className = "remove";
    remove.setAttribute("aria-label", `Remove ${name}`);
    remove.textContent = "×";
    chip.append(label, input, remove);
    target.append(chip);
  }

  const fieldset = target.closest("fieldset");
  fieldset.querySelector("input[type=search]").value = "";
  fieldset.querySelector(".suggestions").replaceChildren();
}

document.addEventListener("click", (event) => {
  const suggestion = event.target.closest(".suggestion");
  if (suggestion) {
    addChip(suggestion.dataset);
    return;
  }
  const remove = event.target.closest(".chip .remove");
  if (remove) {
    remove.closest(".chip").remove();
  }
});

// Enter in a picker adds the first suggestion instead of submitting the form.
document.addEventListener("keydown", (event) => {
  if (event.key !== "Enter" || !event.target.closest(".picker")) {
    return;
  }
  event.preventDefault();
  const first = event.target
    .closest("fieldset")
    .querySelector(".suggestions .suggestion");
  if (first) {
    addChip(first.dataset);
  }
});

function showError(message) {
  errorBox.textContent = message;
  errorBox.hidden = false;
}

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
document.addEventListener("htmx:afterRequest", (event) => {
  if (event.detail.successful) {
    errorBox.hidden = true;
  }
});

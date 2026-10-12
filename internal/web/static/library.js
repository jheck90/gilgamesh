// Update a library preview's price as soon as a chapter is ticked or
// unticked, or the edit box changes. Without this script the reader taps
// "Update price".
document.addEventListener("change", (event) => {
  const input = event.target;
  if (input.type !== "checkbox" || !input.form || !input.form.classList.contains("preview")) return;
  input.form.requestSubmit(input.form.querySelector("button[data-update]"));
});

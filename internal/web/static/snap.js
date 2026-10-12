// Snap as soon as the photo is taken, so it's one tap from the book page.
// Without this script the reader taps "Snap" after taking it.
document.addEventListener("change", (event) => {
  const input = event.target;
  if (input.name !== "photo" || !input.files.length) return;
  input.form.querySelector("label").textContent = "Reading the page…";
  input.form.submit();
});

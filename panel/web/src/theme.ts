// Kumo components read light/dark from data-mode; mirror the system preference.
// Panel colours already follow prefers-color-scheme in CSS before this runs.
const dark = window.matchMedia("(prefers-color-scheme: dark)");

function apply() {
  document.documentElement.dataset.mode = dark.matches ? "dark" : "light";
}

apply();
dark.addEventListener("change", apply);

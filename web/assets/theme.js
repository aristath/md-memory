// Set the preference before styles load. Device mode follows the native color scheme.
try {
  const theme = localStorage.getItem("songya-theme");
  document.documentElement.dataset.theme = ["light", "dark"].includes(theme)
    ? theme
    : "system";
} catch {
  document.documentElement.dataset.theme = "system";
}

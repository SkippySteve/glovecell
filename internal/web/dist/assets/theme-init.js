// Applies the saved console theme before first paint, so the page never flashes
// the wrong one. A separate file because the CSP allows no inline script; keep
// the key and values in sync with src/theme.ts.
(function () {
  var theme = 'dark';
  try {
    var saved = localStorage.getItem('console-theme');
    if (saved === 'dark' || saved === 'light' || saved === 'system') theme = saved;
  } catch (e) { /* storage blocked: keep the default */ }
  document.documentElement.setAttribute('data-theme', theme);
})();

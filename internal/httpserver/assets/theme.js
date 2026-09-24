// Apply the saved palette before the dashboard paints.
(() => {
  let theme = 'light';
  try {
    const stored = localStorage.getItem('cpra-theme');
    if (stored === 'dark' || stored === 'light') theme = stored;
  } catch { /* Fall back to the default palette when storage is unavailable. */ }
  document.documentElement.dataset.theme = theme;
  document.querySelector('meta[name="theme-color"]').content = theme === 'dark' ? '#15161a' : '#fbf3df';
})();

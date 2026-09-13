// Preserve choices saved before the System option was added to the palette.
// Material stores the option index as well as its colors, so both must agree.
(() => {
  try {
    const palette = __md_get('__palette');
    const color = palette?.color;
    if (!color || color.media || !['default', 'slate'].includes(color.scheme)) return;
    const dark = color.scheme === 'slate';
    palette.index = dark ? 2 : 1;
    color.media = dark ? '(prefers-color-scheme: dark)' : '(prefers-color-scheme: light)';
    __md_set('__palette', palette);
  } catch { /* Appearance remains available when browser storage is restricted. */ }
})();

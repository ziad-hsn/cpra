/** @type {import('svgo').Config} */
module.exports = {
  multipass: true,
  js2svg: { indent: 2, pretty: true, eol: 'lf', finalNewline: true },
  plugins: [
    { name: 'preset-default', params: { overrides: {
      removeDesc: false,
      cleanupIds: false,   // ids are authored as cpra-* in src; nothing to prefix
      removeUnknownsAndDefaults: { keepRoleAttr: true, keepAriaAttrs: true },
    } } },
    'sortAttrs',
  ],
};

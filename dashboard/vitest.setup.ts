import '@testing-library/jest-dom/vitest';

// JSDOM has no top layer or native modal interaction. These methods let
// component tests exercise draft lifetime; Chromium tests verify modality.
if (typeof HTMLDialogElement !== 'undefined' && !HTMLDialogElement.prototype.showModal) {
  HTMLDialogElement.prototype.showModal = function () { this.open = true; };
  HTMLDialogElement.prototype.close = function () { this.open = false; };
}

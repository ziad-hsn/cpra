'use strict';
const assert = require('node:assert/strict');
const { test } = require('node:test');
const { auditAccessibility, version } = require('./accessibility.cjs');

async function record(data, count = 1) {
  class Element {
    tagName = 'TD';
    parentElement = null;
  }
  const node = { element: new Element(), html: '<td>private-value</td>', target: ['#private-value'],
    any: [{ id: 'color-contrast', data, message: 'private-value', relatedNodes: [{ html: 'private-value' }] }], all: [], none: [] };
  const finding = { id: 'color-contrast', impact: 'serious', nodes: Array.from({ length: count }, () => node) };
  const originalWindow = global.window, originalElement = global.Element, originalDocument = global.document;
  const originalEvidence = process.env.CPRA_BROWSER_EVIDENCE_DIR;
  delete process.env.CPRA_BROWSER_EVIDENCE_DIR;
  global.Element = Element;
  global.document = {};
  global.window = { axe: { run: async () => ({ testEngine: { version }, violations: [], incomplete: [finding], passes: [finding] }) } };
  try {
    return await auditAccessibility({ evaluate: async (callback, argument) => typeof callback === 'function' ? callback(argument) : undefined }, 'contrast-fixture', []);
  } finally {
    if (originalWindow === undefined) delete global.window; else global.window = originalWindow;
    if (originalElement === undefined) delete global.Element; else global.Element = originalElement;
    if (originalDocument === undefined) delete global.document; else global.document = originalDocument;
    if (originalEvidence === undefined) delete process.env.CPRA_BROWSER_EVIDENCE_DIR; else process.env.CPRA_BROWSER_EVIDENCE_DIR = originalEvidence;
  }
}

test('contrast evidence keeps measurements and fixed reasons without DOM or diagnostic text', async () => {
  const report = await record({ messageKey: 'elmPartiallyObscured', fgColor: '#AABBCC', bgColor: '#001122',
    contrastRatio: 8.12, expectedContrastRatio: '4.5:1', fontSize: '12.0pt (16px)', fontWeight: 'normal',
    colorParse: 'private-value', html: 'private-value' });
  assert.equal(JSON.stringify(report).includes('private-value'), false);
  assert.deepEqual(report.needsReview[0].contrast[0], { location: 'td:nth-of-type(1)', checks: [{
    group: 'any', reason: 'elmPartiallyObscured', foreground: '#aabbcc', background: '#001122', shadow: null,
    ratio: 8.12, expectedRatio: 4.5, fontSizePt: 12, fontSizePx: 16, fontWeight: 'normal',
  }] });
  assert.deepEqual(report.contrastPasses[0].contrast, report.needsReview[0].contrast);
});

test('unknown contrast diagnostics and invalid measurements cannot enter the report', async () => {
  const report = await record({ messageKey: 'private-value', fgColor: 'private-value', bgColor: '#ffffff',
    shadowColor: 'private-value', contrastRatio: Infinity, expectedContrastRatio: 'private-value',
    fontSize: 'private-value', fontWeight: 'private-value' });
  assert.equal(JSON.stringify(report).includes('private-value'), false);
  const result = report.needsReview[0].contrast[0].checks[0];
  assert.equal(result.reason, 'unreported');
  for (const key of ['foreground', 'shadow', 'ratio', 'expectedRatio', 'fontSizePt', 'fontSizePx', 'fontWeight']) assert.equal(result[key], null);
});

test('missing colors remain unavailable and contrast output is bounded', async () => {
  const report = await record({ messageKey: 'outsideViewport', fgColor: '#ffffff', contrastRatio: 0,
    fontSize: '999999pt (999999px)', expectedContrastRatio: '999:1' }, 101);
  assert.equal(report.needsReview[0].nodeCount, 101);
  assert.equal(report.needsReview[0].contrast.length, 100);
  assert.equal(report.needsReview[0].locations.length, 100);
  assert.equal(report.contrastPasses[0].contrast.length, 100);
  const result = report.needsReview[0].contrast[0].checks[0];
  for (const key of ['ratio', 'expectedRatio', 'fontSizePt', 'fontSizePx']) assert.equal(result[key], null);
});

test('ratios must be finite numeric measurements in the physical contrast range', async () => {
  for (const value of [Infinity, NaN, -1, 0, 0.5, 21.01, '8.12']) {
    const report = await record({ fgColor: '#ffffff', bgColor: '#000000', contrastRatio: value });
    assert.equal(report.needsReview[0].contrast[0].checks[0].ratio, null);
  }
});

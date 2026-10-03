'use strict';
const fs = require('node:fs');
const path = require('node:path');
const axeRoot = path.join(path.dirname(process.env.CPRA_BROWSER_MODULE || path.join(__dirname, 'browser/node_modules/playwright')), 'axe-core');
const version = require(path.join(axeRoot, 'package.json')).version;
const expected = require('./browser/package.json').dependencies['axe-core'];
if (version !== expected) throw new Error('Use the locked accessibility scanner version');

async function auditAccessibility(page, label, reports) {
  if (!/^[a-z0-9-]{1,80}$/.test(label)) throw new Error('Use a fixed accessibility checkpoint name');
  await page.evaluate(fs.readFileSync(path.join(axeRoot, 'axe.min.js'), 'utf8'));
  const report = await page.evaluate(async checkpoint => {
    const results = await window.axe.run(document, {
      runOnly: { type: 'tag', values: ['wcag2a', 'wcag2aa', 'wcag21a', 'wcag21aa', 'wcag22aa'] },
      elementRef: true,
    });
    // Export structural locations only. Axe's HTML and generated selectors can
    // include field values, private identifiers or other user-provided text.
    const location = element => {
      const parts = [];
      for (let current = element; current instanceof Element && parts.length < 8; current = current.parentElement) {
        const tag = current.tagName.toLowerCase();
        const siblings = current.parentElement ? [...current.parentElement.children].filter(child => child.tagName === current.tagName) : [current];
        parts.unshift(`${tag}:nth-of-type(${siblings.indexOf(current) + 1})`);
      }
      return parts.join(' > ');
    };
    const finding = result => ({ rule: result.id, impact: result.impact, nodeCount: result.nodes.length, locations: result.nodes.slice(0, 100).map(node => location(node.element)) });
    return { checkpoint, scanner: results.testEngine.version, violations: results.violations.map(finding), needsReview: results.incomplete.map(finding), passedRules: results.passes.map(result => result.id), automatedOnly: true };
  }, label);
  reports.push(report);
  if (process.env.CPRA_BROWSER_EVIDENCE_DIR) {
    const name = `accessibility-${path.basename(require.main.filename, '.cjs')}-${String(reports.length).padStart(2, '0')}-${label}.json`;
    fs.writeFileSync(path.join(process.env.CPRA_BROWSER_EVIDENCE_DIR, name), JSON.stringify(report, null, 2) + '\n', { flag: 'wx', mode: 0o600 });
  }
  if (report.violations.length) throw new Error(`Accessibility violations at ${label}: ${report.violations.map(item => item.rule).join(', ')}`);
  return report;
}

module.exports = { auditAccessibility, version };

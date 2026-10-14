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
    // Export structural locations and bounded contrast measurements. Axe's HTML and generated selectors can
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
    const reasons = new Set(['bgImage', 'bgGradient', 'imgNode', 'bgOverlap', 'complexTextShadows',
      'fgAlpha', 'elmPartiallyObscured', 'elmPartiallyObscuring', 'outsideViewport', 'equalRatio',
      'shortTextContent', 'nonBmp', 'pseudoContent', 'colorParse', 'fgOnShadowColor', 'shadowOnBgColor', 'hidden']);
    const color = value => typeof value === 'string' && /^#[a-f0-9]{6}$/i.test(value) ? value.toLowerCase() : null;
    const number = (value, max, min = 0) => typeof value === 'number' && Number.isFinite(value) && value >= min && value <= max ? value : null;
    const contrast = node => ['any', 'all', 'none'].flatMap(group => (node[group] || [])
      .filter(check => check.id === 'color-contrast').slice(0, 1).map(check => {
        const data = check.data || {};
        const size = typeof data.fontSize === 'string' && /^(\d+(?:\.\d+)?)pt \((\d+(?:\.\d+)?)px\)$/.exec(data.fontSize);
        const expected = typeof data.expectedContrastRatio === 'string' && /^(\d+(?:\.\d+)?):1$/.exec(data.expectedContrastRatio);
        const foreground = color(data.fgColor), background = color(data.bgColor);
        return { group, reason: reasons.has(data.messageKey) ? data.messageKey : 'unreported',
          foreground, background, shadow: color(data.shadowColor),
          ratio: foreground && background ? number(data.contrastRatio, 21, 1) : null,
          expectedRatio: expected ? number(Number(expected[1]), 21, 1) : null,
          fontSizePt: size ? number(Number(size[1]), 4096) : null,
          fontSizePx: size ? number(Number(size[2]), 4096) : null,
          fontWeight: ['normal', 'bold'].includes(data.fontWeight) ? data.fontWeight : null };
      }));
    const finding = result => ({ rule: result.id, impact: result.impact, nodeCount: result.nodes.length,
      locations: result.nodes.slice(0, 100).map(node => location(node.element)),
      contrast: result.id === 'color-contrast' ? result.nodes.slice(0, 100).map(node => ({ location: location(node.element), checks: contrast(node) })) : [] });
    return { checkpoint, scanner: results.testEngine.version, violations: results.violations.map(finding), needsReview: results.incomplete.map(finding),
      contrastPasses: results.passes.filter(result => result.id === 'color-contrast').map(finding),
      passedRules: results.passes.map(result => result.id), automatedOnly: true };
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

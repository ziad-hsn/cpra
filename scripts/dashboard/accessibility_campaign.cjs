// Frontend qualification against fixed HTTP fixtures. This does not execute
// CPRa's API, Raft, controller or providers; the Go browser campaigns do that.
'use strict';
const fs = require('node:fs');
const path = require('node:path');
const crypto = require('node:crypto');
const https = require('node:https');
const { execFileSync } = require('node:child_process');
const { chromium } = require(process.env.CPRA_BROWSER_MODULE || './browser/node_modules/playwright');
const { auditAccessibility, version } = require('./accessibility.cjs');
const root = path.resolve(__dirname, '../..');
const args = process.argv.slice(2);
if (args.length !== 2 || args[0] !== '--out') throw new Error('Usage: node scripts/dashboard/accessibility_campaign.cjs --out NEW_DIRECTORY');
const out = path.resolve(args[1]);
fs.mkdirSync(path.dirname(out), { recursive: true, mode: 0o700 });
fs.mkdirSync(out, { mode: 0o700 });
process.env.CPRA_BROWSER_EVIDENCE_DIR = out;
const sha = bytes => crypto.createHash('sha256').update(bytes).digest('hex');
const manifest = JSON.parse(fs.readFileSync(path.join(root, 'dashboard/build-manifest.json')));
const inputs = { ...manifest.inputs, ...manifest.outputs };
for (const name of ['scripts/dashboard/accessibility.cjs', 'scripts/dashboard/accessibility_campaign.cjs', 'scripts/dashboard/browser/package.json', 'scripts/dashboard/browser/package-lock.json']) inputs[name] = sha(fs.readFileSync(path.join(root, name)));
const unchanged = () => Object.entries(inputs).every(([name, digest]) => sha(fs.readFileSync(path.join(root, name))) === digest);
const report = { schema_version: 1, evidence_class: 'frontend_http_fixture', result: 'running', inputs, scanner: version, node: process.version, accessibility: [], keyboardChecks: [], providerOperations: 0, apiServerVerified: false, releaseGatePassed: false, fullWCAGConformance: false, endurancePerformed: false };
const save = () => fs.writeFileSync(path.join(out, 'result.json'), JSON.stringify(report, null, 2) + '\n', { mode: 0o600 });
save();
let browser, server, timer;
const check = (condition, label) => { if (!condition) throw new Error(label); };
const token = 'frontend-fixture-token-01234567890123456789';
const secret = 'frontend-fixture-private-value-0123456789';

(async () => {
  check(unchanged(), 'Embedded frontend does not match its recorded inputs; run make dashboard-build');
  execFileSync('openssl', ['req', '-x509', '-newkey', 'rsa:2048', '-nodes', '-keyout', path.join(out, 'key.pem'), '-out', path.join(out, 'cert.pem'), '-days', '1', '-subj', '/CN=localhost', '-addext', 'subjectAltName=IP:127.0.0.1'], { stdio: 'ignore', timeout: 10_000 });
  fs.chmodSync(path.join(out, 'key.pem'), 0o600);
  const cert = fs.readFileSync(path.join(out, 'cert.pem'));
  const pin = crypto.createHash('sha256').update(new crypto.X509Certificate(cert).publicKey.export({ type: 'spki', format: 'der' })).digest('base64');
  const assets = path.join(root, 'internal/httpserver/assets');
  const types = { '.html': 'text/html', '.js': 'application/javascript', '.css': 'text/css', '.svg': 'image/svg+xml', '.wasm': 'application/wasm', '.ttf': 'font/ttf', '.woff2': 'font/woff2', '.png': 'image/png' };
  server = https.createServer({ key: fs.readFileSync(path.join(out, 'key.pem')), cert }, (request, response) => {
    const name = new URL(request.url, 'https://localhost').pathname;
    let file = path.resolve(assets, '.' + name);
    if (!file.startsWith(assets + path.sep)) file = path.join(assets, 'index.html');
    if (!fs.existsSync(file) || !fs.statSync(file).isFile()) file = path.join(assets, 'index.html');
    response.writeHead(200, { 'Content-Type': types[path.extname(file)] || 'application/octet-stream', 'Cache-Control': 'no-store' });
    fs.createReadStream(file).pipe(response);
  });
  await new Promise(resolve => server.listen(0, '127.0.0.1', resolve));
  const origin = `https://127.0.0.1:${server.address().port}`;
  browser = await chromium.launch({ headless: true, executablePath: process.env.CPRA_BROWSER_EXECUTABLE || chromium.executablePath(), args: [`--ignore-certificate-errors-spki-list=${pin}`] });
  report.browser = browser.version();
  report.playwright = require(path.join(process.env.CPRA_BROWSER_MODULE || path.join(__dirname, 'browser/node_modules/playwright'), 'package.json')).version;
  const context = await browser.newContext({ viewport: { width: 1440, height: 1000 } });
  const page = await context.newPage();
  page.setDefaultTimeout(8000);
  const errors = [];
  page.on('pageerror', () => errors.push('Uncaught frontend error'));
  let finishPending;
  let pendingArrived;
  const pendingRequest = new Promise(resolve => { pendingArrived = resolve; });
  const kinds = { monitors: 'Monitor', 'notification-endpoints': 'NotificationEndpoint', recipients: 'Recipient', 'notification-groups': 'NotificationGroup', credentials: 'Credential' };
  const resource = (type, id, spec) => ({ apiVersion: 'cpra.io/v2', kind: kinds[type], metadata: { id, name: id, uid: `uid-${id}`, resourceVersion: 'rv-1' }, spec });
  const records = {
    monitors: resource('monitors', 'checkout', { enabled: true, check: { driver: { type: 'tcp', config: { host: 'localhost', port: 443 } }, interval: '60s', timeout: '5s' }, recovery: { driver: { type: 'webhook', config: {}, credentialRefs: { url: 'webhook' } } } }),
    'notification-endpoints': resource('notification-endpoints', 'notify', { type: 'webhook', config: {}, credentialRefs: { url: 'webhook' } }),
    recipients: resource('recipients', 'oncall', { endpointRefs: ['notify'] }),
    'notification-groups': resource('notification-groups', 'team', { recipientRefs: ['oncall'] }),
    credentials: resource('credentials', 'webhook', { description: 'Write-only fixture' }),
  };
  records.monitors.status = { applied: true, controlRevision: 'control-1', incidentID: 'incident-1', health: 'unhealthy' };
  records.credentials.status = { available: true };
  const incident = { id: 'incident-1', monitorID: 'checkout', revision: 'attention-1', state: 'open', dismissed: false };
  const action = { id: 'action-1', monitorID: 'checkout', incarnationUID: 'uid-checkout', reviewRevision: 'review-1', kind: 'intervention', state: 'unknown', held: true, executorFenced: true };
  const permissions = JSON.parse(fs.readFileSync(path.join(root, 'api/openapi/operations.json'))).filter(item => !item.buildTag).map(item => item.operationID);
  await page.route('**/*', async route => {
    const request = route.request(), url = new URL(request.url());
    if (url.origin !== origin) return route.abort();
    if (!url.pathname.startsWith('/api/')) return route.continue();
    const reply = (body, status = 200, headers = {}) => route.fulfill({ status, contentType: 'application/json', body: JSON.stringify(body), headers });
    const name = url.pathname;
    if (name === '/api/v2/access' && request.method() === 'GET') return reply({ mode: 'management', authenticationRequired: true });
    if (name === '/api/v2/self') return request.headers().authorization === `Bearer ${token}` ? reply({ principalId: 'frontend-fixture', role: 'operator', permissions }) : reply({}, 401);
    if (name === '/api/v2/discovery') return reply({ apiVersions: ['cpra.io/v2'], drivers: { check: ['http', 'tcp'], recovery: ['webhook'], notification: ['webhook'] }, resources: [], patchTypes: ['application/merge-patch+json'] });
    if (request.method() !== 'GET') {
      if (name === '/api/v2/monitors/checkout' && request.method() === 'PATCH') {
        report.keyboardChecks.push({ check: 'conditional-disable-request', passed: request.headers()['if-match'] === '"rv-1"' });
        await new Promise(resolve => { finishPending = resolve; pendingArrived(); });
        return reply({ code: 'conflict', title: 'Conflict', status: 412 }, 412, { 'X-Operation-ID': 'fixture-operation' });
      }
      errors.push('Unexpected fixture mutation');
      return reply({}, 500);
    }
    if (name === '/api/v1/overview') return reply({ generated: new Date().toISOString(), total: 1, disabled: 0, by_status: { down: 1 }, by_pulse_type: { tcp: 1 }, by_code: {}, up_percent: 0, index_capped: false });
    for (const [type, value] of Object.entries(records)) {
      if (name === `/api/v2/${type}`) return reply({ items: [value], snapshot: 'fixture-page' });
      if (name === `/api/v2/${type}/${value.metadata.id}`) return reply(value);
    }
    if (name === '/api/v2/incidents/incident-1') return reply(incident);
    if (name === '/api/v2/actions') return reply({ items: [action] });
    if (name === '/api/v2/actions/action-1') return reply(action);
    if (name.endsWith('/history')) return reply({ items: [] });
    return reply({}, 404);
  });
  timer = setTimeout(() => { report.error = 'Frontend campaign exceeded its two-minute budget'; void browser.close(); }, 120_000);
  async function visit(route) {
    await page.goto(origin + route);
    await page.getByRole('heading', { name: 'Sign in to CPRa' }).waitFor();
    await page.getByLabel('Bearer token').fill(token);
    await page.getByRole('button', { name: 'Sign in', exact: true }).click();
    await page.getByRole('button', { name: 'Sign out' }).waitFor();
  }
  await page.goto(origin + '/monitor-configurations');
  await page.getByRole('heading', { name: 'Sign in to CPRa' }).waitFor();
  await auditAccessibility(page, 'sign-in', report.accessibility);
  await page.getByLabel('Bearer token').fill(token);
  await page.getByRole('button', { name: 'Sign in', exact: true }).click();
  await page.getByRole('button', { name: 'Sign out' }).waitFor();
  for (let presses = 0; presses < 30; presses++) {
    await page.keyboard.press('Tab');
    if (await page.getByRole('link', { name: 'Skip to main content' }).evaluate(element => element === document.activeElement)) break;
  }
  check(await page.getByRole('link', { name: 'Skip to main content' }).evaluate(element => element === document.activeElement), 'Keyboard could not reach skip link');
  await page.keyboard.press('Enter');
  check(await page.getByRole('main').evaluate(element => element === document.activeElement), 'Skip link did not focus main content');
  report.keyboardChecks.push({ check: 'skip-to-main-content', passed: true });

  async function modal(label, locator, pending = false) {
    await locator.waitFor();
    check(await locator.evaluate(element => element.matches(':modal')), `${label} is not a native modal`);
    const background = page.getByRole('button', { name: 'Sign out' });
    await background.evaluate(element => element.focus());
    check(await locator.evaluate(element => element.contains(document.activeElement)), `${label} allowed background focus`);
    for (let i = 0; i < 12; i++) {
      await page.keyboard.press(i % 3 === 0 ? 'Shift+Tab' : 'Tab');
      check(await locator.evaluate(element => element.contains(document.activeElement)), `${label} lost keyboard focus`);
    }
    await auditAccessibility(page, label, report.accessibility);
    report.keyboardChecks.push({ check: `${label}-modality-and-tab`, passed: true });
    await page.keyboard.press('Escape');
    if (pending) check(await locator.isVisible(), 'Pending operation was hidden by Escape');
    else await locator.waitFor({ state: 'hidden' });
  }
  for (const [route, title] of [['monitor-configurations', 'monitor'], ['notification-endpoints', 'notification-endpoint'], ['recipients', 'recipient'], ['notification-groups', 'group'], ['secrets', 'secret']]) {
    await visit(`/${route}?create=1`);
    await page.getByLabel('Stable ID', { exact: true }).waitFor();
    if (route === 'secrets') await page.getByLabel('New secret value').fill(secret);
    await auditAccessibility(page, `${title}-editor`, report.accessibility);
  }
  await visit('/monitor-configurations/checkout');
  await page.getByRole('button', { name: 'Delete monitor' }).click();
  await modal('delete-confirmation', page.getByRole('alertdialog'));
  await page.getByRole('button', { name: 'Edit monitor' }).click();
  await page.getByLabel('Name', { exact: true }).fill('Unsaved accessibility draft');
  await page.getByRole('link', { name: 'Recipients', exact: true }).click();
  await modal('discard-draft', page.getByRole('alertdialog'));
  check(await page.getByLabel('Name', { exact: true }).inputValue() === 'Unsaved accessibility draft', 'Escape discarded the unsaved draft');

  page.once('dialog', dialog => dialog.accept());
  await visit('/monitors/by-id/checkout');
  const controls = page.getByRole('region', { name: 'Monitor controls', exact: true });
  for (const name of ['Acknowledge', 'Dismiss', 'Snooze', 'Request recovery']) {
    await controls.getByRole('button', { name, exact: true }).click();
    await modal(name.toLowerCase().replaceAll(' ', '-'), page.getByRole('dialog', { name, exact: true }));
  }
  await controls.getByRole('button', { name: 'Disable', exact: true }).click();
  const disable = page.getByRole('dialog', { name: 'Disable', exact: true });
  await disable.getByRole('button', { name: 'Disable', exact: true }).click();
  await Promise.race([pendingRequest, new Promise((_, reject) => setTimeout(() => reject(new Error('Pending fixture request did not arrive')), 8000))]);
  await disable.locator('[disabled]').first().waitFor();
  await page.getByRole('button', { name: 'Sign out' }).evaluate(element => element.focus());
  check(await disable.evaluate(element => element.contains(document.activeElement)), 'Pending write lost focus or allowed background focus');
  for (let i = 0; i < 4; i++) {
    await page.keyboard.press(i % 2 === 0 ? 'Tab' : 'Shift+Tab');
    check(await disable.evaluate(element => element.contains(document.activeElement)), 'Pending write lost Tab focus');
  }
  await page.keyboard.press('Escape');
  check(await disable.isVisible(), 'Escape hid a pending conditional write');
  report.keyboardChecks.push({ check: 'pending-write-focus-and-escape', passed: true });
  finishPending();
  await disable.getByRole('alert').waitFor();
  await modal('control-conflict', disable);
  await page.getByRole('button', { name: 'Review action action-1', exact: true }).click();
  await modal('unknown-action-review', page.getByRole('dialog', { name: 'Review unknown action', exact: true }));
  await page.setViewportSize({ width: 390, height: 844 });
  await controls.getByRole('button', { name: 'Snooze', exact: true }).click();
  await modal('mobile-snooze', page.getByRole('dialog', { name: 'Snooze', exact: true }));
  check(errors.length === 0, 'Uncaught frontend errors');
  check(unchanged(), 'Frontend inputs changed during verification');
  check(![token, secret].some(value => JSON.stringify(report).includes(value)), 'Private fixture value entered accessibility evidence');
  report.inputsUnchanged = true;
  report.result = 'passed';
})().catch(error => {
  report.result = 'failed';
  report.error = String(error.message).replaceAll(token, '[redacted]').replaceAll(secret, '[redacted]').slice(0, 1000);
}).finally(async () => {
  clearTimeout(timer);
  if (browser) await browser.close();
  if (server) await new Promise(resolve => server.close(resolve));
  save();
  console.log(JSON.stringify({ result: report.result, report: path.join(out, 'result.json'), scans: report.accessibility.length, error: report.error }));
  process.exitCode = report.result === 'passed' ? 0 : 1;
});

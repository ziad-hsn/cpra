// Private integration fixture. Uses an existing browser; never installs packages.
// All API responses come from actual Go handlers and the real encrypted catalog.
'use strict';
const { chromium } = require(process.env.CPRA_BROWSER_MODULE);
const playwrightVersion = require(`${process.env.CPRA_BROWSER_MODULE}/package.json`).version;
const origin = process.env.CPRA_BROWSER_ORIGIN;
const operator = 'operator-fixture-012345678901234567890123456';
const reader = 'reader-fixture-012345678901234567890123456789';
const secret = 'https://example.test/browser-fixture-private-value';
const checks = [];
const record = name => checks.push(name);
function check(condition, label) { if (!condition) throw new Error(label); }

(async () => {
  const browser = await chromium.launch({ headless: true, executablePath: process.env.CPRA_BROWSER_EXECUTABLE,
    args: [`--ignore-certificate-errors-spki-list=${process.env.CPRA_BROWSER_CERT_PIN}`] });
  try {
    const context = await browser.newContext({ viewport: { width: 1440, height: 1000 } });
    const page = await context.newPage();
    page.setDefaultTimeout(15_000);
    const readChecks = [], errors = [], writes = [];
    page.on('pageerror', error => errors.push([secret, operator, reader].some(value => error.message.includes(value)) ? 'private value in browser error' : error.message.slice(0, 1000)));
    page.on('request', request => {
      check(!request.url().includes(secret) && !request.url().includes(operator) && !request.url().includes(reader), 'secret or token in URL');
      if (!['GET', 'HEAD'].includes(request.method())) writes.push({ method: request.method(), path: new URL(request.url()).pathname });
    });
    page.on('response', response => {
      if (response.request().method() === 'GET' && new URL(response.url()).pathname.startsWith('/api/')) {
        readChecks.push(response.text().then(body => check(!body.includes(secret), 'secret in GET response')).catch(error => {
          if (error.message === 'secret in GET response') throw error;
          // Identity changes can abort old reads; this is not a successful read.
        }));
      }
    });
    async function signIn(token = operator) {
      await page.getByRole('heading', { name: 'Sign in to CPRa' }).waitFor();
      await page.getByLabel('Bearer token').fill(token);
      await page.getByRole('button', { name: 'Sign in', exact: true }).click();
      await page.getByRole('button', { name: 'Sign out', exact: true }).waitFor();
    }
    async function visit(path, token = operator) { await page.goto(origin + path); await signIn(token); }
    async function saved() {
      try { await page.getByText('Saved durably. Controller application has not yet been confirmed.', { exact: true }).waitFor(); }
      catch { throw new Error(`Save confirmation missing after checkpoint ${checks.length}: ${JSON.stringify({ alerts: await page.getByRole('alert').allTextContents(), status: await page.getByRole('status').allTextContents() })}`); }
      const link = page.getByRole('link', { name: 'View operation progress', exact: true });
      await link.waitFor();
      return link.getAttribute('href');
    }
    async function committed() {
      try { await page.getByRole('heading', { name: 'Committed', exact: true }).waitFor(); }
      catch { throw new Error(`Committed receipt missing after checkpoint ${checks.length}: ${JSON.stringify({ path: new URL(page.url()).pathname, alerts: await page.getByRole('alert').allTextContents(), status: await page.getByRole('status').allTextContents(), headings: await page.getByRole('heading').allTextContents() })}`); }
    }
    async function api(method, path, data, version, token = operator) {
      // Browser-origin fetch uses the same TLS pin and real server; no mocks.
      return page.evaluate(async ({ method, path, data, version, token }) => {
        const response = await fetch(path, { method, headers: { Authorization: `Bearer ${token}`,
          ...(data ? { 'Content-Type': 'application/json' } : {}), ...(version ? { 'If-Match': `"${version}"` } : {}) },
          ...(data ? { body: JSON.stringify(data) } : {}) });
        return { status: response.status, body: await response.json() };
      }, { method, path, data, version, token });
    }
    await visit('/secrets?create=1');
    await page.getByLabel('Stable ID', { exact: true }).fill('browser-secret');
    await page.getByLabel('New secret value', { exact: true }).fill(secret);
    await page.getByRole('button', { name: 'Save', exact: true }).click();
    const secretOperation = await saved();
    check(await page.getByLabel('New secret value').count() === 0, 'secret input not removed after submission');
    check(!(await page.content()).includes(secret), 'secret remains in DOM');
    const credential = await api('GET', '/api/v2/credentials/browser-secret');
    check(credential.status === 200 && !Object.hasOwn(credential.body.spec, 'value'), 'credential response was not write-only');
    check(credential.body.status.available === true, 'credential availability missing');
    record('write-only credential creation and redacted reads');

    await visit('/secrets/browser-secret');
    await page.getByRole('button', { name: 'Edit secret', exact: true }).click();
    check(await page.getByLabel('New secret value').count() === 0, 'existing credential was repopulated');
    await page.getByLabel('Description', { exact: true }).fill('Description only; keep the existing value');
    await page.getByRole('button', { name: 'Save', exact: true }).click();
    await saved();
    const editedCredential = await api('GET', '/api/v2/credentials/browser-secret');
    check(editedCredential.body.status.available === true && !Object.hasOwn(editedCredential.body.spec, 'value'), 'credential metadata edit lost or disclosed value');
    record('conditional credential patch preserves its write-only value');

    await visit('/notification-endpoints?create=1');
    await page.getByLabel('Stable ID', { exact: true }).fill('browser-log');
    await page.getByLabel('Notification driver').selectOption('webhook');
    await page.getByLabel('Specific url secret ID').fill('browser-secret');
    await page.getByRole('group', { name: 'Url secret', exact: true }).getByRole('button', { name: 'Use ID', exact: true }).click();
    await page.getByRole('button', { name: 'Save', exact: true }).click();
    await saved();
    await visit('/recipients?create=1');
    await page.getByLabel('Stable ID', { exact: true }).fill('browser-person');
    await page.getByLabel('Specific endpoint references ID').fill('browser-log');
    await page.getByRole('group', { name: 'Endpoint references', exact: true }).getByRole('button', { name: 'Use ID', exact: true }).click();
    await page.getByRole('button', { name: 'Save', exact: true }).click();
    await saved();
    await visit('/notification-groups?create=1');
    await page.getByLabel('Stable ID', { exact: true }).fill('browser-team');
    await page.getByLabel('Specific recipient references ID').fill('browser-person');
    await page.getByRole('group', { name: 'Recipient references', exact: true }).getByRole('button', { name: 'Use ID', exact: true }).click();
    await page.getByRole('button', { name: 'Save', exact: true }).click();
    await saved();
    record('notification endpoint with secret reference, recipient and group creation');

    await visit('/monitor-configurations?create=1');
    await page.getByLabel('Stable ID', { exact: true }).fill('browser-monitor');
    await page.getByLabel('Check driver').selectOption('tcp');
    await page.getByLabel('Host', { exact: true }).fill('127.0.0.1');
    await page.getByLabel('Port', { exact: true }).fill('9');
    await page.getByLabel('Enabled', { exact: true }).selectOption('false');
    await page.getByLabel('Add Code', { exact: true }).selectOption('red');
    await page.getByLabel('Code red notification type').selectOption('webhook');
    await page.getByLabel('Specific code red group ID').fill('browser-team');
    await page.getByRole('group', { name: 'Code red group', exact: true }).getByRole('button', { name: 'Use ID', exact: true }).click();
    await page.getByRole('button', { name: 'Save', exact: true }).click();
    const monitorOperation = await saved();
    const monitor = await api('GET', '/api/v2/monitors/browser-monitor');
    check(monitor.status === 200 && monitor.body.spec.enabled === false && monitor.body.spec.notifications.red.notifyType === 'webhook', 'monitor write shape differs');
    record('guided disabled monitor and actual Code driver selection');

    await page.getByRole('link', { name: 'View operation progress', exact: true }).click();
    await committed();
    check(!(await page.getByRole('region', { name: 'Operation receipt' }).textContent()).includes('applied by the controller'), 'fixture falsely claims controller application');
    await page.reload();
    await signIn();
    check(new URL(page.url()).pathname === monitorOperation, 'receipt route not preserved through refresh sign-in');
    await committed();
    record('committed operation receipt and reload sign-in reconciliation');

    await visit('/monitor-configurations/browser-monitor');
    await page.getByRole('button', { name: 'Edit monitor', exact: true }).click();
    await page.getByLabel('Name', { exact: true }).fill('My isolated draft');
    const concurrent = structuredClone(monitor.body);
    concurrent.metadata.name = 'Another operator';
    delete concurrent.status;
    const replaced = await api('PUT', '/api/v2/monitors/browser-monitor', concurrent, monitor.body.metadata.resourceVersion);
    check(replaced.status === 200, 'concurrent real update failed');
    const countBefore = writes.length;
    await page.getByRole('button', { name: 'Save', exact: true }).click();
    await page.getByRole('button', { name: 'Compare latest resource', exact: true }).waitFor();
    check(await page.getByLabel('Name', { exact: true }).inputValue() === 'My isolated draft', 'conflict overwrote draft');
    await page.getByRole('button', { name: 'Compare latest resource', exact: true }).click();
    await page.getByRole('region', { name: 'Latest resource comparison' }).waitFor();
    check(writes.length === countBefore + 1, 'conflicting mutation retried automatically');
    page.once('dialog', dialog => dialog.accept());
    await page.getByRole('button', { name: 'Cancel editing', exact: true }).click();
    record('strong version conflict with retained isolated draft and no write retry');

    await visit('/notification-endpoints/browser-log');
    await page.getByRole('button', { name: 'Delete notification endpoint', exact: true }).click();
    await page.getByRole('button', { name: 'Confirm deletion', exact: true }).click();
    await page.getByRole('alert').filter({ hasText: 'reference prevents' }).waitFor();
    check((await api('GET', '/api/v2/notification-endpoints/browser-log')).status === 200, 'referenced endpoint deleted');
    record('referenced endpoint deletion rejected by real API');

    await visit('/monitor-configurations/browser-monitor', reader);
    await page.getByRole('heading', { name: 'Another operator', exact: true }).waitFor();
    check(await page.getByRole('button', { name: /^(Edit|Delete|Save|Check now)/ }).count() === 0, 'reader mutation controls exposed');
    const denied = await api('DELETE', '/api/v2/monitors/browser-monitor', undefined, replaced.body.metadata.resourceVersion, reader);
    check(denied.status === 403, 'reader write not rejected by server');
    await page.getByRole('button', { name: 'Sign out', exact: true }).click();
    await page.getByRole('heading', { name: 'Sign in to CPRa' }).waitFor();
    check(await page.getByRole('heading', { name: 'Another operator', exact: true }).count() === 0, 'old resource remains after sign-out');
    await signIn();
    await page.getByRole('button', { name: 'Delete monitor', exact: true }).click();
    await page.getByRole('button', { name: 'Confirm deletion', exact: true }).click();
    await saved();
    check((await api('GET', '/api/v2/monitors/browser-monitor')).status === 404, 'monitor delete not committed');
    record('reader denied writes, identity replacement, and conditional monitor deletion');

    await visit(secretOperation);
    await page.getByRole('heading', { name: 'Partial or superseded', exact: true }).waitFor();
    await page.getByText('This operation was superseded before application was confirmed. Inspect the current resource and retained history before deciding what to change.', { exact: true }).waitFor();
    record('earlier credential receipt remains readable as superseded');
    const storage = await page.evaluate(() => JSON.stringify({ local: { ...localStorage }, session: { ...sessionStorage }, cookie: document.cookie }));
    check(![secret, operator, reader].some(value => storage.includes(value)), 'secret or token persisted in browser storage');
    await Promise.all(readChecks);
    check(errors.length === 0, `uncaught browser errors occurred: ${JSON.stringify(errors)}`);
    record('tokens absent from browser storage, secret-free GETs and URLs, no page errors');
    console.log(JSON.stringify({ browser: browser.version(), node: process.version, playwright: playwrightVersion,
      scope: 'real TLS API + Raft catalog; no main/controller/provider execution', checks }, null, 2));
    await context.close();
  } finally { await browser.close(); }
})().catch(error => { console.error(error.message); process.exitCode = 1; });

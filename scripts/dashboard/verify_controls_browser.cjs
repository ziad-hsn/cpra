// Uses the embedded dashboard served by normal runCPRa startup. No route mocks.
'use strict';
const { chromium } = require(process.env.CPRA_BROWSER_MODULE);
const origin = process.env.CPRA_BROWSER_ORIGIN;
const operator = 'main-operator-fixture-012345678901234567890123456789';
const reader = 'main-reader-fixture-012345678901234567890123456789';
const checks = [];
function check(value, message) { if (!value) throw new Error(message); }

(async () => {
  const browser = await chromium.launch({ headless: true, executablePath: process.env.CPRA_BROWSER_EXECUTABLE,
    args: [`--ignore-certificate-errors-spki-list=${process.env.CPRA_BROWSER_CERT_PIN}`] });
  try {
    const context = await browser.newContext({ viewport: { width: 1440, height: 1080 } });
    const page = await context.newPage();
    page.setDefaultTimeout(20_000);
    const errors = [];
    page.on('pageerror', error => errors.push(error.message));
    const writes = [];
    page.on('request', request => { if (request.url().startsWith(origin + '/api/v2/') && request.method() !== 'GET') writes.push({ method: request.method(), path: new URL(request.url()).pathname }); });
    async function signIn(token = operator) {
      await page.getByRole('heading', { name: 'Sign in to CPRa' }).waitFor();
      await page.getByLabel('Bearer token').fill(token);
      await page.getByRole('button', { name: 'Sign in', exact: true }).click();
      await page.getByRole('button', { name: 'Sign out', exact: true }).waitFor();
    }
    async function api(path) {
      return page.evaluate(async ({ path, token }) => {
        const response = await fetch(path, { headers: { Authorization: `Bearer ${token}` } });
        if (!response.ok) throw new Error(`Observation failed: ${response.status}`);
        return response.json();
      }, { path, token: operator });
    }
    async function targetCount() {
      const response = await fetch(process.env.CPRA_BROWSER_TARGET + '/count');
      check(response.ok, 'target-side accounting unavailable');
      return response.json();
    }
    async function applied() {
      await page.getByText('Saved durably and applied by the controller.', { exact: true }).waitFor();
    }
    async function control(label, fields = {}) {
      await page.getByRole('button', { name: label, exact: true }).click();
      const dialog = page.getByRole('dialog', { name: label, exact: true });
      for (const [field, value] of Object.entries(fields)) await dialog.getByLabel(field, { exact: field !== 'Duration' }).fill(value);
      await dialog.getByRole('button', { name: label, exact: true }).click();
      await dialog.waitFor({ state: 'hidden' });
      await applied();
    }
    await page.goto(origin + '/monitors');
    await signIn();
    await page.getByText('Browser controls', { exact: true }).click();
    await page.getByRole('heading', { name: 'Monitor controls' }).waitFor();
    check(await page.getByRole('button', { name: /check now/i }).count() === 0, 'check-now unexpectedly exposed');
    await control('Acknowledge', { 'Note (optional)': 'I am investigating the test service' });
    await page.getByText(/Acknowledged by team\/oncall/).waitFor();
    await page.getByRole('cell', { name: 'I am investigating the test service', exact: true }).waitFor();
    checks.push('acknowledgement records named operator and timeline note while checks continue');

    await control('Dismiss', { Reason: 'Known isolated test failure' });
    await page.getByText('Notifications dismissed for this incident. Checks and recovery continue.', { exact: true }).waitFor();
    await control('Reopen notifications');
    await page.getByRole('button', { name: 'Dismiss', exact: true }).waitFor();
    checks.push('exact incident dismissal and reopening with durable owner receipts');

    await control('Snooze', { Reason: 'Controlled maintenance', Duration: '30m' });
    const paused = await targetCount();
    await page.waitForTimeout(700);
    const stillPaused = await targetCount();
    check(Number.isInteger(paused) && paused === stillPaused, 'checks continued after applied snooze');
    await control('Disable');
    await control('End snooze');
    const disabled = await api('/api/v2/monitors/browser-controls');
    check(disabled.spec.enabled === false && (!disabled.status.snoozedUntil || Date.parse(disabled.status.snoozedUntil) <= Date.now()), 'unsnooze changed disabled state');
    checks.push('applied snooze pauses actual checks; unsnooze preserves disablement');

    await control('Enable');
    let resumed = false;
    for (let n = 0; n < 30; n++) {
      const observed = await targetCount();
      if (observed > stillPaused) { resumed = true; break; }
      await page.waitForTimeout(100);
    }
    check(resumed, 'fresh checks did not resume after enabling');
    checks.push('conditional enable resumes actual HTTP checks');

    await page.getByRole('button', { name: 'Acknowledge', exact: true }).click();
    await page.getByLabel('Note (optional)', { exact: true }).fill('Discard this private browser draft');
    await page.goBack();
    await page.getByRole('heading', { name: 'Monitor Fleet', exact: true }).waitFor();
    await page.getByText('Browser controls', { exact: true }).click();
    await page.getByRole('heading', { name: 'Monitor controls' }).waitFor();
    check(await page.getByRole('dialog').count() === 0, 'navigation retained previous control draft');
    checks.push('navigation discards the previous monitor control draft');

    await page.reload();
    await signIn(reader);
    await page.getByRole('heading', { name: 'Monitor controls' }).waitFor();
    check(await page.getByRole('button', { name: /^(Acknowledge|Dismiss|Reopen notifications|Snooze|End snooze|Disable|Enable)$/ }).count() === 0, 'reader sees control writes');
    const storage = await page.evaluate(() => JSON.stringify({ local: { ...localStorage }, session: { ...sessionStorage }, cookie: document.cookie }));
    check(!storage.includes(operator) && !storage.includes(reader), 'token persisted in browser storage');
    check(errors.length === 0, `uncaught browser errors: ${JSON.stringify(errors)}`);
    check(writes.length === 7, `expected seven explicit control writes, got ${writes.length}`);
    checks.push('reader remains observational; no token storage, hidden retries or page errors');
    console.log(JSON.stringify({ scope: 'normal main startup, embedded SPA, TLS, encrypted Raft, owner loop, actual local HTTP target', browser: browser.version(), node: process.version, playwright: require(`${process.env.CPRA_BROWSER_MODULE}/package.json`).version, checks, writes }, null, 2));
    await context.close();
  } finally { await browser.close(); }
})().catch(error => { console.error(error.message); process.exitCode = 1; });

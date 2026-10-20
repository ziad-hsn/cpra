'use strict';
const { chromium } = require(process.env.CPRA_BROWSER_MODULE);
const path = require('node:path');
const { auditAccessibility } = require('./accessibility.cjs');
const origin = process.env.CPRA_BROWSER_ORIGIN;
const operator = 'main-operator-fixture-012345678901234567890123456789';
function check(value, message) { if (!value) throw new Error(message); }

(async () => {
  const browser = await chromium.launch({ headless: true, executablePath: process.env.CPRA_BROWSER_EXECUTABLE,
    args: [`--ignore-certificate-errors-spki-list=${process.env.CPRA_BROWSER_CERT_PIN}`] });
  try {
    const context = await browser.newContext({ viewport: { width: 1440, height: 1080 }, timezoneId: 'Africa/Cairo' });
    const page = await context.newPage();
    page.setDefaultTimeout(20_000);
    const errors = [], writes = [], accessibility = [], checkpoints = [];
    page.on('pageerror', error => errors.push(error.message));
    page.on('request', request => {
      const url = new URL(request.url());
      check(!url.search.includes(operator), 'browser sent token in URL');
      if (url.origin === origin && url.pathname.startsWith('/api/') && request.method() !== 'GET') writes.push(url.pathname);
    });
    async function signIn() {
      await page.getByRole('heading', { name: 'Sign in to CPRa' }).waitFor();
      await page.getByLabel('Bearer token').fill(operator);
      await page.getByRole('button', { name: 'Sign in', exact: true }).click();
      await page.getByRole('button', { name: 'Sign out', exact: true }).waitFor();
    }
    async function navigate(pathname) {
      await page.goto(origin + pathname);
      // Bearers deliberately live only in the current page's memory.
      await signIn();
    }
    async function api(resource) {
      return page.evaluate(async ({ resource, token }) => {
        const response = await fetch(resource, { headers: { Authorization: `Bearer ${token}` } });
        if (!response.ok) throw new Error(`Observation failed: ${response.status}`);
        return response.json();
      }, { resource, token: operator });
    }
    async function waitActions(monitor, predicate) {
      for (let n = 0; n < 150; n++) {
        const value = await api('/api/v2/actions?monitorID=' + monitor + '&limit=100');
        if (predicate(value.items)) return value.items;
        await page.waitForTimeout(100);
      }
      throw new Error('Expected committed action observations did not arrive for ' + monitor);
    }
    async function counts() {
      const response = await fetch(process.env.CPRA_BROWSER_TARGET + '/count');
      check(response.ok, 'receiver accounting unavailable');
      return response.json();
    }
    async function health(healthy) {
      const response = await fetch(process.env.CPRA_BROWSER_TARGET + '/mode?healthy=' + healthy, { method: 'POST' });
      check(response.ok, 'receiver control unavailable');
    }
    async function save(name) {
      await page.screenshot({ path: path.join(process.env.CPRA_BROWSER_EVIDENCE_DIR, name), fullPage: true });
    }
    await navigate('/');
    const monitors = ['demo-review-success', 'demo-review-failed-close'];
    const original = new Map();
    for (const monitor of monitors) {
      const items = await waitActions(monitor, rows => rows.some(a => a.reviewNotice?.phase === 'open' && a.state === 'succeeded'));
      const recovery = items.find(a => a.kind === 'intervention');
      check(recovery?.state === 'unknown' && recovery.held, 'HTTP 500 recovery was not held as unknown');
      original.set(monitor, recovery.id);
    }
    check((await counts()).recoveries === 2, 'unknown recovery repeated');
    await page.getByRole('heading', { name: 'Fleet Overview', exact: true }).waitFor();
    await page.getByText('Latest health snapshot for 8 monitors', { exact: true }).first().waitFor();
    await save('dashboard-populated-demo.png');
    await auditAccessibility(page, 'populated-overview', accessibility);
    await navigate('/monitors/by-id/' + monitors[0]);
    const actions = page.getByRole('region', { name: 'Action outcomes', exact: true });
    await actions.getByText('Recovery outcome unknown — operator review required.', { exact: true }).waitFor();
    await save('review-blue-opening.png');
    checkpoints.push('real uncertain recovery opens explicitly configured blue; both unknown effects are invoked once');

    await health(true);
    for (const monitor of monitors) {
      const expected = monitor.endsWith('failed-close') ? 'unknown' : 'succeeded';
      const items = await waitActions(monitor, rows => rows.some(a => a.reviewNotice?.phase === 'close' && a.state === expected));
      check(items.find(a => a.id === original.get(monitor))?.held, 'health recovery released original action hold');
      const close = items.find(a => a.reviewNotice?.phase === 'close');
      check(close.reviewNotice.health === 'healthy', 'closure did not retain measured health');
      const open = items.find(a => a.reviewNotice?.phase === 'open');
      check(close.reviewNotice.alertID === open.reviewNotice.alertID && close.endpointID === open.endpointID, 'closure changed provider condition/destination');
    }
    await actions.getByRole('button', { name: 'Refresh actions', exact: true }).click();
    await actions.getByText('Health recovered earlier; the recovery action outcome remains unknown.', { exact: true }).waitFor();
    await actions.getByText('Health at this notice: healthy.', { exact: true }).waitFor();
    await actions.getByText('Held for investigation', { exact: false }).waitFor();
    await save('review-blue-health-closure.png');
    await navigate('/monitors/by-id/' + monitors[1]);
    await actions.getByText('Health recovered earlier; the recovery action outcome remains unknown.', { exact: true }).waitFor();
    check(!((await actions.textContent()).includes('provider resolved')), 'failed closure was displayed as provider resolution');
    await save('review-blue-unknown-closure.png');
    checkpoints.push('verified health closes blue condition while original recovery remains held; failed closure is an unknown delivery');

    await navigate('/alerts');
    const incidentPage = await api('/api/v2/incidents?limit=100');
    for (const monitor of monitors) {
      const incident = incidentPage.items.find(item => item.monitorID === monitor);
      check(incident?.state === 'closed' && incident.notifications, 'closed incident did not retain actual delivery counts');
      const expected = monitor.endsWith('failed-close') ? {accepted: 1, unknown: 1} : {accepted: 2, unknown: 0};
      check(incident.notifications.accepted === expected.accepted && incident.notifications.unknown === expected.unknown, 'incident counts disagree with independently received blue deliveries');
      const row = page.getByRole('region', {name: 'Latest incident page', exact: true}).getByRole('row').filter({has: page.getByRole('link', {name: monitor, exact: true})});
      await row.waitFor();
      const counts = incident.notifications;
      check(await row.getByRole('cell').nth(7).innerText() === `${counts.pending} pending · ${counts.accepted} accepted · ${counts.failed} failed · ${counts.unknown} unknown · ${counts.cancelled} cancelled`, 'incident notification column differs from committed outcomes');
      for (const field of ['firstFailureAt', 'openedAt', 'closedAt']) check(incident[field]?.endsWith('Z') && !incident[field].startsWith('0001-'), 'incident wire time is unavailable or not UTC');
    }
    const displayedTime = page.getByRole('region', {name: 'Latest incidents', exact: true}).locator('time');
    const instant = await displayedTime.getAttribute('datetime');
    const zone = await page.evaluate(value => new Intl.DateTimeFormat('en-US', {timeZoneName: 'short'}).formatToParts(new Date(value)).find(part => part.type === 'timeZoneName').value, instant);
    check(zone !== 'UTC' && (await displayedTime.innerText()).includes(zone), 'dashboard failed to label local browser time separately from UTC wire time');
    checkpoints.push('incident notification counts match committed deliveries and receiver effects; UTC wire timestamps are presented in the browser timezone');

    await health(false);
    for (const monitor of monitors) {
      await waitActions(monitor, rows => rows.filter(a => a.reviewNotice?.phase === 'open' && a.state === 'succeeded').length === 2);
    }
    const accounting = await counts();
    check(accounting.recoveries === 2 && accounting.notices.length === 6, 'later outage replayed an effect or closure');
    check(accounting.notices.every(notice => notice.color === 'blue' && notice.action_id && notice.delivery_id && notice.endpoint_id && notice.review?.action_id), 'network envelope lost blue identity');
    checkpoints.push('later qualifying outage opens a separate blue condition without repeating an unknown recovery or failed closure');

    await navigate('/');
    await page.getByRole('heading', { name: 'Fleet Overview', exact: true }).waitFor();
    await context.setOffline(true);
    await page.getByText(/Saved fleet observations are shown below\. Current health is unavailable while offline/).waitFor();
    const beforeOfflineWrites = writes.length;
    // Settings has not been visited: its lazy module must fail safely offline.
    await page.getByRole('link', { name: 'Settings', exact: true }).click();
    await page.getByRole('heading', { name: 'You are offline', exact: true }).waitFor();
    check(!/Unexpected Application Error|TypeError:|\bat \w+ \(/.test(await page.locator('body').innerText()), 'route failure exposed stack details');
    await save('offline-route.png');
    await context.setOffline(false);
    await page.getByRole('button', { name: 'Reload dashboard', exact: true }).click();
    await signIn();
    await navigate('/missing-review-fixture-route');
    await page.getByRole('heading', { name: 'Page not found', exact: true }).waitFor();
    check(writes.length === beforeOfflineWrites, 'reconnection retried a mutation');
    checkpoints.push('cached observations report offline unavailability; uncached route fails safely; reconnect and missing routes remain usable');

    await page.setViewportSize({ width: 390, height: 844 });
    await navigate('/monitors/by-id/' + monitors[1]);
    await actions.getByRole('button', { name: 'Refresh actions', exact: true }).waitFor();
    const widths = await page.evaluate(() => ({ viewport: innerWidth, document: document.documentElement.scrollWidth, body: document.body.scrollWidth }));
    check(widths.document <= widths.viewport + 1 && widths.body <= widths.viewport + 1, 'blue action detail overflows mobile viewport');
    await auditAccessibility(page, 'mobile-blue-actions', accessibility);
    await save('review-blue-mobile.png');
    check(writes.length === 0, 'observation scenario sent a management write');
    const storage = await page.evaluate(() => JSON.stringify({ local: { ...localStorage }, session: { ...sessionStorage }, cookie: document.cookie }));
    check(!storage.includes(operator) && !(await page.locator('body').innerText()).includes(operator), 'token exposed in DOM or storage');
    check(errors.length === 0, 'unhandled browser error: ' + JSON.stringify(errors));
    console.log(JSON.stringify({ scope: 'real-server local browser fixtures; not provider-account or native-platform qualification', checkpoints, receiver: { recoveries: accounting.recoveries, notifications: accounting.notices.length, checks: accounting.checks }, accessibility, browser: browser.version() }, null, 2));
    await context.close();
  } finally { await browser.close(); }
})().catch(error => { console.error(error.message); process.exitCode = 1; });

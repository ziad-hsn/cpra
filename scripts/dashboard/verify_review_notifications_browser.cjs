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
    async function waitObservation(monitor, predicate) {
      for (let n = 0; n < 150; n++) {
        const value = await api('/api/v2/observations/monitors/' + monitor);
        if (predicate(value)) return value;
        await page.waitForTimeout(100);
      }
      throw new Error('Expected current monitor availability did not arrive for ' + monitor);
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
    await page.getByText('Latest health snapshot for 9 monitors', { exact: true }).first().waitFor();
    const maintenance = await waitObservation('demo-inventory', row => row.maintenance && row.health === 'unhealthy');
    check(maintenance.notificationsSuppressed, 'maintenance did not suppress ordinary notifications');
    const fleet = await api('/api/v1/overview');
    check(fleet.maintenance === 1 && fleet.suppressed_unhealthy === 1 && fleet.unsuppressed_unhealthy === 2, 'maintenance was counted as unsuppressed critical health');
    await page.getByText('1 in maintenance; health checks continue.', { exact: true }).waitFor();
    await save('dashboard-populated-demo.png');
    await auditAccessibility(page, 'populated-overview', accessibility);

    const beforePreparation = await waitObservation('demo-preparation', row => row.health === 'healthy' && row.availability.state === 'available');
    const removedCA = await fetch(process.env.CPRA_BROWSER_TARGET + '/remove-ca', { method: 'POST' });
    check(removedCA.ok, 'disposable CA fixture removal failed');
    const unavailable = await waitObservation('demo-preparation', row => row.health === 'unknown' && row.availability.state === 'unavailable');
    check(unavailable.availability.reason === 'configurationProblem' && unavailable.configurationProblem?.code === 'ca_file_missing', 'CA preparation was reported as a remote outage');
    check(unavailable.lastObservedHealth === 'healthy' && unavailable.lastObservedAt === unavailable.lastCheckedAt, 'last target observation was overwritten by local preparation');
    check(Date.parse(unavailable.lastObservedAt) >= Date.parse(beforePreparation.lastObservedAt) && Date.parse(unavailable.availability.observedAt) > Date.parse(unavailable.lastObservedAt), 'preparation and target timestamps were conflated');
    check(unavailable.warnings?.some(warning => warning.reason === 'unknownRecoveryDestination'), 'missing blue destination warning absent from observation');
    check(!JSON.stringify(unavailable).includes('private-ca-target-marker'), 'credential-backed target path leaked through observation');
    const noIncident = await api('/api/v2/incidents?monitorID=demo-preparation&limit=100');
    check(noIncident.items.length === 0, 'local CA failure opened a service incident');
    await navigate('/monitors/by-id/demo-preparation');
    await page.getByText(/Current health is unknown because local preparation prevents checks/).waitFor();
    await page.getByText(/Uncertain recovery has no enabled blue notification destination/).waitFor();
    check(!(await page.locator('body').innerText()).includes('private-ca-target-marker'), 'credential-backed path exposed in dashboard');
    const preparationCounts = await counts();
    await page.waitForTimeout(600);
    const stillUnavailable = await api('/api/v2/observations/monitors/demo-preparation');
    check(stillUnavailable.lastObservedAt === unavailable.lastObservedAt && (await counts()).tlsChecks === preparationCounts.tlsChecks, 'local preparation produced fake target observations or contacted the target');
    check((await counts()).recoveries === 2, 'local configuration failure invoked recovery');
    await save('monitor-check-unavailable.png');
    checkpoints.push('missing CA changes current health to unknown, preserves the last healthy target observation and timestamps, admits no service incident or recovery, and conceals credential paths');

    await navigate('/monitors');
    await page.getByRole('table', { name: 'Monitors table', exact: true }).waitFor();
    async function rowsDoNotOverlap() {
      await page.waitForTimeout(200);
      const geometry = await page.locator('.vtable-row').evaluateAll(rows => rows.map(row => {
        const bounds = row.getBoundingClientRect();
        const cells = Array.from(row.querySelectorAll('[role=cell]'));
        const statusOnOneLine = Array.from(row.querySelectorAll('.status-chip .badge-label')).every(label => {
          const range = document.createRange();
          range.selectNodeContents(label);
          return range.getClientRects().length === 1;
        });
        // Monitor names intentionally ellipsize. Operational values and badge
        // labels must remain readable within their cell, including enlarged text.
        const textFits = cells.slice(1).every(cell => {
          const cellBounds = cell.getBoundingClientRect();
          const text = document.createTreeWalker(cell, NodeFilter.SHOW_TEXT);
          while (text.nextNode()) {
            if (!text.currentNode.textContent.trim()) continue;
            const range = document.createRange();
            range.selectNodeContents(text.currentNode);
            if (!Array.from(range.getClientRects()).every(rect => rect.left >= cellBounds.left - 1 && rect.right <= cellBounds.right + 1 && rect.top >= bounds.top - 1 && rect.bottom <= bounds.bottom + 1)) return false;
          }
          return Array.from(cell.querySelectorAll('.badge-label')).every(label => {
            const badge = label.parentElement.getBoundingClientRect();
            const range = document.createRange();
            range.selectNodeContents(label);
            return Array.from(range.getClientRects()).every(rect => rect.left >= badge.left - 1 && rect.right <= badge.right + 1 && rect.top >= badge.top - 1 && rect.bottom <= badge.bottom + 1);
          });
        });
        return { top: bounds.top, bottom: bounds.bottom, measured: row.dataset.index !== undefined,
          textFits, statusOnOneLine, contentsFit: cells.every(cell => cell.getBoundingClientRect().bottom <= bounds.bottom + 1) };
      }).sort((a, b) => a.top - b.top));
      check(geometry.length >= 4 && geometry.every(row => row.measured && row.contentsFit), 'table rows were unmeasured or clipped their contents');
      check(geometry.every((row, index) => !index || row.top >= geometry[index - 1].bottom - 1), 'variable-height table rows overlap');
      check(geometry.every(row => row.textFits), 'operational table text or badge labels are clipped');
      check(geometry.every(row => row.statusOnOneLine), 'short health labels break across lines');
    }
    async function compactShellFits() {
      await page.waitForFunction(() => matchMedia('(max-width: 760px)').matches && Math.abs(document.querySelector('.sidebar').getBoundingClientRect().width - 64) < 1);
      const fits = await page.evaluate(() => {
        const sidebar = document.querySelector('.sidebar').getBoundingClientRect();
        const logo = document.querySelector('.brand-logo').getBoundingClientRect();
        const main = document.querySelector('.app-main').getBoundingClientRect();
        const title = document.querySelector('.topbar-title').getBoundingClientRect();
        return logo.left >= sidebar.left && logo.right <= sidebar.right + 1 && sidebar.right <= main.left + 1 && title.left >= sidebar.right && main.right <= innerWidth + 1 && document.documentElement.scrollWidth <= innerWidth + 1;
      });
      check(fits, 'compact sidebar, logo or header overlaps the main page');
    }
    await rowsDoNotOverlap();
    await save('monitor-table-desktop.png');
    const largeText = await page.addStyleTag({ content: '.vtable-row .vtable-td{font-size:22px!important;line-height:1.8!important}.vtable-row span{font-size:20px!important}' });
    await rowsDoNotOverlap();
    await save('monitor-table-large-text.png');
    await largeText.evaluate(element => element.remove());
    // A 720 CSS-pixel viewport exercises the layout width of a 1440-pixel
    // desktop at 200% zoom. Keep Playwright's viewport bookkeeping consistent
    // with its screenshot capture; this is equivalent reflow, not toolbar zoom.
    await page.setViewportSize({ width: 720, height: 540 });
    await compactShellFits();
    await rowsDoNotOverlap();
    await save('monitor-table-zoom-reflow.png');
    await page.setViewportSize({ width: 390, height: 844 });
    await compactShellFits();
    await rowsDoNotOverlap();
    const tableWidths = await page.evaluate(() => ({ viewport: innerWidth, document: document.documentElement.scrollWidth, body: document.body.scrollWidth }));
    check(tableWidths.document <= tableWidths.viewport + 1 && tableWidths.body <= tableWidths.viewport + 1, 'scrollable table overflows the mobile page');
    await save('monitor-table-mobile.png');
    await page.setViewportSize({ width: 1440, height: 1080 });
    checkpoints.push('maintenance remains distinct from critical counts; measured virtual rows do not overlap at 1440px, larger text, 200% equivalent zoom reflow, and a 390px viewport with long monitor identity/name fixtures');
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

'use strict';
const { chromium } = require(process.env.CPRA_BROWSER_MODULE);
const path = require('node:path');
const origin = process.env.CPRA_BROWSER_ORIGIN;
const operator = 'main-operator-fixture-012345678901234567890123456789';
const reader = 'main-reader-fixture-012345678901234567890123456789';
function check(value, message) { if (!value) throw new Error(message); }

(async () => {
  const browser = await chromium.launch({ headless: true, executablePath: process.env.CPRA_BROWSER_EXECUTABLE,
    args: [`--ignore-certificate-errors-spki-list=${process.env.CPRA_BROWSER_CERT_PIN}`] });
  try {
    const context = await browser.newContext({ viewport: { width: 1440, height: 1080 } });
    const page = await context.newPage();
    page.setDefaultTimeout(20_000);
    const errors = [], writes = [], operationHandles = [], checkpoints = [], reads = [], metricsRequests = [], keyboardSteps = [];
    page.on('pageerror', error => errors.push(error.message));
    page.on('request', request => {
      const url = new URL(request.url());
      if (url.origin !== origin) return;
      if (request.method() === 'GET') reads.push(url.pathname);
      if (url.pathname.startsWith('/api/v2/') && request.method() !== 'GET') writes.push({ method: request.method(), path: url.pathname });
      if (url.pathname === '/metrics') metricsRequests.push({ method: request.method(), path: url.pathname, queryEmpty: url.search === '', readerBearer: request.headers().authorization === `Bearer ${reader}` });
    });
    page.on('response', response => { if (response.url().startsWith(origin + '/api/v2/') && response.request().method() !== 'GET' && response.ok()) operationHandles.push(response.headers()['x-operation-id']); });
    const network = await context.newCDPSession(page);
    await network.send('Network.enable', { maxTotalBufferSize: 8 * 1024 * 1024, maxResourceBufferSize: 4 * 1024 * 1024, enableDurableMessages: true });
    function observedJSONResponse(predicate, label) {
      return new Promise((resolve, reject) => {
        const records = new Map();
        let total = 0, settled = false;
        const cleanup = () => {
          clearTimeout(timer);
          for (const [event, handler] of handlers) network.off(event, handler);
          for (const record of records.values()) for (const chunk of record.chunks) chunk.fill(0);
        };
        const fail = () => { if (!settled) { settled = true; cleanup(); reject(new Error(`${label} response observation failed: ${JSON.stringify([...records.values()].map(({ status, size, finished, interrupted, streamUnavailable }) => ({ status, size, finished, interrupted, streamUnavailable })))}`)); } };
        const append = (record, data, first = false) => {
          if (settled) return;
          const chunk = Buffer.from(data, 'base64');
          record.size += chunk.length; total += chunk.length;
          if (record.size > 4 * 1024 * 1024 || total > 8 * 1024 * 1024) { chunk.fill(0); fail(); return; }
          if (first) record.chunks.unshift(chunk); else record.chunks.push(chunk);
        };
        const finish = (event, interrupted) => {
          const record = records.get(event.requestId);
          if (!record || record.finished || settled) return;
          record.finished = true; record.interrupted = interrupted;
          void (async () => {
            await record.stream;
            if (settled || !record.status) return;
            let bytes;
            if (record.chunks.length) bytes = Buffer.concat(record.chunks);
            else {
              try {
                const response = await network.send('Network.getResponseBody', { requestId: event.requestId });
                check(response.body.length <= 6 * 1024 * 1024, 'Encoded API response exceeded its bound');
                bytes = Buffer.from(response.body, response.base64Encoded ? 'base64' : 'utf8');
                response.body = '';
              } catch { if (interrupted) return; throw new Error('Completed response body is unavailable'); }
            }
            try {
              check(bytes.length <= 4 * 1024 * 1024, 'API response exceeded its bound');
              let body;
              try { body = JSON.parse(bytes.toString('utf8')); }
              catch { if (interrupted) return; throw new Error('Completed response is not JSON'); }
              settled = true; cleanup(); resolve({ url: record.url, status: record.status, body });
            } finally { bytes.fill(0); }
          })().catch(fail);
        };
        const handlers = [
          ['Network.requestWillBeSent', event => {
            if (event.request.method !== 'GET' || !predicate(new URL(event.request.url))) return;
            if (records.size >= 32 || records.has(event.requestId)) { fail(); return; }
            records.set(event.requestId, { url: event.request.url, chunks: [], size: 0 });
          }],
          ['Network.responseReceived', event => {
            const record = records.get(event.requestId);
            if (!record) return;
            record.status = event.response.status;
            record.stream = network.send('Network.streamResourceContent', { requestId: event.requestId })
              .then(result => append(record, result.bufferedData, true)).catch(() => { record.streamUnavailable = true; });
          }],
          ['Network.dataReceived', event => { const record = records.get(event.requestId); if (record && event.data) append(record, event.data); }],
          ['Network.loadingFinished', event => finish(event, false)],
          ['Network.loadingFailed', event => finish(event, true)],
        ];
        const timer = setTimeout(fail, 20_000);
        for (const [event, handler] of handlers) network.on(event, handler);
      });
    }
    async function signIn(token) {
      await page.getByRole('heading', { name: 'Sign in to CPRa' }).waitFor();
      await page.getByLabel('Bearer token').fill(token);
      await page.getByRole('button', { name: 'Sign in', exact: true }).click();
      await page.getByRole('button', { name: 'Sign out', exact: true }).waitFor();
    }
    async function keyboardActivate(locator, label) {
      await locator.waitFor({ state: 'visible' });
      for (let presses = 1; presses <= 100; presses++) {
        await page.keyboard.press('Tab');
        if (await locator.evaluate(element => element === document.activeElement)) {
          keyboardSteps.push({ control: label, tabPresses: presses });
          await page.keyboard.press('Enter');
          return;
        }
      }
      throw new Error(`Keyboard could not reach ${label} within 100 Tab presses`);
    }
    async function checkMobileOverflow(label) {
      const widths = await page.evaluate(() => ({ viewport: innerWidth, document: document.documentElement.scrollWidth, body: document.body.scrollWidth }));
      check(widths.document <= widths.viewport + 1 && widths.body <= widths.viewport + 1, `${label} overflows mobile viewport: ${JSON.stringify(widths)}`);
    }
    async function counts() { const response = await fetch(process.env.CPRA_BROWSER_TARGET + '/count'); check(response.ok, 'target accounting unavailable'); return response.json(); }
    await page.goto(origin + '/monitors');
    await signIn(operator);
    await page.getByText('Browser recovery', { exact: true }).click();
    const controls = page.getByRole('region', { name: 'Monitor controls', exact: true });
    const actions = page.getByRole('region', { name: 'Action outcomes', exact: true });
    await controls.getByRole('button', { name: 'Request recovery', exact: true }).click();
    const recovery = page.getByRole('dialog', { name: 'Request recovery', exact: true });
    check(await recovery.getByRole('button', { name: 'Request recovery', exact: true }).isDisabled(), 'recovery accepted without a reason');
    await recovery.getByLabel('Reason', { exact: true }).fill('Investigated the local target before requesting recovery');
    await recovery.getByRole('button', { name: 'Request recovery', exact: true }).click();
    await recovery.waitFor({ state: 'hidden' });
    await controls.getByText('Saved durably and applied by the controller.', { exact: true }).waitFor();
    const review = actions.getByRole('button', { name: /^Review action / });
    await review.waitFor();
    check((await counts()).recoveries === 1, 'manual request did not perform exactly one local recovery');
    checkpoints.push('reasoned manual recovery uses normal driver and one real network request');

    async function recordReview(resolution, reason) {
      await review.click();
      const dialog = page.getByRole('dialog', { name: 'Review unknown action', exact: true });
      await dialog.getByLabel('Conclusion').selectOption(resolution);
      await dialog.getByLabel('Reason', { exact: true }).fill(reason);
      await dialog.getByLabel('Note (optional)', { exact: true }).fill('Operator browser qualification note');
      await dialog.getByLabel(/^Evidence references \(optional\)/).fill('local-fixture-receipt-1');
      await dialog.getByRole('button', { name: 'Record review', exact: true }).click();
      await dialog.waitFor({ state: 'hidden' });
      await actions.getByText('Saved durably and applied by the controller.', { exact: true }).waitFor();
    }
    await recordReview('inconclusive', 'Waiting for independent receipt inspection');
    await actions.getByText(/Operator review: inconclusive · team\/oncall/).waitFor();
    await actions.getByText(/Held for investigation/).waitFor();
    checkpoints.push('inconclusive review retains the hold and records the named operator');
    await recordReview('accepted', 'Operator confirmed acceptance using the designated fixture receipt');
    await actions.getByText(/Operator review: accepted · team\/oncall/).waitFor();
    await actions.getByText(/No investigation hold/).waitFor();
    await page.getByRole('cell', { name: /Operator confirmed acceptance using the designated fixture receipt/ }).waitFor();
    await page.waitForTimeout(1100);
    check((await counts()).recoveries === 1, 'review caused duplicate recovery');
    checkpoints.push('conclusive review preserves unknown provider outcome, records evidence, and never replays');
    check((await actions.textContent()).includes('external outcome unknown'), 'review replaced original provider facts');
    await page.screenshot({ path: path.join(process.env.CPRA_BROWSER_EVIDENCE_DIR, 'operator-review.png'), fullPage: true });

    await review.click();
    await page.getByLabel('Reason', { exact: true }).fill('Discard this review draft on navigation');
    await page.goBack();
    await page.getByRole('heading', { name: 'Monitor Fleet', exact: true }).waitFor();
    await page.getByText('Browser recovery', { exact: true }).click();
    await actions.waitFor();
    check(await page.getByRole('dialog').count() === 0, 'navigation retained the old action review draft');
    checkpoints.push('navigation clears the previous action review draft');
    await page.reload();
    await signIn(reader);
    await actions.getByText(/Operator review: accepted · team\/oncall/).waitFor();
    check(await page.getByRole('button', { name: /^(Review action |Request recovery|check now)/i }).count() === 0, 'reader sees a mutation control');
    await page.getByRole('link', { name: 'Operation progress', exact: true }).click();
    await page.getByRole('heading', { name: 'Retained operations', exact: true }).waitFor();
    await page.getByLabel('Exact monitor ID (optional)', { exact: true }).fill('browser-recovery');
    const [operationPageResponse] = await Promise.all([
      observedJSONResponse(url => url.origin === origin && url.pathname === '/api/v2/operations' && url.searchParams.get('monitorID') === 'browser-recovery', 'Retained operation page'),
      page.getByRole('button', { name: 'Apply filter', exact: true }).click(),
    ]);
    check(operationPageResponse.status === 200, 'reader operation page failed');
    const operationPage = operationPageResponse.body;
    const reviewedHandle = operationHandles[operationHandles.length - 1];
    check(typeof reviewedHandle === 'string' && /^op\.[a-f0-9-]{36}\.\d{20}$/.test(reviewedHandle), 'review response lost its allocated operation handle');
    check(operationPage.items.some(item => item.id === reviewedHandle), 'retained list omitted the completed review');
    check(operationPage.items.every(operation => operation.items.every(item => item.id === 'browser-recovery')), 'exact monitor operation filter included another resource');
    await page.getByRole('link', { name: reviewedHandle, exact: true }).waitFor();
    await page.screenshot({ path: path.join(process.env.CPRA_BROWSER_EVIDENCE_DIR, 'reader-operations.png'), fullPage: true });
    await page.getByRole('link', { name: reviewedHandle, exact: true }).click();
    await page.getByRole('heading', { name: 'Completed', exact: true }).waitFor();
    check((await counts()).recoveries === 1, 'reading operation list or detail repeated recovery');
    checkpoints.push('reader discovers retained monitor operations and opens the exact completed review without mutations');

    const observationsStart = reads.length;
    await page.getByRole('link', { name: 'Monitor configurations', exact: true }).click();
    await page.getByRole('link', { name: 'Browser recovery', exact: true }).click();
    await page.getByRole('link', { name: 'View monitoring and controls', exact: true }).click();
    await page.getByRole('heading', { name: 'Monitor state', exact: true }).waitFor();
    const savedIdentity = page.getByRole('region', { name: 'Saved monitor identity', exact: true });
    await savedIdentity.waitFor();
    check(new URL(page.url()).pathname === '/monitors/by-id/browser-recovery', 'configuration did not navigate through the stable monitor ID');
    check((await savedIdentity.textContent()).includes('browser-recovery'), 'stable monitor identity was not rendered');
    await page.getByRole('region', { name: 'Monitor application and observations', exact: true }).getByText(/The controller reports this configuration generation applied/).waitFor();
    await actions.getByText(/Operator review: accepted · team\/oncall/).waitFor();
    check(await page.getByRole('button', { name: /^(Review action |Request recovery|Acknowledge|Dismiss|Snooze|Disable|Enable|check now)/i }).count() === 0, 'stable-ID reader view exposed a write control');
    await page.screenshot({ path: path.join(process.env.CPRA_BROWSER_EVIDENCE_DIR, 'reader-monitor-state.png'), fullPage: true });
    checkpoints.push('saved configuration navigates to stable-ID monitor observations, history and existing action evidence');

    const [incidentResponse] = await Promise.all([
      observedJSONResponse(url => url.origin === origin && url.pathname === '/api/v2/incidents', 'Incident page'),
      page.getByRole('link', { name: 'Alerts', exact: true }).click(),
    ]);
    check(incidentResponse.status === 200 && new URL(incidentResponse.url).searchParams.get('limit') === '100', 'incident list did not request a bounded v2 page');
    const incidentPage = incidentResponse.body;
    const originalIncident = incidentPage.items.find(item => item.monitorID === 'browser-recovery');
    check(incidentPage.items.length <= 100 && originalIncident && typeof originalIncident.id === 'string', 'real manual-recovery incident was not retained in the list');
    check(typeof incidentPage.snapshot === 'string' && !incidentPage.generatedAt.startsWith('0001-01-01'), 'incident page did not identify its snapshot');
    await page.getByRole('heading', { name: 'Latest incidents', exact: true }).waitFor();
    const incidentRegion = page.getByRole('region', { name: 'Latest incident page', exact: true });
    await incidentRegion.getByRole('link', { name: 'browser-recovery', exact: true }).waitFor();
    check((await page.getByLabel('Incident page counts', { exact: true }).textContent()).includes('page counts, not fleet totals'), 'incident page counts implied fleet totals');
    check(!(await incidentRegion.textContent()).includes('0001'), 'Go zero incident timestamps appeared as actual observations');
    await page.screenshot({ path: path.join(process.env.CPRA_BROWSER_EVIDENCE_DIR, 'reader-incidents.png'), fullPage: true });
    await incidentRegion.getByRole('link', { name: 'browser-recovery', exact: true }).click();
    await savedIdentity.waitFor();
    check(new URL(page.url()).pathname === '/monitors/by-id/browser-recovery', 'incident link lost stable monitor identity');
    check(reads.slice(observationsStart).every(path => !/^\/api\/v1\/monitors\/[^/]+$/.test(path)), 'stable-ID observation fell back to numeric detail');
    checkpoints.push('reader lists the actual latest incident with bounded snapshot metadata and returns to its stable-ID monitor');

    const [stateResponse, readinessResponse] = await Promise.all([
      page.waitForResponse(response => new URL(response.url()).origin === origin && new URL(response.url()).pathname === '/api/v2/state'),
      page.waitForResponse(response => new URL(response.url()).origin === origin && new URL(response.url()).pathname === '/api/v2/readyz'),
      page.getByRole('link', { name: 'System Health', exact: true }).click(),
    ]);
    check(stateResponse.ok() && readinessResponse.ok(), 'runtime observation endpoints failed for reader');
    // The query client may cancel a navigation-time request. Correlate the real
    // successful endpoint responses with the panel's decoded current observation
    // instead of asking Chromium to capture that canceled request body again.
    const runtimeRegion = page.getByRole('region', { name: 'Runtime readiness and progress', exact: true });
    await runtimeRegion.getByText('Ready for new work', { exact: true }).waitFor();
    await runtimeRegion.locator('p').filter({ hasText: /^Readiness endpoint: Ready(?: ·|\.)/ }).waitFor();
    check(await runtimeRegion.locator('dt').filter({ hasText: /^Storage availability$/ }).evaluate(element => element.nextElementSibling?.textContent) === 'Available', 'runtime panel lost storage availability');
    check(await runtimeRegion.locator('dt').filter({ hasText: /^Controller initialization and progress$/ }).evaluate(element => element.nextElementSibling?.textContent) === 'Ready', 'runtime panel lost controller readiness');
    checkpoints.push('actual state and readiness endpoints report usable Raft and controller progress despite the failed monitored target');

    await page.getByRole('link', { name: 'Settings', exact: true }).click();
    await page.getByRole('button', { name: 'View metrics', exact: true }).waitFor();
    check(metricsRequests.length === 0, 'metrics loaded before the explicit view request');
    const [metricsResponse] = await Promise.all([
      page.waitForResponse(response => new URL(response.url()).origin === origin && new URL(response.url()).pathname === '/metrics'),
      page.getByRole('button', { name: 'View metrics', exact: true }).click(),
    ]);
    check(metricsResponse.ok(), 'authenticated metrics export failed');
    const metricOutput = page.getByRole('region', { name: 'Prometheus metrics output', exact: true });
    await metricOutput.waitFor();
    check((await metricOutput.textContent()).includes('cpra_'), 'metrics viewer did not render actual CPRa exposition');
    await page.screenshot({ path: path.join(process.env.CPRA_BROWSER_EVIDENCE_DIR, 'reader-metrics.png'), fullPage: true });
    await page.getByRole('button', { name: 'Hide metrics', exact: true }).click();
    check(await metricOutput.count() === 0, 'hide retained displayed metrics');
    await page.waitForTimeout(1100);
    check(metricsRequests.length === 1, 'hidden metrics triggered an automatic request');
    await page.getByRole('button', { name: 'View metrics', exact: true }).click();
    await metricOutput.waitFor();
    check(metricsRequests.length === 2 && metricsRequests.every(request => request.method === 'GET' && request.readerBearer && request.queryEmpty), 'metrics did not use exactly two explicit authenticated reads');
    await page.setViewportSize({ width: 390, height: 844 });
    await checkMobileOverflow('Metrics');
    await keyboardActivate(page.getByRole('link', { name: 'Alerts', exact: true }), 'Alerts navigation');
    await page.getByRole('heading', { name: 'Latest incidents', exact: true }).waitFor();
    await incidentRegion.getByRole('link', { name: 'browser-recovery', exact: true }).waitFor();
    await checkMobileOverflow('Incidents');
    await page.screenshot({ path: path.join(process.env.CPRA_BROWSER_EVIDENCE_DIR, 'mobile-reader-incidents.png'), fullPage: true });
    await keyboardActivate(incidentRegion.getByRole('link', { name: 'browser-recovery', exact: true }), 'Stable monitor identity');
    await savedIdentity.waitFor();
    check(new URL(page.url()).pathname === '/monitors/by-id/browser-recovery', 'mobile keyboard incident navigation lost stable identity');
    await checkMobileOverflow('Monitor state');
    await keyboardActivate(page.getByRole('link', { name: 'Settings', exact: true }), 'Settings navigation');
    await page.getByRole('button', { name: 'View metrics', exact: true }).waitFor();
    check(metricsRequests.length === 2, 'mobile navigation triggered an automatic metrics request');
    await keyboardActivate(page.getByRole('button', { name: 'View metrics', exact: true }), 'View metrics');
    await metricOutput.waitFor();
    check((await metricOutput.textContent()).includes('cpra_'), 'mobile keyboard metrics did not show actual exposition');
    await checkMobileOverflow('Mobile metrics');
    await page.screenshot({ path: path.join(process.env.CPRA_BROWSER_EVIDENCE_DIR, 'mobile-reader-metrics.png'), fullPage: true });
    await keyboardActivate(page.getByRole('button', { name: 'Hide metrics', exact: true }), 'Hide metrics');
    check(await metricOutput.count() === 0, 'mobile keyboard hide retained displayed metrics');
    check(metricsRequests.length === 3 && metricsRequests.every(request => request.method === 'GET' && request.readerBearer && request.queryEmpty), 'metrics did not use exactly three explicit authenticated reads');
    checkpoints.push('390×844 keyboard reader navigation reaches Alerts, stable monitor, Settings and View/Hide metrics without body overflow; scoped qualification only');
    await keyboardActivate(page.getByRole('button', { name: 'View metrics', exact: true }), 'View metrics before sign-out');
    await metricOutput.waitFor();
    check(metricsRequests.length === 4 && metricsRequests.every(request => request.method === 'GET' && request.readerBearer && request.queryEmpty), 'final metrics view did not use exactly four explicit authenticated reads');
    const authenticatedStorage = await page.evaluate(() => JSON.stringify({ local: { ...localStorage }, session: { ...sessionStorage }, cookie: document.cookie }));
    check(!authenticatedStorage.includes(operator) && !authenticatedStorage.includes(reader), 'authenticated metrics view persisted a token');
    await page.getByRole('button', { name: 'Sign out', exact: true }).click();
    await page.getByRole('heading', { name: 'Sign in to CPRa', exact: true }).waitFor();
    check(await metricOutput.count() === 0, 'sign-out retained the previous metrics');
    check((await counts()).recoveries === 1, 'observation navigation repeated recovery');
    checkpoints.push('reader views real authenticated metrics on demand; hide and sign-out clear output without token URLs or storage');

    const storage = await page.evaluate(() => JSON.stringify({ local: { ...localStorage }, session: { ...sessionStorage }, cookie: document.cookie }));
    check(!storage.includes(operator) && !storage.includes(reader), 'token persisted in browser storage');
    check(writes.length === 3 && writes[0].path.endsWith('/recover') && writes.slice(1).every(write => write.path.endsWith('/review')), 'unexpected write or hidden retry');
    check(errors.length === 0, `uncaught browser errors: ${JSON.stringify(errors)}`);
    checkpoints.push('reader remains observational; three explicit writes, no hidden retries or token storage');
    console.log(JSON.stringify({ scope: 'normal main, embedded SPA, TLS, encrypted Raft, actual local HTTP/webhook targets; no production-account certification', browser: browser.version(), node: process.version, playwright: require(`${process.env.CPRA_BROWSER_MODULE}/package.json`).version, checkpoints, writes, metricsRequests, keyboardSteps }));
    await context.close();
  } finally { await browser.close(); }
})().catch(error => { console.error(error.message); process.exitCode = 1; });

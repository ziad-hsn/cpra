'use strict';
const path = require('node:path');
const { chromium } = require(process.env.CPRA_BROWSER_MODULE);
const origin = process.env.CPRA_BROWSER_ORIGIN;
const files = JSON.parse(process.env.CPRA_BROWSER_FILES);
const marker = process.env.CPRA_BROWSER_PRIVATE_MARKER;
const names = files.map(file => path.basename(file));
const operator = 'main-operator-fixture-012345678901234567890123456789';
const check = (ok, message) => { if (!ok) throw new Error(message); };

(async () => {
  const browser = await chromium.launch({ headless: true, executablePath: process.env.CPRA_BROWSER_EXECUTABLE,
    args: [`--ignore-certificate-errors-spki-list=${process.env.CPRA_BROWSER_CERT_PIN}`] });
  let stage = 'launch';
  let lastPage;
  try {
    const context = await browser.newContext({ viewport: { width: 1440, height: 1100 } });
    const page = await context.newPage(); page.setDefaultTimeout(20_000);
    lastPage = page;
    const writes = [], errors = [], reads = [], pending = [];
    let captureFailure = '', interruptedReads = 0;
    page.on('pageerror', () => errors.push('page error'));
    page.on('request', request => {
      if (!request.url().startsWith(origin + '/api/')) return;
      check(!request.url().includes(operator) && !request.url().includes(marker), 'Private data in request URL');
      if (request.method() !== 'GET') {
        const body = request.postData() || '';
        check(names.every(name => !body.includes(name)), 'Local filename sent to server');
        writes.push({ path: new URL(request.url()).pathname, method: request.method(), empty: body.length === 0 });
      }
    });
    // Inspector streaming retains bytes when React cancels a superseded query.
    // Observe traffic without intercepting requests or replacing application I/O.
    const network = await context.newCDPSession(page), records = new Map();
    await network.send('Network.enable', { maxTotalBufferSize: 16 * 1024 * 1024, maxResourceBufferSize: 4 * 1024 * 1024, enableDurableMessages: true });
    let buffered = 0;
    const append = (record, encoded, initial = false) => {
      const bytes = Buffer.from(encoded, 'base64');
      if (record.bytes + bytes.length > 4 * 1024 * 1024 || buffered + bytes.length > 16 * 1024 * 1024) { bytes.fill(0); captureFailure ||= 'Read capture exceeded its byte bound'; return; }
      record.bytes += bytes.length; buffered += bytes.length;
      if (initial) record.chunks.unshift(bytes); else record.chunks.push(bytes);
    };
    network.on('Network.requestWillBeSent', event => {
      if (!event.request.url.startsWith(origin + '/api/v2/') || event.request.method !== 'GET') return;
      if (records.size >= 256 || records.has(event.requestId)) { captureFailure ||= 'Read capture exceeded its count bound or redirected'; return; }
      const record = { path: new URL(event.request.url).pathname, chunks: [], bytes: 0 };
      pending.push(new Promise(resolve => { record.complete = resolve; })); records.set(event.requestId, record);
    });
    network.on('Network.responseReceived', event => {
      const record = records.get(event.requestId); if (!record) return;
      record.status = event.response.status;
      record.stream = network.send('Network.streamResourceContent', { requestId: event.requestId })
        .then(body => append(record, body.bufferedData, true)).catch(() => { record.unavailable = true; });
    });
    network.on('Network.dataReceived', event => { const record = records.get(event.requestId); if (record && event.data) append(record, event.data); });
    const finish = (event, interrupted) => {
      const record = records.get(event.requestId); if (!record || record.finished) return;
      record.finished = true;
      if (interrupted) interruptedReads++;
      void (async () => {
        await record.stream;
        if (!record.status && interrupted) return;
        let raw;
        if (!record.unavailable) raw = Buffer.concat(record.chunks);
        else {
          const body = await network.send('Network.getResponseBody', { requestId: event.requestId });
          check(body.body.length <= 6 * 1024 * 1024, 'Encoded read exceeded its bound');
          raw = Buffer.from(body.body, body.base64Encoded ? 'base64' : 'utf8'); body.body = '';
        }
        try {
          check(raw.length <= 4 * 1024 * 1024, 'Decoded read exceeded its bound');
          check(!raw.includes(Buffer.from(operator)) && !raw.includes(Buffer.from(marker)), 'Private data in read response');
          if (record.status === 200 && /^\/api\/v2\/operations\/[^/]+$/.test(record.path)) {
            let observation;
            try { observation = JSON.parse(raw.toString('utf8')); }
            catch { if (interrupted) return; throw new Error('Completed result was not JSON'); }
            reads.push(observation);
          }
        } finally { raw.fill(0); }
      })().catch(() => { captureFailure ||= `Read capture failed at ${record.path}`; }).finally(() => {
        for (const chunk of record.chunks) { buffered -= chunk.length; chunk.fill(0); }
        record.chunks = []; record.complete();
      });
    };
    network.on('Network.loadingFinished', event => finish(event, false));
    network.on('Network.loadingFailed', event => finish(event, true));
    stage = 'sign in and file selection';
    await page.goto(`${origin}/import`);
    await page.getByLabel('Bearer token').fill(operator);
    await page.getByRole('button', { name: 'Sign in', exact: true }).click();
    await page.getByRole('heading', { name: 'Apply configuration', exact: true }).waitFor();
    const chooser = page.waitForEvent('filechooser');
    await page.getByLabel('Configuration files', { exact: true }).click();
    await (await chooser).setFiles(files);
    await page.getByRole('button', { name: 'Create inactive operation', exact: true }).click();
    await page.getByRole('alert').filter({ hasText: 'Inactive operation created.' }).waitFor();
    await page.getByRole('button', { name: 'Upload remaining resources', exact: true }).click();
    await page.getByRole('alert').filter({ hasText: 'All resources are staged.' }).waitFor();
    stage = 'sealed validation';
    await page.getByRole('button', { name: 'Validate whole collection', exact: true }).click();
    await page.getByRole('heading', { name: 'Saved validation result', exact: true }).waitFor();
    check(writes.every(write => !write.path.endsWith('/activate')), 'Validation implicitly activated the collection');
    const originalLink = page.getByRole('region', { name: 'Apply collection' }).getByRole('link');
    const operationID = (await originalLink.innerText()).trim();
    check(operationID.startsWith('op.'), 'Missing original operation identity');
    stage = 'explicit activation';
    await page.getByRole('button', { name: 'Review activation', exact: true }).click();
    const dialog = page.getByRole('alertdialog');
    check((await dialog.innerText()).includes(operationID), 'Confirmation omits original operation');
    await page.getByRole('button', { name: 'Keep reviewing', exact: true }).click();
    check(writes.every(write => !write.path.endsWith('/activate')), 'Dismissing confirmation activated work');
    await page.getByRole('button', { name: 'Review activation', exact: true }).click();
    await page.getByRole('button', { name: 'Confirm activation', exact: true }).click();
    await page.getByText('Resource results are retained and ready to read.', { exact: true }).waitFor({ timeout: 45_000 });
    await page.getByRole('button', { name: 'Read resource results', exact: true }).click();
    const results = page.getByRole('region', { name: 'Application resource results' });
    await results.waitFor();
    const cells = await results.getByRole('row').allTextContents();
    check(cells.length === 3 && cells[1].includes('Monitor/activated-monitor') && cells[2].includes('Credential/activated-secret'), 'Results lost original input order');
    check(cells.slice(1).every(row => row.includes('Change committed') && row.includes('Applied')), 'Configuration decisions and controller results missing');
    check(cells[1].includes(names[0]) && cells[2].includes(names[1]), 'Result source attribution missing');
    const drainCaptures = async () => {
      let timer;
      try {
        await Promise.race([(async () => {
          let count;
          do { count = pending.length; await Promise.all(pending.slice(0, count)); }
          while (pending.length !== count);
        })(), new Promise((_, reject) => { timer = setTimeout(() => reject(new Error('Read capture did not finish')), 10_000); })]);
      } finally { clearTimeout(timer); }
      check(!captureFailure, captureFailure);
    };
    await drainCaptures();
    const original = reads.findLast(read => read.id === operationID && read.executionResult?.state === 'ready');
    check(original?.state === 'completed' && original.committed === 2 && original.applied === 2, 'Original operation did not complete both resources');
    check(original.normalizationProfile === 'cpra.file.base.v3', 'Original file normalization profile was lost');
    const activations = writes.filter(write => write.path.endsWith('/activate'));
    check(activations.length === 1 && activations[0].path === `/api/v2/operations/${operationID}/activate` && activations[0].empty, 'Activation was duplicated, redirected or included a body');

    stage = 'guided notification setup';
    await page.getByRole('button', { name: 'Close this import', exact: true }).click();
    await page.getByText('Set up webhook notifications', { exact: true }).click();
    await page.getByRole('combobox', { name: /^Choose monitor/ }).selectOption('activated-monitor');
    await page.getByLabel('Recipient name', { exact: true }).fill('Example on-call');
    await page.getByLabel('Webhook URL', { exact: true }).fill(`https://example.invalid/${marker}`);
    const beforeSetupWrites = writes.length;
    await page.getByRole('button', { name: 'Prepare notification setup', exact: true }).click();
    const setupPreview = page.getByRole('region', { name: 'Collection preview', exact: true });
    await setupPreview.getByRole('row').nth(5).waitFor();
    check(await setupPreview.getByRole('row').count() === 6, 'Guided setup did not compose five resources');
    check(writes.length === beforeSetupWrites, 'Local guided preparation performed an HTTP mutation');
    check(!((await page.content()).includes(marker)), 'Guided preparation retained a secret in the DOM');
    await page.getByRole('button', { name: 'Preview on server', exact: true }).click();
    await page.getByRole('alert').filter({ hasText: 'Server preview passed.' }).waitFor();
    check(writes.slice(beforeSetupWrites).every(write => write.path.endsWith('/collections/preflight')), 'Guided preview activated configuration');
    await page.getByRole('button', { name: 'Create inactive operation', exact: true }).click();
    await page.getByRole('alert').filter({ hasText: 'Inactive operation created.' }).waitFor();
    await page.getByRole('button', { name: 'Upload remaining resources', exact: true }).click();
    await page.getByRole('alert').filter({ hasText: 'All resources are staged.' }).waitFor();
    await page.getByRole('button', { name: 'Validate whole collection', exact: true }).click();
    await page.getByRole('heading', { name: 'Saved validation result', exact: true }).waitFor();
    const guidedOperationID = (await page.getByRole('region', { name: 'Apply collection' }).getByRole('link').innerText()).trim();
    check(guidedOperationID !== operationID && guidedOperationID.startsWith('op.'), 'Guided setup lost its distinct operation identity');
    await page.getByRole('button', { name: 'Review activation', exact: true }).click();
    check((await page.getByRole('alertdialog').innerText()).includes(guidedOperationID), 'Guided confirmation omitted its operation');
    await page.getByRole('button', { name: 'Confirm activation', exact: true }).click();
    await page.getByText('Resource results are retained and ready to read.', { exact: true }).waitFor({ timeout: 45_000 });
    await page.getByRole('button', { name: 'Read resource results', exact: true }).click();
    const guidedRows = page.getByRole('region', { name: 'Application resource results' }).getByRole('row');
    await guidedRows.nth(5).waitFor();
    check(await guidedRows.count() === 6 && (await guidedRows.allTextContents()).slice(1).every(row => row.includes('Change committed') && row.includes('Applied')), 'Guided resources were not all applied');
    check(writes.filter(write => write.path === `/api/v2/operations/${guidedOperationID}/activate`).length === 1, 'Guided activation was repeated');
    check(writes.slice(beforeSetupWrites).every(write => write.path === '/api/v2/operations' || write.path.startsWith('/api/v2/operations/') || ['/api/v2/collections/preflight', '/api/v2/collections/prepare'].includes(write.path)), 'Guided setup bypassed collection admission');
    const dom = await page.locator('body').innerText();
    check(!dom.includes(marker) && !dom.includes(operator), 'Secret or token in DOM');
    const storage = await page.evaluate(async () => JSON.stringify({ local: { ...localStorage }, session: { ...sessionStorage }, cookies: document.cookie, databases: await indexedDB.databases(), caches: await caches.keys() }));
    check([marker, operator, ...names].every(value => !storage.includes(value)), 'Private input persisted in browser storage');
    await drainCaptures();
    stage = 'sign out with retained results';
    check(await results.count() === 1, 'Sign-out fixture has no populated results');
    await page.getByRole('button', { name: 'Sign out', exact: true }).click();
    await page.getByLabel('Bearer token').waitFor();
    check(await results.count() === 0 && !((await page.locator('body').innerText()).includes(guidedOperationID)), 'Sign-out retained operation results');
    await drainCaptures();
    stage = 'new session overview';
    await page.getByLabel('Bearer token').fill(operator);
    await page.getByRole('button', { name: 'Sign in', exact: true }).click();
    await page.getByRole('link', { name: 'Overview', exact: true }).click();
    await page.getByRole('heading', { name: 'Fleet Overview', exact: true }).waitFor();
    await page.getByRole('status', { name: 'Global fleet status' }).waitFor();
    if (process.env.CPRA_BROWSER_EVIDENCE_DIR) {
      check(path.isAbsolute(process.env.CPRA_BROWSER_EVIDENCE_DIR), 'Screenshot directory must be absolute');
      await page.screenshot({ path: path.join(process.env.CPRA_BROWSER_EVIDENCE_DIR, 'dashboard-overview.png'), fullPage: true });
    }
    // Use the public API for concurrent-editor fixtures, without replacing any
    // browser transport. The imported monitor stays disabled throughout.
    async function api(resource, method = 'GET', body) {
      return page.evaluate(async ({resource, method, body, token}) => {
        const headers = {Authorization: `Bearer ${token}`};
        if (body) headers['Content-Type'] = 'application/json';
        if (method === 'PUT') headers['If-Match'] = `"${body.metadata.resourceVersion}"`;
        if (method === 'POST') headers['If-None-Match'] = '*';
        const response = await fetch(resource, {method, headers, body: body ? JSON.stringify(body) : undefined});
        if (!response.ok) throw new Error(`Fixture request failed: ${response.status}`);
        return response.json();
      }, {resource, method, body, token: operator});
    }
    async function visit(route) {
      await page.goto(origin + route);
      await page.getByLabel('Bearer token').fill(operator);
      await page.getByRole('button', {name: 'Sign in', exact: true}).click();
      await page.getByRole('button', {name: 'Sign out', exact: true}).waitFor();
    }
    const document = resource => ({apiVersion: resource.apiVersion, kind: resource.kind, metadata: resource.metadata, spec: resource.spec});
    stage = 'monitor contextual setup and concurrent edit';
    let observed = await api('/api/v2/monitors/activated-monitor');
    const group = await api('/api/v2/notification-groups/' + observed.spec.notifications.red.groupRef);
    const recipientID = group.spec.recipientRefs[0];
    const blue = {notifyType: 'webhook', recipientRefs: [recipientID]};
    observed.spec.notifications.blue = blue;
    observed = await api('/api/v2/monitors/activated-monitor', 'PUT', document(observed));
    const reviewedVersion = observed.metadata.resourceVersion;
    await visit('/monitors/by-id/activated-monitor');
    await page.getByRole('link', {name: 'Set up notifications', exact: true}).click();
    await page.getByLabel('Webhook destination').selectOption('recipient');
    await page.getByLabel('Notification group name', {exact: true}).fill('Reviewed replacement group');
    await page.getByLabel('Specific recipient ID', {exact: true}).fill(recipientID);
    await page.getByRole('group', {name: 'Recipient', exact: true}).getByRole('button', {name: 'Use ID', exact: true}).click();
    const beforeConditional = writes.length;
    await page.getByRole('button', {name: 'Prepare notification setup', exact: true}).click();
    const review = page.getByLabel('Review notification rule changes', {exact: true});
    await review.waitFor();
    const diff = await review.innerText();
    check(diff.includes(reviewedVersion) && diff.includes('red') && diff.includes('green') && diff.includes('Other notification rules'), 'Conditional notification diff omitted its version or preserved rules');
    check(writes.length === beforeConditional, 'Reviewing a conditional setup mutated configuration');
    observed.metadata.name = 'Concurrent editor change';
    const concurrent = await api('/api/v2/monitors/activated-monitor', 'PUT', document(observed));
    await page.getByRole('button', {name: 'Prepare reviewed collection', exact: true}).click();
    await page.getByRole('region', {name: 'Collection preview', exact: true}).getByRole('row').nth(2).waitFor();
    await page.getByRole('button', {name: 'Preview on server', exact: true}).click();
    await page.getByRole('alert').filter({hasText: 'Server preview found invalid resources or references.'}).waitFor();
    check((await page.getByRole('list', {name: 'Preview validation errors'}).innerText()).includes('A resource version changed; reload and review the intended changes.'), 'Preflight failed for a reason other than the frozen version conflict');
    const afterConflict = await api('/api/v2/monitors/activated-monitor');
    check(afterConflict.metadata.resourceVersion === concurrent.metadata.resourceVersion && JSON.stringify(afterConflict.spec.notifications.blue) === JSON.stringify(blue), 'Conditional setup overwrote a concurrent edit or blue rule');
    check(writes.slice(beforeConditional).every(write => write.path === '/api/v2/monitors/activated-monitor' || write.path === '/api/v2/collections/preflight'), 'Conflicting setup created a replacement operation or activation');
    await page.getByRole('button', {name: 'Discard selection', exact: true}).click();

    stage = 'explicitly reviewed replacement';
    await page.getByLabel('Webhook destination').selectOption('recipient');
    await page.getByLabel('Notification group name', {exact: true}).fill('Approved replacement group');
    await page.getByLabel('Specific recipient ID', {exact: true}).fill(recipientID);
    await page.getByRole('group', {name: 'Recipient', exact: true}).getByRole('button', {name: 'Use ID', exact: true}).click();
    await page.getByRole('button', {name: 'Prepare notification setup', exact: true}).click();
    await review.waitFor();
    check((await review.innerText()).includes(concurrent.metadata.resourceVersion), 'Explicit new review did not show the current version');
    await page.getByRole('button', {name: 'Prepare reviewed collection', exact: true}).click();
    await page.getByRole('region', {name: 'Collection preview', exact: true}).getByRole('row').nth(2).waitFor();
    await page.getByRole('button', {name: 'Preview on server', exact: true}).click();
    await page.getByRole('alert').filter({hasText: 'Server preview passed.'}).waitFor();
    await page.getByRole('button', {name: 'Create inactive operation', exact: true}).click();
    await page.getByRole('alert').filter({hasText: 'Inactive operation created.'}).waitFor();
    await page.getByRole('button', {name: 'Upload remaining resources', exact: true}).click();
    await page.getByRole('alert').filter({hasText: 'All resources are staged.'}).waitFor();
    await page.getByRole('button', {name: 'Validate whole collection', exact: true}).click();
    await page.getByRole('heading', {name: 'Saved validation result', exact: true}).waitFor();
    const replacementOperationID = (await page.getByRole('region', {name: 'Apply collection'}).getByRole('link').innerText()).trim();
    check(replacementOperationID.startsWith('op.') && ![operationID, guidedOperationID].includes(replacementOperationID), 'New reviewed inputs reused an earlier operation');
    await page.getByRole('button', {name: 'Review activation', exact: true}).click();
    check((await page.getByRole('alertdialog').innerText()).includes(replacementOperationID), 'Replacement confirmation omitted its original operation');
    await page.getByRole('button', {name: 'Confirm activation', exact: true}).click();
    await page.getByText('Resource results are retained and ready to read.', {exact: true}).waitFor({timeout: 45_000});
    await page.getByRole('button', {name: 'Read resource results', exact: true}).click();
    const replacementRows = page.getByRole('region', {name: 'Application resource results'}).getByRole('row');
    await replacementRows.nth(2).waitFor();
    check((await replacementRows.allTextContents()).slice(1).every(row => row.includes('Change committed') && row.includes('Applied')), 'Reviewed replacement did not apply both resources');
    const replaced = await api('/api/v2/monitors/activated-monitor');
    check(replaced.spec.enabled === false && replaced.metadata.name === concurrent.metadata.name && JSON.stringify(replaced.spec.notifications.blue) === JSON.stringify(blue), 'Reviewed replacement changed enablement, concurrent name or blue configuration');
    check(replaced.spec.notifications.red.groupRef !== observed.spec.notifications.red.groupRef && replaced.spec.notifications.red.groupRef === replaced.spec.notifications.green.groupRef, 'Reviewed red/green rules were not replaced together');
    check(writes.filter(write => write.path === `/api/v2/operations/${replacementOperationID}/activate`).length === 1, 'Reviewed replacement activation repeated');
    await page.getByRole('button', {name: 'Close this import', exact: true}).click();

    stage = 'recipient contextual setup rejects an incompatible destination';
    await api('/api/v2/notification-endpoints', 'POST', {apiVersion: 'cpra.io/v2', kind: 'NotificationEndpoint', metadata: {id: 'local-log-only'}, spec: {type: 'log', config: {}}});
    await api('/api/v2/recipients', 'POST', {apiVersion: 'cpra.io/v2', kind: 'Recipient', metadata: {id: 'local-log-recipient'}, spec: {endpointRefs: ['local-log-only']}});
    await visit('/recipients/local-log-recipient');
    await page.getByRole('link', {name: 'Set up notifications', exact: true}).click();
    await page.getByRole('combobox', {name: /^Choose monitor/}).selectOption('activated-monitor');
    await page.getByLabel('Notification group name', {exact: true}).fill('Incompatible destination');
    const beforeIncompatible = writes.length;
    await page.getByRole('button', {name: 'Prepare notification setup', exact: true}).click();
    await page.getByRole('alert').filter({hasText: 'This recipient has no webhook endpoint.'}).waitFor();
    check(writes.length === beforeIncompatible && await page.getByRole('region', {name: 'Collection preview', exact: true}).count() === 0, 'Incompatible recipient was adopted or submitted');
    check(!((await page.locator('body').innerText()).includes(marker)), 'Contextual setup exposed protected destination data');
    stage = 'sign out';
    await page.getByRole('button', { name: 'Sign out', exact: true }).click();
    await page.getByLabel('Bearer token').waitFor();
    await drainCaptures();
    check(errors.length === 0, 'Browser reported an uncaught error');
    process.stdout.write(JSON.stringify({ operationID, guidedOperationID, replacementOperationID, resultID: original.executionResult.summary.resultID, initialOperationActivations: activations.length, interruptedReads,
      checks: ['real-file-import', 'file-normalization-profile', 'whole-collection-validation', 'explicit-confirmation', 'one-bodyless-activation', 'dependency-ordered-application', 'separate-decision-and-controller', 'source-attribution', 'guided-notification-collection', 'guided-explicit-activation', 'conditional-rule-diff', 'concurrent-edit-conflict', 'blue-rule-preserved', 'monitor-and-recipient-context', 'incompatible-recipient-rejected', 'secret-redaction', 'ephemeral-session', 'sign-out-clear'] }));
  } catch (error) {
    const visible = lastPage ? (await lastPage.locator('body').innerText()).slice(0, 4000).replaceAll(operator, '[token]').replaceAll(marker, '[private]') : '';
    throw new Error(`${stage}: ${error.message}\nVisible page: ${visible}`);
  }
  finally { await browser.close(); }
})().catch(error => { process.stderr.write(error.message + '\n'); process.exitCode = 1; });

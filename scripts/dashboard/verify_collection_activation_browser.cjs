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
  try {
    const context = await browser.newContext({ viewport: { width: 1440, height: 1100 } });
    const page = await context.newPage(); page.setDefaultTimeout(20_000);
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
    await page.getByRole('heading', { name: 'Import configuration files', exact: true }).waitFor();
    const chooser = page.waitForEvent('filechooser');
    await page.getByLabel('Configuration files', { exact: true }).click();
    await (await chooser).setFiles(files);
    await page.getByRole('button', { name: 'Create inactive operation', exact: true }).click();
    await page.getByRole('alert').filter({ hasText: 'Inactive operation created.' }).waitFor();
    await page.getByRole('button', { name: 'Upload remaining resources', exact: true }).click();
    await page.getByRole('alert').filter({ hasText: 'All resources are staged.' }).waitFor();
    stage = 'sealed validation';
    await page.getByRole('button', { name: 'Validate whole collection', exact: true }).click();
    await page.getByRole('heading', { name: 'Original sealed validation result', exact: true }).waitFor();
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
    let captureTimer;
    try { await Promise.race([Promise.all(pending), new Promise((_, reject) => { captureTimer = setTimeout(() => reject(new Error('Read capture did not finish')), 10_000); })]); }
    finally { clearTimeout(captureTimer); }
    check(!captureFailure, captureFailure);
    const original = reads.findLast(read => read.id === operationID && read.executionResult?.state === 'ready');
    check(original?.state === 'completed' && original.committed === 2 && original.applied === 2, 'Original operation did not complete both resources');
    check(original.normalizationProfile === 'cpra.file.base.v1', 'Original file normalization profile was lost');
    const activations = writes.filter(write => write.path.endsWith('/activate'));
    check(activations.length === 1 && activations[0].path === `/api/v2/operations/${operationID}/activate` && activations[0].empty, 'Activation was duplicated, redirected or included a body');
    const dom = await page.locator('body').innerText();
    check(!dom.includes(marker) && !dom.includes(operator), 'Secret or token in DOM');
    const storage = await page.evaluate(async () => JSON.stringify({ local: { ...localStorage }, session: { ...sessionStorage }, cookies: document.cookie, databases: await indexedDB.databases(), caches: await caches.keys() }));
    check([marker, operator, ...names].every(value => !storage.includes(value)), 'Private input persisted in browser storage');
    stage = 'sign out';
    await page.getByRole('button', { name: 'Sign out', exact: true }).click();
    check(await results.count() === 0, 'Sign-out retained operation results');
    check(errors.length === 0, 'Browser reported an uncaught error');
    process.stdout.write(JSON.stringify({ operationID, resultID: original.executionResult.summary.resultID, activations: activations.length, interruptedReads,
      checks: ['real-file-import', 'file-normalization-profile', 'whole-collection-validation', 'explicit-confirmation', 'one-bodyless-activation', 'dependency-ordered-application', 'separate-decision-and-controller', 'source-attribution', 'secret-redaction', 'ephemeral-session', 'sign-out-clear'] }));
  } catch (error) { throw new Error(`${stage}: ${error.message}`); }
  finally { await browser.close(); }
})().catch(error => { process.stderr.write(error.message + '\n'); process.exitCode = 1; });

// Real file chooser, production Worker/WASM, embedded SPA, and normal TLS API.
// No route interception, application-module/Worker replacement, or downloads.
'use strict';
const path = require('node:path');
const { isDeepStrictEqual } = require('node:util');
const { chromium } = require(process.env.CPRA_BROWSER_MODULE);
const origin = process.env.CPRA_BROWSER_ORIGIN;
const files = JSON.parse(process.env.CPRA_BROWSER_FILES);
const marker = process.env.CPRA_BROWSER_PRIVATE_MARKER;
const names = files.map(file => path.basename(file));
const operator = 'main-operator-fixture-012345678901234567890123456789';
const reader = 'main-reader-fixture-012345678901234567890123456789';
const privateValues = [operator, reader, marker, ...names];
const checks = [];
let phase = 'browser launch';
function check(value, message) { if (!value) throw new Error(message); }
async function bounded(promise, milliseconds, message) {
  let timer;
  try {
    return await Promise.race([promise, new Promise((_, reject) => { timer = setTimeout(() => reject(new Error(message)), milliseconds); })]);
  } finally { clearTimeout(timer); }
}

(async () => {
  const started = Date.now();
  const browser = await chromium.launch({ headless: true, executablePath: process.env.CPRA_BROWSER_EXECUTABLE,
    args: [`--ignore-certificate-errors-spki-list=${process.env.CPRA_BROWSER_CERT_PIN}`] });
  try {
    const context = await browser.newContext({ viewport: { width: 1440, height: 1080 } });
    const page = await context.newPage();
    page.setDefaultTimeout(20_000);
    const errors = [], requests = [], writes = [], workerURLs = [], wasmResponses = [];
    const responseRecords = new Map();
    let responseFailure = '', checkedGetResponses = 0, interruptedGetResponses = 0;
    let observedPreview, observedValidation, sealedValidationReads = 0;
    let settlePreview;
    const previewCompletion = new Promise(resolve => { settlePreview = resolve; });
    // Keep inspector response bodies outside renderer lifetimes. This observes
    // the actual browser traffic without intercepting or replacing any request.
    const network = await context.newCDPSession(page);
    await network.send('Network.enable', { maxTotalBufferSize: 16 * 1024 * 1024, maxResourceBufferSize: 4 * 1024 * 1024, enableDurableMessages: true });
    network.on('Network.requestWillBeSent', event => {
      const { url, method } = event.request;
      if (!url.startsWith(origin + '/api/')) return;
      if (responseRecords.size >= 256 || responseRecords.has(event.requestId)) { responseFailure = 'API response accounting exceeded its bound or observed a redirect'; return; }
      let settle;
      const completion = new Promise(resolve => { settle = resolve; });
      responseRecords.set(event.requestId, { method, path: new URL(url).pathname, completion, settle });
    });
    let bufferedBytes = 0;
    function appendBody(record, encoded, initial = false) {
      const bytes = Buffer.from(encoded, 'base64');
      record.byteLength = (record.byteLength ?? 0) + bytes.length;
      bufferedBytes += bytes.length;
      if (record.byteLength > 4 * 1024 * 1024 || bufferedBytes > 16 * 1024 * 1024) {
        bytes.fill(0); responseFailure ||= 'API response observation exceeded its byte bound'; return;
      }
      record.chunks ??= [];
      if (initial) record.chunks.unshift(bytes); else record.chunks.push(bytes);
    }
    network.on('Network.responseReceived', event => {
      const record = responseRecords.get(event.requestId);
      if (!record) return;
      record.status = event.response.status; record.received = true;
      record.stream = bounded(network.send('Network.streamResourceContent', { requestId: event.requestId }), 10_000, 'API stream observation timed out')
        .then(result => appendBody(record, result.bufferedData, true))
        .catch(() => { record.streamUnavailable = true; });
    });
    network.on('Network.dataReceived', event => {
      const record = responseRecords.get(event.requestId);
      if (record && event.data) appendBody(record, event.data);
    });
    function finishResponse(event, interrupted) {
      const record = responseRecords.get(event.requestId);
      if (!record || record.finished) return;
      record.finished = true;
      if (interrupted) {
        record.failure = event.errorText;
        if (record.received && record.method === 'GET') interruptedGetResponses++;
      }
      let observationStage = 'body retrieval';
      void (async () => {
        await record.stream;
        let bytes;
        if (!record.streamUnavailable && record.chunks) bytes = Buffer.concat(record.chunks);
        else if (!interrupted) {
          const response = await bounded(network.send('Network.getResponseBody', { requestId: event.requestId }), 10_000, 'API body observation timed out');
          check(response.body.length <= 6 * 1024 * 1024, 'Encoded API response exceeded its bound');
          bytes = Buffer.from(response.body, response.base64Encoded ? 'base64' : 'utf8');
          response.body = '';
        } else return;
        try {
          observationStage = 'response privacy and size';
          check(bytes.byteLength <= 4 * 1024 * 1024, 'API response exceeded its byte bound');
          check(privateValues.every(value => !bytes.includes(Buffer.from(value))), 'Private input appeared in an API response');
          if (record.method === 'GET' && !interrupted) checkedGetResponses++;
          let body;
          if (record.path === '/api/v2/collections/preflight' || record.path.endsWith('/validation')) {
            try { body = JSON.parse(bytes.toString('utf8')); }
            catch { if (interrupted) return; throw new Error('Completed response was not JSON'); }
          }
          if (record.method === 'POST' && record.path === '/api/v2/collections/preflight') {
            observationStage = 'preflight result';
            check(record.status === 200, 'Preflight did not return HTTP 200');
            observedPreview = body;
          }
          if (record.method === 'GET' && record.path.endsWith('/validation') && record.status === 200) {
            observationStage = 'original validation seal';
            const seal = value => ({ operationID: value.operationID, identityFormat: value.identityFormat,
              contentDigest: value.contentDigest, itemCount: value.itemCount, summary: value.summary });
            if (observedValidation) check(isDeepStrictEqual(seal(observedValidation), seal(body)), 'Retained browsing changed the original validation seal');
            else observedValidation = body;
            sealedValidationReads++;
          }
        } finally { bytes.fill(0); }
      })().catch(() => { responseFailure ||= `API response failed ${observationStage} (${record.method} ${record.path})`; }).finally(() => {
        for (const bytes of record.chunks ?? []) { bufferedBytes -= bytes.length; bytes.fill(0); }
        record.chunks = [];
        record.settle();
        if (record.method === 'POST' && record.path === '/api/v2/collections/preflight') settlePreview();
      });
    }
    network.on('Network.loadingFailed', event => finishResponse(event, true));
    network.on('Network.loadingFinished', event => finishResponse(event, false));
    let createdWorkers = 0, closedWorkers = 0, sentFileName = false;
    page.on('pageerror', () => errors.push('uncaught page error'));
    page.on('worker', worker => {
      createdWorkers++; workerURLs.push(worker.url());
      worker.on('close', () => { closedWorkers++; });
    });
    context.on('request', request => {
      const url = request.url();
      requests.push({ method: request.method(), url, body: request.method() === 'GET' ? request.postData() : undefined });
      if (url.startsWith(origin + '/api/v2/') && request.method() !== 'GET') {
        const raw = request.postData() ?? '';
        // Selected names are never part of the source identity sent to CPRa.
        if (names.some(name => raw.includes(name))) sentFileName = true;
        writes.push({ method: request.method(), path: new URL(url).pathname });
      }
    });
    context.on('response', response => {
      if (new URL(response.url()).pathname.endsWith('.wasm')) wasmResponses.push({ status: response.status(), url: response.url() });
    });
    async function signIn(token) {
      await page.getByRole('heading', { name: 'Sign in to CPRa' }).waitFor();
      await page.getByLabel('Bearer token').fill(token);
      await page.getByRole('button', { name: 'Sign in', exact: true }).click();
      await page.getByRole('button', { name: 'Sign out', exact: true }).waitFor();
      await page.getByRole('heading', { name: 'Import configuration files', exact: true }).waitFor();
    }
    async function select(paths) {
      const chooser = page.waitForEvent('filechooser');
      await page.getByLabel('Configuration files', { exact: true }).click();
      await (await chooser).setFiles(paths);
    }
    async function workerCount(count) {
      for (let n = 0; n < 100; n++) {
        if (page.workers().length === count) return;
        await page.waitForTimeout(20);
      }
      throw new Error('Private worker did not reach the expected lifecycle state');
    }
    async function safeStorage() {
      const value = await page.evaluate(async () => ({
        local: { ...localStorage }, session: { ...sessionStorage }, cookie: document.cookie,
        databases: await indexedDB.databases(), caches: await caches.keys(),
        serviceWorkers: (await navigator.serviceWorker.getRegistrations()).length,
      }));
      const encoded = JSON.stringify(value);
      check(privateValues.every(privateValue => !encoded.includes(privateValue)), 'Private import data persisted in browser storage');
      check(value.databases.length === 0 && value.caches.length === 0 && value.serviceWorkers === 0, 'Import created a browser persistence mechanism');
    }
    async function redactedDOM() {
      const html = await page.content();
      check(![marker, operator, reader, 'identityKey', 'sourceFingerprint'].some(value => html.includes(value)), 'Private input or protocol material appeared in the DOM');
    }
    phase = 'operator sign-in';
    await page.goto(origin + '/import');
    await signIn(operator);
    const discovery = await page.evaluate(async token => {
      const response = await fetch('/api/v2/discovery', { headers: { Authorization: `Bearer ${token}` } });
      if (!response.ok) throw new Error('Cannot read discovery');
      return response.json();
    }, operator);
    const operations = Object.values(discovery.resourceOperations).flat();
    check(operations.includes('PreflightCollection') && operations.includes('ValidateOperation') && operations.includes('GetOperationValidation') && operations.includes('ActivateOperation'), 'This regression requires independently advertised validation and activation');

    // Reverse selection order verifies deterministic ordering before validating a
    // Monitor whose Credential occurs in the later file.
    phase = 'two-file local preparation';
    await select([files[1], files[0]]);
    await page.getByRole('heading', { name: 'Source-attributed preview', exact: true }).waitFor();
    await workerCount(1);
    const rows = page.getByRole('region', { name: 'Collection preview' }).getByRole('row');
    check(await rows.count() === 3, 'Preview did not contain both frozen resources');
    check(await rows.nth(1).getByRole('cell').allTextContents().then(cells => cells[0] === 'Monitor/browser-import-monitor' && cells[1] === names[0] && cells[2] === '1 / 1'), 'Monitor source attribution was incorrect');
    check(await rows.nth(2).getByRole('cell').allTextContents().then(cells => cells[0] === 'Credential/browser-import-secret' && cells[1] === names[1] && cells[2] === '1 / 1'), 'Credential source attribution was incorrect');
    check(writes.length === 0, 'Local file preparation performed an API mutation or preflight');
    await redactedDOM(); await safeStorage();
    check(await page.getByRole('button', { name: 'Create inactive operation', exact: true }).isEnabled(), 'Supported staging and validation could not create an inactive operation');
    checks.push('real picker and Worker/WASM parse YAML plus JSON; later-file dependency and local source attribution; no local-preparation HTTP writes');

    phase = 'server preflight';
    const previewResponse = page.waitForResponse(response => response.url() === origin + '/api/v2/collections/preflight' && response.request().method() === 'POST');
    await page.getByRole('button', { name: 'Preview on server', exact: true }).click();
    const response = await previewResponse;
    await bounded(previewCompletion, 10_000, 'Preflight response observation timed out');
    check(!responseFailure && observedPreview, responseFailure || `Preflight response body was unavailable: ${JSON.stringify([...responseRecords.values()].filter(record => record.path.endsWith('/preflight')).map(({ status, failure }) => ({ status, failure })))}`);
    const preview = observedPreview;
    check(response.status() === 200 && preview.valid === true && preview.itemCount === 2 && preview.items.length === 2, 'Actual server preflight rejected the complete collection');
    check(preview.items.every(item => item.outcome === 'create' && item.committed === false && item.applied === false), 'Preflight claimed committed or applied mutations');
    check(privateValues.every(value => !JSON.stringify(preview).includes(value)), 'Server preview reflected private input');
    await page.getByRole('alert').filter({ hasText: 'Server preview passed. This preview did not activate anything.' }).waitFor();
    check(await rows.nth(1).getByRole('cell').nth(3).innerText() === 'create' && await rows.nth(2).getByRole('cell').nth(3).innerText() === 'create', 'Server outcomes did not reach source-attributed rows');
    check(await page.getByRole('button', { name: 'Create inactive operation', exact: true }).isEnabled(), 'Successful preview disabled supported staging');
    await redactedDOM(); await safeStorage();
    checks.push('real authenticated TLS preflight validates cross-file graph; explicit false committed/applied; durable staging is separately available');

    phase = 'durable staged validation';
    await page.getByRole('button', { name: 'Create inactive operation', exact: true }).click();
    await page.getByRole('alert').filter({ hasText: 'Inactive operation created.' }).waitFor();
    await page.getByRole('button', { name: 'Upload remaining resources', exact: true }).click();
    await page.getByRole('alert').filter({ hasText: 'All resources are staged.' }).waitFor();
    await page.getByRole('button', { name: 'Validate whole collection', exact: true }).click();
    await page.getByRole('heading', { name: 'Original sealed validation result', exact: true }).waitFor();
    const validationRows = page.getByRole('region', { name: 'Original validation result' }).getByRole('row');
    check(await validationRows.count() === 3, 'Sealed validation did not render both original resources');
    check(await validationRows.nth(1).getByRole('cell').allTextContents().then(cells => cells[0] === '1' && cells[1] === 'Monitor/browser-import-monitor' && cells[2] === names[0] && cells[3] === '1 / 1' && cells[4] === 'create'), 'Original monitor validation source attribution was incorrect');
    check(await validationRows.nth(2).getByRole('cell').allTextContents().then(cells => cells[0] === '2' && cells[1] === 'Credential/browser-import-secret' && cells[2] === names[1] && cells[3] === '1 / 1' && cells[4] === 'create'), 'Original credential validation source attribution was incorrect');
    check(await page.getByRole('button', { name: 'Validate whole collection', exact: true }).isDisabled(), 'An original validation was offered for resubmission');
    check(await page.getByRole('button', { name: 'Review activation', exact: true }).isEnabled(), 'Successful validation did not enable explicit activation review');
    await redactedDOM(); await safeStorage();
    await bounded(Promise.all([...responseRecords.values()].filter(record => record.path.endsWith('/validation')).map(record => record.completion)), 10_000, 'Original validation observation timed out');
    check(!responseFailure && observedValidation && sealedValidationReads > 0, responseFailure || 'Original import validation was not observed before draft disposal');
    checks.push('inactive create and upload followed by exactly one asynchronous validation request; original sealed results display local source coordinates; no activation');

    phase = 'discard and malformed final file';
    await page.getByRole('button', { name: 'Close this import', exact: true }).click();
    await workerCount(0);
    check(await page.getByRole('heading', { name: 'Source-attributed preview', exact: true }).count() === 0, 'Discard retained the private preview');
    check(names.every(name => !page.url().includes(name)), 'Discard put source names in navigation');
    check(!(await page.content()).includes(names[0]) && !(await page.content()).includes(names[1]), 'Discard retained source names');

    phase = 'retained operation list and original result';
    const originalID = writes[4].path.split('/')[4];
    const beforeBrowsing = writes.length;
    const beforeResultReads = sealedValidationReads;
    await page.getByRole('link', { name: 'Operation progress', exact: true }).click();
    const operationRows = page.getByRole('region', { name: 'Retained operation page' }).getByRole('row');
    await page.getByRole('link', { name: originalID, exact: true }).waitFor();
    check(await operationRows.count() === 2, 'The owner list omitted or duplicated the original collection');
    check(await operationRows.nth(1).getByRole('cell').nth(1).innerText() === 'Collection · 2 resources · 2 uploaded', 'Collection list did not preserve the original inventory');
    await page.getByRole('link', { name: originalID, exact: true }).click();
    const resultView = page.getByRole('region', { name: 'Original collection validation', exact: true });
    await resultView.getByRole('button', { name: 'Read original validation result', exact: true }).waitFor();
    check(await page.getByRole('region', { name: 'Original validation resource results', exact: true }).count() === 0, 'Detail eagerly loaded retained validation rows');
    await resultView.getByRole('button', { name: 'Read original validation result', exact: true }).click();
    const retainedRows = page.getByRole('region', { name: 'Original validation resource results', exact: true }).getByRole('row');
    await retainedRows.nth(2).waitFor();
    check(await retainedRows.count() === 3, 'Retained result did not contain both original resources');
    check(await retainedRows.nth(1).getByRole('cell').allTextContents().then(cells => cells[0] === '1' && cells[1] === 'Monitor' && cells[2] === 'browser-import-monitor' && cells[3] === 'Create' && cells[5] === 'source.00000000000000000001' && cells[6] === '1' && cells[7] === '1'), 'Retained monitor source coordinates were incorrect');
    check(await retainedRows.nth(2).getByRole('cell').allTextContents().then(cells => cells[0] === '2' && cells[1] === 'Credential' && cells[2] === 'browser-import-secret' && cells[3] === 'Create' && cells[5] === 'source.00000000000000000002' && cells[6] === '1' && cells[7] === '1'), 'Retained credential source coordinates were incorrect');
    const retainedHTML = await page.content();
    check(names.every(name => !retainedHTML.includes(name)), 'Retained view reconstructed private file names');
    await redactedDOM(); await safeStorage();
    await bounded(Promise.all([...responseRecords.values()].filter(record => record.path.endsWith('/validation')).map(record => record.completion)), 10_000, 'Retained validation observation timed out');
    check(!responseFailure && sealedValidationReads > beforeResultReads, 'Retained detail did not reread the original validation seal');
    check(writes.length === beforeBrowsing, 'Operation browsing performed a mutation');
    if (process.env.CPRA_BROWSER_SCREENSHOT) {
      check(path.isAbsolute(process.env.CPRA_BROWSER_SCREENSHOT), 'Browser screenshot path must be absolute');
      await page.screenshot({ path: process.env.CPRA_BROWSER_SCREENSHOT, fullPage: true });
    }
    checks.push('owner operation list and explicit detail read preserve original inventory and source-token results after local draft disposal; browsing performs no mutation');
    await page.getByRole('link', { name: 'Import files', exact: true }).click();
    await page.getByRole('heading', { name: 'Import configuration files', exact: true }).waitFor();

    phase = 'malformed final file';
    const beforeMalformed = writes.length;
    await select(files);
    await page.getByRole('alert').filter({ hasText: 'The selected collection is invalid.' }).waitFor();
    await workerCount(0);
    const malformedMessage = await page.getByRole('alert').innerText();
    check(malformedMessage.includes(names[2]) && !malformedMessage.includes(marker), 'Malformed final file was not safely attributed');
    check(writes.length === beforeMalformed && await page.getByRole('button', { name: 'Preview on server', exact: true }).count() === 0, 'Malformed final file performed or enabled server work');
    await redactedDOM(); await safeStorage();
    checks.push('discard terminates worker and removes draft; malformed last file fails locally with safe file attribution and no additional API write');

    phase = 'sign-out and reader denial';
    await select([files[0], files[1]]);
    await page.getByRole('heading', { name: 'Source-attributed preview', exact: true }).waitFor();
    await workerCount(1);
    await page.getByRole('button', { name: 'Sign out', exact: true }).click();
    await page.getByRole('heading', { name: 'Sign in to CPRa', exact: true }).waitFor();
    await workerCount(0);
    check(names.every(name => !page.url().includes(name)), 'Sign-out put source names in navigation');
    const signedOut = await page.content();
    check(![...names, 'browser-import-monitor', 'browser-import-secret'].some(value => signedOut.includes(value)), 'Sign-out retained the previous private draft');
    await safeStorage();
    await signIn(reader);
    await page.getByRole('status').filter({ hasText: 'Import requires an operator identity.' }).waitFor();
    check(await page.getByLabel('Configuration files', { exact: true }).isDisabled(), 'Reader can prepare imports');
    check(await page.getByRole('button', { name: /^(Preview on server|Create inactive operation|Review activation)$/ }).count() === 0, 'Reader inherited import mutation controls');
    await workerCount(0); await redactedDOM(); await safeStorage();
    checks.push('sign-out terminates worker and clears visible draft; reader cannot select files or inherit import controls');

    check(createdWorkers === 3 && closedWorkers === 3, 'Worker lifecycle did not match valid, malformed, and signed-out selections');
    check(workerURLs.every(url => url.startsWith(origin + '/assets/') && url.includes('collection.worker-')), 'Import did not use the embedded production worker');
    check(wasmResponses.length >= 1 && wasmResponses.every(item => item.status === 200 && item.url.startsWith(origin + '/assets/collection-parser-')), 'Production Go/WASM asset was not served successfully');
    phase = 'bounded GET response accounting';
    await page.waitForLoadState('networkidle');
    for (let awaited = -1; awaited !== responseRecords.size;) {
      awaited = responseRecords.size;
      await bounded(Promise.all([...responseRecords.values()].map(record => record.completion)), 10_000, 'API response accounting timed out');
    }
    check(!responseFailure && checkedGetResponses > 0, responseFailure || 'API GET response accounting was empty');
    check(requests.every(request => privateValues.every(value => !request.url.includes(value) && !(request.body ?? '').includes(value))), 'Private values reached a URL or GET body');
    check(requests.every(request => request.url.startsWith(origin + '/')), 'Browser import contacted an external origin');
    check(!sentFileName, 'A local file name reached an API request');
    check(writes.length === 5 && writes[0].path === '/api/v2/collections/preflight' && writes[0].method === 'POST' &&
      writes[1].path === '/api/v2/collections/prepare' && writes[1].method === 'POST' &&
      writes[2].path === '/api/v2/operations' && writes[2].method === 'POST' &&
      /\/api\/v2\/operations\/op\.[^.]+\.\d{20}\/items$/.test(writes[3].path) && writes[3].method === 'PUT' &&
      writes[4].path === writes[3].path.replace(/\/items$/, '/validate') && writes[4].method === 'POST', 'Import performed unexpected durable writes or retries');
    check(observedValidation?.summary?.valid === true && observedValidation.summary.count === 2 && observedValidation.itemCount === 2 && observedValidation.items.length === 2 && !observedValidation.nextCursor, 'Actual retained validation response was not observed');
    check(errors.length === 0, 'Uncaught browser error');
    checks.push('completed API GET response bodies inspected for private input; interrupted responses counted separately');
    console.log(JSON.stringify({ result: 'passed', operationID: observedValidation.operationID, resultID: observedValidation.summary.resultID, scope: 'normal startup, embedded SPA, real file chooser, production Worker/WASM, TLS and Raft; ephemeral preview, original staged validation, owner list and explicit retained-result browsing; no activation qualification', browser: browser.version(), node: process.version, playwright: require(`${process.env.CPRA_BROWSER_MODULE}/package.json`).version, elapsedMs: Date.now() - started, checks, writes, workers: { created: createdWorkers, closed: closedWorkers }, wasmResponses: wasmResponses.length, getResponses: { checked: checkedGetResponses, interrupted: interruptedGetResponses }, storage: 'no private values, IndexedDB, CacheStorage or ServiceWorker persistence observed' }, null, 2));
    phase = 'browser context shutdown';
    await context.close();
  } finally { await browser.close(); }
})().catch(error => { console.error(`Collection browser failed during ${phase}: ${error.message}`); process.exitCode = 1; });

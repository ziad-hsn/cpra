'use strict';
const { chromium } = require(process.env.CPRA_BROWSER_MODULE);
const origin = process.env.CPRA_BROWSER_ORIGIN;
const operationID = process.env.CPRA_BROWSER_OPERATION;
const operator = 'main-operator-fixture-012345678901234567890123456789';
const reader = 'main-reader-fixture-012345678901234567890123456789';
const check = (ok, message) => { if (!ok) throw new Error(message); };

(async () => {
  const browser = await chromium.launch({ headless: true, executablePath: process.env.CPRA_BROWSER_EXECUTABLE,
    args: [`--ignore-certificate-errors-spki-list=${process.env.CPRA_BROWSER_CERT_PIN}`] });
  let stage = 'launch';
  try {
    const context = await browser.newContext({ viewport: { width: 1440, height: 1000 } });
    const page = await context.newPage(); page.setDefaultTimeout(15_000);
    const errors = [], requests = [], observations = [], pending = [];
    let observationFailure = '', interruptedCaptures = 0;
    page.on('pageerror', () => errors.push('page error'));
    page.on('request', request => { if (request.url().startsWith(origin + '/api/')) requests.push({ method: request.method(), url: request.url() }); });
    const network = await context.newCDPSession(page);
    await network.send('Network.enable', { maxTotalBufferSize: 8 * 1024 * 1024, maxResourceBufferSize: 4 * 1024 * 1024, enableDurableMessages: true });
    const records = new Map();
    let buffered = 0;
    const append = (record, data, first = false) => {
      const bytes = Buffer.from(data, 'base64'); buffered += bytes.length;
      if (buffered > 8 * 1024 * 1024 || record.bytes + bytes.length > 4 * 1024 * 1024) { bytes.fill(0); observationFailure ||= 'Response capture exceeded its byte bound'; return; }
      record.bytes += bytes.length;
      if (first) record.chunks.unshift(bytes); else record.chunks.push(bytes);
    };
    network.on('Network.responseReceived', event => {
      if (new URL(event.response.url).pathname !== `/api/v2/operations/${operationID}` || event.response.status !== 200) return;
      if (records.size >= 64) { observationFailure ||= 'Response capture exceeded its count bound'; return; }
      const record = { chunks: [], bytes: 0 }; records.set(event.requestId, record);
      pending.push(new Promise(resolve => { record.complete = resolve; }));
      record.stream = network.send('Network.streamResourceContent', { requestId: event.requestId })
        .then(value => append(record, value.bufferedData, true)).catch(() => { record.unavailable = true; });
    });
    network.on('Network.dataReceived', event => { const record = records.get(event.requestId); if (record && event.data) append(record, event.data); });
    const finish = (event, interrupted) => {
      const record = records.get(event.requestId); if (!record || record.finished) return;
      record.finished = true;
      if (interrupted) interruptedCaptures++;
      void (async () => {
        await record.stream;
        let raw;
        if (!record.unavailable) raw = Buffer.concat(record.chunks);
        else {
          const body = await network.send('Network.getResponseBody', { requestId: event.requestId });
          check(body.body.length <= 6 * 1024 * 1024, 'Encoded response exceeded its bound');
          raw = Buffer.from(body.body, body.base64Encoded ? 'base64' : 'utf8'); body.body = '';
        }
        try {
          check(raw.length <= 4 * 1024 * 1024, 'Decoded response exceeded its bound');
          check(!raw.includes(Buffer.from(operator)) && !raw.includes(Buffer.from(reader)), 'Response exposed authentication');
          observations.push(JSON.parse(raw.toString('utf8')));
        } finally { raw.fill(0); }
      })().catch(() => { observationFailure ||= 'A response could not be captured or validated'; }).finally(() => {
        for (const chunk of record.chunks) { buffered -= chunk.length; chunk.fill(0); }
        record.chunks = [];
        record.complete();
      });
    };
    network.on('Network.loadingFinished', event => finish(event, false));
    network.on('Network.loadingFailed', event => finish(event, true));
    async function signIn(token) {
      await page.getByRole('heading', { name: 'Sign in to CPRa' }).waitFor();
      await page.getByLabel('Bearer token').fill(token);
      await page.getByRole('button', { name: 'Sign in', exact: true }).click();
      await page.getByRole('button', { name: 'Sign out', exact: true }).waitFor();
    }
    stage = 'owner observation';
    await page.goto(`${origin}/operations/${encodeURIComponent(operationID)}`);
    await signIn(operator);
    await page.getByRole('heading', { name: 'Completed', exact: true }).waitFor();
    const receipt = page.getByRole('region', { name: 'Operation receipt' });
    check((await receipt.innerText()).includes('Committed: 1 · Applied: 1'), 'Receipt did not expose authoritative counts');
    check(!(await receipt.innerText()).includes('Credential/staged-secret'), 'Receipt retained a duplicate resource page');
    await page.getByRole('button', { name: 'Read resource results', exact: true }).click();
    const results = page.getByRole('region', { name: 'Application resource results' });
    await results.waitFor();
    check((await results.innerText()).includes('Credential/staged-secret'), 'Original resource missing');
    check((await results.innerText()).includes('Change committed') && (await results.innerText()).includes('Applied'), 'Decision and child outcome not shown');
    await page.getByRole('button', { name: 'Hide resource results', exact: true }).click();
    check(await results.count() === 0, 'Hide kept resource rows');
    await page.getByRole('button', { name: 'Read resource results', exact: true }).click();
    await results.waitFor();
    let timer;
    try { await Promise.race([Promise.all(pending), new Promise((_, reject) => { timer = setTimeout(() => reject(new Error('Response capture did not finish')), 10_000); })]); }
    finally { clearTimeout(timer); }
    check(!observationFailure, observationFailure);
    check(observations.length >= 3, `Missing real API observation (${observations.length}/${records.size})`);
    const original = observations[0];
    check(original.executionResult?.state === 'ready', 'Original retained result unavailable');
    for (const observation of observations) {
      check(observation.id === operationID && JSON.stringify(observation.executionResult) === JSON.stringify(original.executionResult), 'Immutable result changed');
      check(JSON.stringify(observation.items) === JSON.stringify(original.items), 'Original rows changed');
    }
    stage = 'reader isolation';
    await page.getByRole('button', { name: 'Sign out', exact: true }).click();
    check(await results.count() === 0, 'Sign-out retained resource rows');
    await signIn(reader);
    await page.getByText('This operation receipt is unavailable. This alone does not prove that the change was rejected.', { exact: true }).waitFor();
    check(await page.getByRole('region', { name: 'Collection application results' }).count() === 0, 'Reader observed another actor result');
    const text = await page.locator('body').innerText();
    check(!text.includes(operator) && !text.includes(reader), 'Authentication value appeared in DOM');
    check(requests.every(request => request.method === 'GET' && !request.url.includes(operator) && !request.url.includes(reader)), 'Result browsing made a mutation or leaked authentication');
    check(errors.length === 0, 'Browser reported an uncaught error');
    process.stdout.write(JSON.stringify({ operationID, resultID: original.executionResult.summary.resultID, reads: observations.length, interruptedCaptures, writes: 0, checks: ['normal-owner-result', 'separate-decision-and-controller', 'bounded-explicit-read', 'original-seal-refresh', 'sign-out-clear', 'reader-isolation', 'GET-only'] }));
  } catch (error) { throw new Error(`${stage}: ${error.message}`); }
  finally { await browser.close(); }
})().catch(error => { process.stderr.write(error.message + '\n'); process.exitCode = 1; });

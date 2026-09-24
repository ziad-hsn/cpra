'use strict';
// Real dedicated Worker and production Go/WASM bridge. No provider/API endpoints.
const fs = require('node:fs'), path = require('node:path'), crypto = require('node:crypto'), zlib = require('node:zlib');
const { pathToFileURL } = require('node:url'), { execFileSync } = require('node:child_process');
const root = path.resolve(__dirname, '../..'), output = path.join(root, 'bin/verification/browser-import-worker');
const check = (condition, message) => { if (!condition) throw new Error(message); };
function processRSS(pid) {
  const rows = [];
  for (const name of fs.readdirSync('/proc')) if (/^\d+$/.test(name)) try {
    const status = fs.readFileSync(`/proc/${name}/status`, 'utf8');
    rows.push({ pid: Number(name), parent: Number(status.match(/^PPid:\s*(\d+)/m)?.[1]), rss: Number(status.match(/^VmRSS:\s*(\d+)/m)?.[1] ?? 0) * 1024 });
  } catch {}
  const ids = new Set([pid]);
  for (let n = 0; n < 8; n++) for (const row of rows) if (ids.has(row.parent)) ids.add(row.pid);
  return rows.filter(row => ids.has(row.pid)).reduce((sum, row) => sum + row.rss, 0);
}
(async () => {
  fs.mkdirSync(output, { recursive: true, mode: 0o700 });
  const go = process.env.CPRA_BROWSER_GO || 'go';
  const native = path.join(output, 'native-parser');
  const env = { ...process.env, GOWORK: 'off', GOTOOLCHAIN: 'local', GOFLAGS: '', GOEXPERIMENT: '', CGO_ENABLED: '0', GOOS: '', GOARCH: '' };
  execFileSync(go, ['build', '-trimpath', '-mod=readonly', '-buildvcs=false', '-pgo=off', '-o', native, path.join(__dirname, 'collectionwasm/native_fixture.go')], { cwd: path.join(root, 'sdk/go'), env });
  const { createServer } = await import(pathToFileURL(path.join(root, 'dashboard/node_modules/vite/dist/node/index.js')).href);
  let failParserAsset = false;
  const server = await createServer({ root: path.join(root, 'dashboard'), configFile: false, server: { host: '127.0.0.1', port: 0, hmr: false }, logLevel: 'error', plugins: [{ name: 'private-worker-proof', configureServer(server) {
    server.middlewares.use((req, res, next) => { if (failParserAsset && req.url.endsWith('/collection-parser.wasm')) { failParserAsset = false; res.writeHead(503); res.end(); return; } next(); });
    server.middlewares.use('/__collection-proof', (_req, res) => { res.setHeader('Content-Type', 'text/html'); res.end('<!doctype html><title>Private import worker qualification</title>'); });
  } }] });
  await server.listen();
  const origin = `http://127.0.0.1:${server.httpServer.address().port}`;
  const { chromium } = require(process.env.CPRA_BROWSER_MODULE);
  const launched = await chromium.launchServer({ executablePath: process.env.CPRA_BROWSER_EXECUTABLE, headless: true });
  const browser = await chromium.connect(launched.wsEndpoint());
  try {
    const page = await browser.newPage(), requests = [], errors = [];
    page.on('request', request => requests.push(new URL(request.url()).pathname));
    page.on('pageerror', error => errors.push(error.message));
    await page.goto(origin + '/__collection-proof');
    await page.evaluate(async () => {
      globalThis.collectionAPI = await import('/src/import/collection.ts');
      globalThis.ticks = 0; setInterval(() => globalThis.ticks++, 10);
      globalThis.makeFile = source => {
        const file = new File([source.text], source.name.split('/').at(-1));
        if (source.name.includes('/')) Object.defineProperty(file, 'webkitRelativePath', { value: source.name });
        return file;
      };
    });
    const cases = [];
    const sources = [
      { name: 'service-b/monitors.json', text: '{"apiVersion":"cpra.io/v2","kind":"Monitor","metadata":{"id":"large-json"},"spec":{"check":{"interval":"60s","timeout":"5s","maxFailures":9007199254740993,"driver":{"type":"http","config":{"url":"https://example.test","headers":{"__proto__":"literal-value"}}}}}}' },
      { name: 'service-a/monitors.yaml', text: 'monitors:\n  - name: legacy\n    pulse_check:\n      type: http\n      interval: 60s\n      timeout: 5s\n      retries: 3\n      max_failures: 4\n      config:\n        url: https://example.test\n        retries: 0\n---\napiVersion: cpra.io/v2\nkind: Recipient\nmetadata:\n  id: oncall\nspec:\n  endpointRefs: [mail]\n' },
    ];
    const expected = [...sources].sort((a, b) => Buffer.compare(Buffer.from(a.name), Buffer.from(b.name))).flatMap(source => {
      const value = JSON.parse(execFileSync(native, { input: source.text, encoding: 'utf8' })); check(value.valid, 'Native fixture rejected'); return value.items;
    });
    const parity = await page.evaluate(async ({ sources, expected }) => {
      const started = performance.now(), progress = [];
      const frozen = await collectionAPI.BrowserCollection.prepare(sources.map(makeFile), { onProgress: value => progress.push(value) });
      try {
        const summary = frozen.summary, items = await frozen.page();
        const body = await frozen.preflightBody(), bytes = body.take(), wire = new TextDecoder().decode(bytes);
        const matches = expected.every(raw => wire.includes('"resource":' + raw));
        const create = await frozen.createBody(), createBytes = create.take(), identity = JSON.parse(new TextDecoder().decode(createBytes));
        const safe = JSON.stringify({ summary, items, progress, holder: { ...frozen }, body: { ...body } });
        const privateAbsent = !safe.includes(identity.identityKey) && !safe.includes(identity.sourceFingerprint) && !safe.includes('https://example.test') && !safe.includes('service-a/');
        bytes.fill(0); createBytes.fill(0);
        return { summary, items, progress, matches, privateAbsent, localNames: [frozen.sourceName(1), frozen.sourceName(2)], elapsedMs: performance.now() - started };
      } finally { frozen.close(); }
    }, { sources, expected });
    check(parity.matches && parity.privateAbsent && parity.summary.itemCount === 3, 'Shared parser parity/privacy failed');
    check(parity.localNames[0] === 'service-a/monitors.yaml' && parity.items[0].source.token.endsWith('01'), 'Source ordering/attribution changed through worker clone');
    cases.push({ name: 'native-shared-decoder-parity', ...parity, localNames: undefined });
    const fresh = await page.evaluate(async sources => {
      const first = await collectionAPI.BrowserCollection.prepare(sources.map(makeFile));
      const before = first.summary.contentDigest; first.close();
      const second = await collectionAPI.BrowserCollection.prepare(sources.map(makeFile));
      try { return second.summary.contentDigest !== before; } finally { second.close(); }
    }, sources); check(fresh, 'Fresh file selection reused an identity');
    const valid = '{"apiVersion":"cpra.io/v2","kind":"Credential","metadata":{"id":"secret"},"spec":{"value":"private-fixture-value"}}';
    for (const [name, badSources] of [
      ['malformed-trailing-file', [{ name: 'a.json', text: valid }, { name: 'z.yaml', text: 'spec: [private-fixture-value\n' }]],
      ['duplicate-identities', [{ name: 'a.json', text: valid }, { name: 'b.json', text: valid }]],
      ['duplicate-json-key', [{ name: 'a.json', text: valid.replace('"value":', '"value":"first","value":') }]],
      ['duplicate-yaml-key', [{ name: 'a.yaml', text: 'kind: Credential\nkind: Credential\n' }]],
      ['unsupported-alias', [{ name: 'a.yaml', text: 'apiVersion: cpra.io/v2\nkind: Credential\nmetadata: &x {id: test}\nspec: *x\n' }]],
      ...['0.99999999999999999', '1e-999', '9007199254740993.0'].flatMap(number => [
        ['json-integer-control-' + number, [{ name: 'a.json', text: sources[0].text.replace('9007199254740993', number) }]],
        ['yaml-integer-control-' + number, [{ name: 'a.yaml', text: 'apiVersion: cpra.io/v2\nkind: Monitor\nmetadata: {id: numeric}\nspec:\n  check:\n    interval: 60s\n    timeout: 5s\n    maxFailures: ' + number + '\n    driver:\n      type: http\n      config: {url: https://example.test}\n' }]],
      ]),
    ]) {
      if (name.includes('-integer-control-')) {
        check(!JSON.parse(execFileSync(native, { input: badSources[0].text, encoding: 'utf8' })).valid, 'Native integer validation rounded input');
      }
      const result = await page.evaluate(async files => { try { const value = await collectionAPI.BrowserCollection.prepare(files.map(makeFile)); value.close(); return { accepted: true }; } catch (error) { return { accepted: false, code: error.code, message: error.message, location: error.location }; } }, badSources);
      check(!result.accepted && !JSON.stringify(result).includes('private-fixture-value'), `Invalid selection accepted or leaked: ${name}`); cases.push({ name, ...result });
    }
    async function largeCase(escaped) {
      await page.evaluate(escaped => {
        const target = 64 * 1024 * 1024;
        if (!escaped) {
          const count = 128, base = id => `{"apiVersion":"cpra.io/v2","kind":"Credential","metadata":{"id":"large-${id}"},"spec":{"value":"`;
          const overhead = Array.from({ length: count }, (_, i) => base(i) + '"}}').join(',').length + 2;
          const content = 'x'.repeat(Math.floor((target - overhead) / count));
          let text = '[' + Array.from({ length: count }, (_, i) => base(i) + content + '"}}').join(',') + ']'; text += ' '.repeat(target - text.length);
          globalThis.largeFile = new File([text], 'large.json');
        } else {
          const count = 640, base = id => `apiVersion: cpra.io/v2\nkind: Credential\nmetadata:\n  id: escaped-${id}\nspec:\n  value: "`;
          const overhead = Array.from({ length: count }, (_, i) => base(i) + '"\n').join('---\n').length;
          const content = '<'.repeat(Math.floor((target - overhead) / count));
          let text = Array.from({ length: count }, (_, i) => base(i) + content + '"\n').join('---\n'); text += ' '.repeat(target - text.length);
          globalThis.largeFile = new File([text], 'escaped.yaml');
        }
      }, escaped);
      const baseline = processRSS(launched.process().pid); let peak = baseline;
      const timer = setInterval(() => { peak = Math.max(peak, processRSS(launched.process().pid)); }, 25);
      let result;
      try { result = await page.evaluate(async () => {
        const started = performance.now(), before = ticks;
        const frozen = await collectionAPI.BrowserCollection.prepare([largeFile]);
        try {
          const summary = frozen.summary; let count = 0, chunks = 0, maximumChunkBytes = 0;
          while (count < summary.itemCount) {
            const body = await frozen.uploadBody(count); const bytes = body.take();
            maximumChunkBytes = Math.max(maximumChunkBytes, bytes.byteLength); count = body.nextOffset; chunks++; bytes.fill(0);
          }
          const page = await frozen.page();
          return { summary, chunks, maximumChunkBytes, uploadedItems: count, pageItems: page.length, elapsedMs: performance.now() - started, mainThreadTicks: ticks - before };
        } finally { frozen.close(); }
      }); } finally { clearInterval(timer); }
      check(result.summary.sourceBytes === 64 * 1024 * 1024 && result.uploadedItems === (escaped ? 640 : 128) && result.maximumChunkBytes <= 4 * 1024 * 1024 && result.mainThreadTicks > 1, 'Large source workload failed');
      if (escaped) check(result.summary.normalizedBytes > 350 * 1024 * 1024, 'Escaped workload did not measure real expansion');
      return { name: escaped ? '64MiB-escaped-expansion' : '64MiB-ordinary-source', ...result, processTreeRSSBaseline: baseline, processTreeRSSPeak: peak };
    }
    cases.push(await largeCase(false)); cases.push(await largeCase(true));
    const cancellation = await page.evaluate(async () => {
      const controller = new AbortController(), start = performance.now(); let parsingObserved = false;
      const result = collectionAPI.BrowserCollection.prepare([largeFile], { signal: controller.signal, onProgress(progress) {
        if (progress.phase === 'parsing') { parsingObserved = true; controller.abort(); }
      } }).then(value => { value.close(); return 'unexpected-success'; }, error => error.code);
      return { result: await result, parsingObserved, elapsedMs: performance.now() - start };
    }); check(cancellation.result === 'cancelled' && cancellation.parsingObserved, 'Worker cancellation did not interrupt active parsing');
    failParserAsset = true;
    const initializationFailure = await page.evaluate(async text => {
      try { const frozen = await collectionAPI.BrowserCollection.prepare([new File([text], 'valid.json')]); frozen.close(); return 'unexpected-success'; }
      catch (error) { return error.code; }
    }, valid);
    check(initializationFailure === 'unavailable' && failParserAsset === false, 'Failed parser asset initialization was not explicit');
    cases.push({ name: 'asset-initialization-failure', code: initializationFailure });
    const storage = await page.evaluate(async () => ({ local: localStorage.length, session: sessionStorage.length, indexedDB: (await indexedDB.databases()).length }));
    check(Object.values(storage).every(value => value === 0), 'Worker persisted data');
    check(requests.every(url => !url.startsWith('/api/') && url !== '/metrics'), 'Parser performed an API request');
    check(errors.length === 0, 'Uncaught browser errors');
    const wasm = fs.readFileSync(path.join(root, 'dashboard/src/import/generated/collection-parser.wasm'));
    const evidence = { result: 'passed', scope: 'Production dedicated Worker and Go/WASM parser via isolated Vite server; no import UI, API activation or server-assisted reselection/resume qualification.', browser: browser.version(), node: process.version, wasmBytes: wasm.length, gzipBytes: zlib.gzipSync(wasm).length, wasmSHA256: crypto.createHash('sha256').update(wasm).digest('hex'), cases, cancellation, storage, apiRequests: 0, memoryBoundary: 'Aggregate RSS of whole Chrome process tree, including shared pages counted multiple times; not private worker memory.' };
    fs.writeFileSync(path.join(output, 'result.json'), JSON.stringify(evidence, null, 2) + '\n', { mode: 0o600 });
    console.log(JSON.stringify({ result: 'passed', cases: cases.map(test => ({ name: test.name, elapsedMs: test.elapsedMs, normalizedBytes: test.summary?.normalizedBytes, processTreeRSSPeak: test.processTreeRSSPeak })), cancellation, wasmBytes: evidence.wasmBytes }));
  } finally { await browser.close(); await launched.close(); await server.close(); }
})().catch(error => { console.error(error.message); process.exitCode = 1; });

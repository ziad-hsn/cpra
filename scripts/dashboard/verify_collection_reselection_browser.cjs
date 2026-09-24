'use strict';
const path = require('node:path');
const { chromium } = require(process.env.CPRA_BROWSER_MODULE);
const origin = process.env.CPRA_BROWSER_ORIGIN;
const files = JSON.parse(process.env.CPRA_BROWSER_FILES);
const operationID = process.env.CPRA_BROWSER_OPERATION;
const token = 'main-operator-fixture-012345678901234567890123456789';
const privateValues = [token, 'PRIVATE-RESELECTION-MAIN-FIRST', 'PRIVATE-RESELECTION-MAIN-SECOND'];
const check = (ok, message) => { if (!ok) throw new Error(message); };

(async () => {
  const browser = await chromium.launch({ headless: true, executablePath: process.env.CPRA_BROWSER_EXECUTABLE,
    args: [`--ignore-certificate-errors-spki-list=${process.env.CPRA_BROWSER_CERT_PIN}`] });
  let stage = 'launch';
  try {
    const context = await browser.newContext({ viewport: { width: 1280, height: 1000 } });
    const page = await context.newPage(); page.setDefaultTimeout(20_000);
    const writes = [], errors = [];
    let privacyFailure = false;
    page.on('pageerror', () => errors.push('page error'));
    page.on('dialog', dialog => void dialog.accept());
    page.on('request', request => {
      if (!request.url().startsWith(origin + '/api/')) return;
      if (privateValues.some(value => request.url().includes(value))) privacyFailure = true;
      if (request.method() !== 'GET') {
        const body = request.postData() || '';
        if (files.some(file => body.includes(path.basename(file)))) privacyFailure = true;
        writes.push({ path: new URL(request.url()).pathname, method: request.method() });
      }
    });
    const signIn = async () => {
      await page.getByLabel('Bearer token').fill(token);
      await page.getByRole('button', { name: 'Sign in', exact: true }).click();
      await page.getByRole('main').getByRole('heading', { name: 'Operation progress', exact: true }).waitFor();
      await page.getByLabel('Original collection files', { exact: true }).waitFor();
    };
    const selectFiles = async () => {
      const chooser = page.waitForEvent('filechooser');
      await page.getByLabel('Original collection files', { exact: true }).click();
      await (await chooser).setFiles(files);
      await page.getByRole('button', { name: 'Verify original files', exact: true }).waitFor();
    };
    stage = 'refresh drops token and selection';
    await page.goto(`${origin}/operations/${encodeURIComponent(operationID)}`);
    await signIn(); await selectFiles();
    await page.reload();
    await page.getByRole('heading', { name: 'Sign in to CPRa', exact: true }).waitFor();
    await signIn();
    check(await page.getByRole('button', { name: 'Verify original files', exact: true }).count() === 0, 'Refresh retained source selection');
    check(writes.length === 0, 'Selecting files or signing in submitted work');
    stage = 'verify original files';
    await selectFiles();
    await page.getByRole('button', { name: 'Verify original files', exact: true }).click();
    await page.getByRole('button', { name: 'Resume original upload', exact: true }).waitFor();
    check(writes.filter(write => write.path.endsWith('/verify')).length === 1, 'Verification was repeated');
    check(writes.filter(write => write.method === 'PUT').length === files.length, 'Source parts lost empty source or duplicated input');
    stage = 'lost resume reply and explicit reconciliation';
    const resumePath = `${origin}/api/v2/operations/${operationID}/reselection/*/resume`;
    await page.route(resumePath, async route => {
      const response = await route.fetch({ maxRetries: 0 });
      check(response.status() === 202, 'Real server did not admit resume');
      await response.dispose();
      await route.abort('failed');
    }, { times: 1 });
    await page.getByRole('button', { name: 'Resume original upload', exact: true }).click();
    await page.getByRole('alert').filter({ hasText: 'The response was lost' }).waitFor();
    await page.getByRole('button', { name: 'Read attempt progress', exact: true }).click();
    await page.getByRole('region', { name: 'Operation receipt' }).getByText('Uploaded: 2', { exact: false }).waitFor();
    const resumes = writes.filter(write => write.path.endsWith('/resume'));
    check(resumes.length === 1 && resumes[0].path.startsWith(`/api/v2/operations/${operationID}/`), 'Uncertain mutation retried or changed operation');
    check(writes.every(write => write.path.includes(`/${operationID}/reselection`)), 'Reselection created or activated a different operation');
    check((await page.getByRole('region', { name: 'Operation receipt' }).innerText()).includes('Committed: 0'), 'File verification activated configuration');
    if (process.env.CPRA_BROWSER_CONTINUE === '1') {
      stage = 'explicit original validation and activation';
      const controls = page.getByRole('region', { name: 'Original collection controls' });
      await controls.getByRole('button', { name: 'Validate original collection', exact: true }).click();
      await page.getByRole('heading', { name: 'Validated', exact: true }).waitFor({ timeout: 45_000 });
      check(writes.filter(write => write.path.endsWith('/validate')).length === 1 && writes.every(write => !write.path.endsWith('/activate')), 'Validation was repeated or activated work');
      await controls.getByRole('button', { name: 'Review activation', exact: true }).click();
      const dialog = page.getByRole('alertdialog');
      await dialog.waitFor();
      check((await dialog.innerText()).includes(operationID), 'Confirmation lost original identity');
      await dialog.getByRole('button', { name: 'Keep reviewing', exact: true }).click();
      check(writes.every(write => !write.path.endsWith('/activate')), 'Dismissed review activated work');
      await controls.getByRole('button', { name: 'Review activation', exact: true }).click();
      await dialog.getByRole('button', { name: 'Confirm activation', exact: true }).click();
      await page.getByRole('heading', { name: 'Completed', exact: true }).waitFor({ timeout: 45_000 });
      const receipt = await page.getByRole('region', { name: 'Operation receipt' }).innerText();
      check(receipt.includes('Committed: 2') && receipt.includes('Applied: 2'), 'Original activation did not apply both resources');
    }
    stage = 'privacy and sign out';
    const dom = await page.locator('body').innerText();
    check(privateValues.every(value => !dom.includes(value)) && !privacyFailure, 'Private input escaped into DOM or URL');
    const storage = await page.evaluate(async () => JSON.stringify({ local: { ...localStorage }, session: { ...sessionStorage }, cookies: document.cookie, databases: await indexedDB.databases(), caches: await caches.keys() }));
    check([...privateValues, ...files].every(value => !storage.includes(value)), 'Private input persisted in browser storage');
    await page.getByRole('button', { name: 'Sign out', exact: true }).click();
    check(await page.getByRole('region', { name: 'Operation receipt' }).count() === 0 && errors.length === 0, 'Sign-out retained state or browser error');
    const activations = writes.filter(write => write.path.endsWith('/activate'));
    check(activations.length === (process.env.CPRA_BROWSER_CONTINUE === '1' ? 1 : 0), 'Explicit activation count changed');
    process.stdout.write(JSON.stringify({ operationID, resumes: resumes.length, activations: activations.length, checks: ['real-file-chooser', 'refresh-token-reentry', 'empty-source', 'original-upload', 'real-lost-reply', 'no-mutation-retry', 'no-implicit-activation', 'private-input-cleared'] }));
  } catch (error) { throw new Error(`${stage}: ${error.message}`); }
  finally { await browser.close(); }
})().catch(error => { process.stderr.write(error.message + '\n'); process.exitCode = 1; });

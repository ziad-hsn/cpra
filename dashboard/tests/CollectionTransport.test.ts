// @vitest-environment node
import { expect, it, vi } from 'vitest';
vi.hoisted(() => { vi.stubGlobal('window', { location: { origin: 'https://cpra.example' } }); });
import { DashboardSession, ManagementError } from '../src/api/session';
import { collectionCall, collectionOperation, collectionPermissions, collectionPreflight, prepareCollection, collectionValidationPage, collectionValidationPendingDelay, readCollectionValidation } from '../src/api/collectionOperations';
import { collectionIdentityFormat, fileNormalizationProfile, WorkerCollectionInventory } from '../src/api/collectionInventory';
import { operationView, supportedCollectionIdentity } from '../src/api/operations';
import { PrivateCollectionBody } from '../src/import/collection';
import { operationContracts } from '../src/api/generated';
const encoder = new TextEncoder();
const decoder = new TextDecoder();
const response = (value: unknown, status = 200) => new Response(JSON.stringify(value), { status });
const identity = { identityFormat: collectionIdentityFormat, itemCount: 1, contentDigest: 'a'.repeat(64) };
const receipt = { ...identity, id: 'original-1', state: 'uploading', uploaded: 0 };
const all = new Set(Object.keys(operationContracts));

it('binds operation progress to the original optional normalization profile', () => {
  const expected = { ...identity, normalizationProfile: fileNormalizationProfile };
  const profiled = { ...receipt, normalizationProfile: fileNormalizationProfile };
  expect(collectionOperation(profiled, expected).normalizationProfile).toBe(fileNormalizationProfile);
  expect(() => collectionOperation(receipt, expected)).toThrow();
  expect(() => collectionOperation(profiled, identity)).toThrow();
  expect(() => collectionOperation({ ...profiled, normalizationProfile: 'cpra.file.future.v2' }, expected)).toThrow();
  expect(supportedCollectionIdentity(operationView(profiled))).toEqual(expected);
  const future = operationView({ ...profiled, normalizationProfile: 'cpra.file.future.v2' });
  expect(future.normalizationProfile).toBe('cpra.file.future.v2');
  expect(supportedCollectionIdentity(future)).toBeUndefined();
  expect(supportedCollectionIdentity(operationView(receipt))).toEqual(identity);
});
async function fixture(handler: (url: string, init: RequestInit) => Promise<Response>, permissions = [...all], options: { maxResponseBytes?: number } = {}) {
  const sent = vi.fn(handler);
  const fetcher = vi.fn<typeof fetch>(async (url, init = {}) => String(url).endsWith('/self') ? new Headers(init.headers).has('Authorization') ? response({ principalId: 'operator', role: 'operator', permissions }) : response({}, 401) : sent(String(url), init));
  const session = new DashboardSession({ origin: 'https://cpra.example', fetch: fetcher, ...options });
  await session.discover(); await session.signIn('fixture-token'); return { session, sent };
}
function body(text: string) { return new PrivateCollectionBody(encoder.encode(text).buffer); }

it('preserves exact MAC-bound resource bytes through the non-CAS upload, clearing transferred buffers', async () => {
  const inventory = await WorkerCollectionInventory.create(1);
  const raw = encoder.encode('{"apiVersion":"cpra.io/v2","kind":"Credential","metadata":{"id":"secret"},"spec":{"value":"private"},"unused":18446744073709551615}');
  const source = await inventory.addSource(raw);
  inventory.addItem(raw, 'Credential/secret', 1, 1);
  const summary = await inventory.finish();
  const chunk = await inventory.uploadBody(0); const original = decoder.decode(chunk.bytes);
  const { session, sent } = await fixture(async (url, init) => {
    expect(url).toBe('https://cpra.example/api/v2/operations/original-1/items');
    expect(init.method).toBe('PUT'); expect(new Headers(init.headers).has('If-Match')).toBe(false);
    expect(init.redirect).toBe('error'); expect(init.credentials).toBe('omit');
    expect(decoder.decode(init.body as Uint8Array)).toBe(original);
    expect(decoder.decode(init.body as Uint8Array)).toContain(decoder.decode(raw));
    expect(decoder.decode(init.body as Uint8Array)).toContain(source);
    return response({ ...summary, id: 'original-1', state: 'uploading', uploaded: 1 });
  });
  const owned = chunk.bytes;
  try { await collectionCall(session, all, 'UploadOperation', summary, new PrivateCollectionBody(owned.buffer), 'original-1'); }
  finally { inventory.close(); }
  expect(sent).toHaveBeenCalledTimes(1); expect(owned.every(byte => byte === 0)).toBe(true);
});

it('retains ordinary resource CAS requirements while rejecting invalid collection routes and oversized bodies before sending', async () => {
  const { session, sent } = await fixture(async () => response(receipt));
  await expect(session.mutate('/api/v2/monitors/a', { operation: 'ReplaceMonitor', method: 'PUT', resourceVersion: '' })).rejects.toMatchObject({ reason: 'invalid' });
  const raw = new Uint8Array((4 << 20) + 1).fill(5);
  await expect(session.collectionRequest('UploadOperation', raw, 'original-1')).rejects.toMatchObject({ reason: 'invalid' });
  expect(raw.every(byte => byte === 0)).toBe(true);
  await expect(session.collectionRequest('UploadOperation', encoder.encode('{}'), '../other')).rejects.toMatchObject({ reason: 'invalid' });
  expect(sent).not.toHaveBeenCalled();
});

it('treats a lost preflight response as unavailable but a lost create response as uncertain, with no retries', async () => {
  const { session, sent } = await fixture(async () => { throw new Error('secret transport detail'); });
  await expect(collectionCall(session, all, 'PreflightCollection', identity, body('{}'))).rejects.toMatchObject({ reason: 'unavailable' });
  await expect(collectionCall(session, all, 'CreateOperation', identity, body('{}'), undefined, undefined, 'original-ticket')).rejects.toMatchObject({ reason: 'unconfirmed' });
  expect(sent).toHaveBeenCalledTimes(2);
});

it('requires exact format, digest and item count before trusting progress; strips payloads and future outcomes', () => {
  for (const field of ['identityFormat', 'contentDigest', 'itemCount']) {
    const value: Record<string, unknown> = { ...receipt }; delete value[field];
    expect(() => collectionOperation(value, identity)).toThrow(ManagementError);
    expect(() => collectionPreflight({ ...value, valid: true }, identity)).toThrow(ManagementError);
  }
  const value = collectionOperation({ ...receipt, state: 'future-state', items: [{ id: 'Credential/secret', outcome: 'future-outcome', message: 'secret', resource: 'secret', committed: false, applied: false }], identityKey: 'secret', admissionTicket: 'secret' }, identity);
  expect(value.state).toBe('unrecognized'); expect(value.items).toEqual([{ id: 'Credential/secret', outcome: 'unrecognized', committed: false, applied: false }]);
  expect(JSON.stringify(value)).not.toContain('"secret"');
  expect(() => collectionOperation({ ...receipt, uploaded: 2 }, identity)).toThrow();
});

it('marks successful HTTP mutations with mismatched receipts unconfirmed and keeps safe response handle', async () => {
  const { session } = await fixture(async () => new Response(JSON.stringify({ ...receipt, itemCount: 2 }), { status: 200, headers: { 'X-Operation-ID': 'original-1' } }));
  await expect(collectionCall(session, all, 'CreateOperation', identity, body('{}'), undefined, undefined, 'ticket')).rejects.toMatchObject({ reason: 'unconfirmed', response: { operationID: 'original-1' } });
});

it('requires both exact server discovery and identity permissions, consuming rejected bodies safely', async () => {
  const { session, sent } = await fixture(async () => response(receipt), ['GetCapabilities', 'PreflightCollection']);
  expect([...collectionPermissions(session, { resourceOperations: { Collection: ['PreflightCollection', 'CreateOperation'], Operation: ['UploadOperation'] } })]).toEqual(['PreflightCollection']);
  expect(collectionPermissions(session, { resources: ['Collection', 'Operation'] }).size).toBe(0);
  const raw = encoder.encode('private');
  await expect(collectionCall(session, new Set(), 'UploadOperation', identity, new PrivateCollectionBody(raw.buffer), 'original-1')).rejects.toMatchObject({ reason: 'forbidden' });
  expect(raw.every(byte => byte === 0)).toBe(true); expect(sent).not.toHaveBeenCalled();
});

it('uses prepare and create contracts while attaching only the supplied original admission ticket', async () => {
  const requests: Record<string, unknown>[] = [];
  const { session } = await fixture(async (url, init) => {
    requests.push(JSON.parse(decoder.decode(init.body as Uint8Array)));
    return response(url.endsWith('/prepare') ? { ticket: 'opaque-private-ticket', expiresAt: '2099-01-01T00:00:00Z' } : receipt);
  });
  const prepared = await prepareCollection(session, all, body('{"identityKey":"private-key"}'));
  expect(prepared.ticket).toBe('opaque-private-ticket');
  await collectionCall(session, all, 'CreateOperation', identity, body('{"identityKey":"private-key"}'), undefined, undefined, prepared.ticket);
  expect(requests).toEqual([{ identityKey: 'private-key' }, { identityKey: 'private-key', admissionTicket: 'opaque-private-ticket' }]);
});

it('accepts canonical admission tickets up to 128 KiB and rejects oversized tickets before create', async () => {
  let ticket = 'x'.repeat(128 << 10);
  const { session, sent } = await fixture(async url => response(url.endsWith('/prepare') ? { ticket, expiresAt: '2099-01-01T00:00:00Z' } : receipt));
  const prepared = await prepareCollection(session, all, body('{}'));
  expect(prepared.ticket).toHaveLength(128 << 10);
  await collectionCall(session, all, 'CreateOperation', identity, body('{}'), undefined, undefined, prepared.ticket);
  ticket += 'x';
  await expect(prepareCollection(session, all, body('{}'))).rejects.toMatchObject({ reason: 'invalid' });
  const before = sent.mock.calls.length;
  await expect(collectionCall(session, all, 'CreateOperation', identity, body('{}'), undefined, undefined, ticket)).rejects.toMatchObject({ reason: 'invalid' });
  expect(sent).toHaveBeenCalledTimes(before);
});

it('does not retain echoed write-only data in the unconfirmed receipt error', async () => {
  const { session } = await fixture(async () => response({ ...receipt, itemCount: 2, identityKey: 'never-retain-echoed-key', admissionTicket: 'never-retain-echoed-ticket' }));
  let error: unknown;
  try { await collectionCall(session, all, 'CreateOperation', identity, body('{}'), undefined, undefined, 'ticket'); } catch (caught) { error = caught; }
  expect(error).toBeInstanceOf(ManagementError); expect(JSON.stringify(error)).not.toContain('never-retain-echoed');
  expect((error as ManagementError).response).not.toHaveProperty('data');
});

const sealed = (count = 1, valid = true) => ({ ...identity, itemCount: count, operationID: 'original-1',
  summary: { resultID: '11111111-1111-1111-1111-111111111111', valid, summaryOnly: false, count, digest: 'b'.repeat(64), capabilitiesDigest: 'c'.repeat(64), finalizedAt: '2026-09-20T00:00:00Z', expiresAt: '2026-10-20T00:00:00Z', ...(valid ? { planID: '22222222-2222-2222-2222-222222222222', planDigest: 'd'.repeat(64) } : { issue: 'invalidResource' }) },
  items: Array.from({ length: Math.min(count, 100) }, (_, i) => ({ ordinal: i + 1, kind: 'Monitor', id: `monitor-${i + 1}`, source: 'source.00000000000000000001', sourceDocument: i + 1, sourceItem: 1, ...(valid ? { change: 'create' } : { issue: 'invalidResource' }) })), ...(count > 100 ? { nextCursor: 'cursor100' } : {}) });

it.each([[200, 'original-1', true], [202, 'original-1', false], [200, 'other-operation', false], [200, '', false]])('binds activation status %s and handle %s to the original bodyless request', async (status, handle, accepted) => {
  const { session, sent } = await fixture(async (url, init) => {
    expect(url).toBe('https://cpra.example/api/v2/operations/original-1/activate');
    expect(init.method).toBe('POST'); expect(init.body).toBeUndefined();
    expect(new Headers(init.headers).has('If-Match')).toBe(false);
    return new Response(JSON.stringify({ ...receipt, state: 'applying', executionResult: { state: 'pending' } }), { status, headers: { 'X-Operation-ID': handle } });
  });
  const result = collectionCall(session, all, 'ActivateOperation', identity, undefined, receipt.id);
  if (accepted) expect((await result).data).toMatchObject({ id: receipt.id, state: 'applying', executionResult: { state: 'pending' } });
  else await expect(result).rejects.toMatchObject({ reason: 'unconfirmed', response: { operationID: receipt.id } });
  expect(sent).toHaveBeenCalledTimes(1);
});

const executionSummary = { resultID: 'original-activation', uploadID: 'original-upload', planID: 'original-plan', planDigest: 'b'.repeat(64),
  digest: 'c'.repeat(64), outcome: 'completed', itemCount: 1, bytes: 64, finalizedAt: '2026-09-20T00:00:00Z', expiresAt: '2026-10-20T00:00:00Z',
  processed: 1, accepted: 0, unchanged: 1, conflicts: 0, dependencyBlocked: 0, unattempted: 0, childPending: 0, childApplied: 0, childFailed: 0, childSuperseded: 0, childInvalidated: 0 };
it.each([
  ['applying', { state: 'applying' }, true],
  ['pending execution', { state: 'canceled', executionResult: { state: 'pending' } }, true],
  ['ready metadata', { state: 'completed', executionResult: { state: 'ready', summary: executionSummary } }, true],
  ['expired execution', { state: 'invalidated', executionResult: { state: 'expired', summary: executionSummary } }, true],
  ['stale validated', { state: 'validated' }, false],
  ['unknown parent state', { state: 'future-state' }, false],
  ['missing terminal evidence', { state: 'completed' }, false],
  ['unknown execution state', { state: 'canceled', executionResult: { state: 'future-result' } }, false],
  ['missing ready summary', { state: 'completed', executionResult: { state: 'ready' } }, false],
  ['incoherent ready summary', { state: 'completed', executionResult: { state: 'ready', summary: { ...executionSummary, unchanged: 0 } } }, false],
  ['unexpected metadata cursor', { state: 'completed', nextCursor: 'unexpected', executionResult: { state: 'ready', summary: executionSummary } }, false],
] as const)('requires original activation admission evidence: %s', async (_name, disposition, accepted) => {
  const { session, sent } = await fixture(async () => new Response(JSON.stringify({ ...receipt, ...disposition, resource: 'PRIVATE-RESOURCE', message: 'PRIVATE-ERROR' }),
    { status: 200, headers: { 'X-Operation-ID': receipt.id } }));
  const pending = collectionCall(session, all, 'ActivateOperation', identity, undefined, receipt.id);
  if (accepted) expect((await pending).data).toMatchObject({ id: receipt.id, ...disposition });
  else {
    const error: unknown = await pending.catch(error => error);
    expect(error).toMatchObject({ reason: 'unconfirmed', response: { operationID: receipt.id } });
    expect(JSON.stringify(error)).not.toContain('PRIVATE-');
    expect((error as ManagementError).response).not.toHaveProperty('data');
  }
  expect(sent).toHaveBeenCalledTimes(1);
});

it('accepts only202 Operation from Validate, treats old synchronous booleans as uncertain and never retries', async () => {
  for (const status of [200, 202]) {
    const { session, sent } = await fixture(async () => response(status === 200 ? { ...identity, valid: true } : { ...receipt, state: 'validating' }, status));
    if (status === 200) await expect(collectionCall(session, all, 'ValidateOperation', identity, undefined, receipt.id)).rejects.toMatchObject({ reason: 'unconfirmed' });
    else expect((await collectionCall(session, all, 'ValidateOperation', identity, undefined, receipt.id)).data).toMatchObject({ state: 'validating' });
    expect(sent).toHaveBeenCalledTimes(1);
  }
});

it('retained result decoding requires original identity, immutable summary and advancing contiguous pages, stripping unknown payloads', () => {
  const expected = { ...identity, itemCount: 101, sourceCount: 1 };
  const firstRaw = sealed(101), first = collectionValidationPage(firstRaw, expected, receipt.id);
  const secondRaw = { ...firstRaw, items: [{ ...firstRaw.items[0], ordinal: 101, id: 'monitor-101', sourceDocument: 101, resource: 'private-resource', diagnostic: 'private-diagnostic' }], nextCursor: undefined, secret: 'private-secret' };
  const second = collectionValidationPage(secondRaw, expected, receipt.id, first.nextCursor, first);
  expect(second.items[0].ordinal).toBe(101); expect(JSON.stringify(second)).not.toContain('private-');
  expect(() => collectionValidationPage({ ...secondRaw, summary: { ...secondRaw.summary, digest: 'f'.repeat(64) } }, expected, receipt.id, first.nextCursor, first)).toThrow();
  expect(() => collectionValidationPage({ ...secondRaw, items: [{ ...secondRaw.items[0], ordinal: 100 }] }, expected, receipt.id, first.nextCursor, first)).toThrow();
  expect(() => collectionValidationPage({ ...secondRaw, nextCursor: first.nextCursor }, expected, receipt.id, first.nextCursor, first)).toThrow();
  expect(() => collectionValidationPage(secondRaw, expected, receipt.id, 'different', first)).toThrow();
  expect(() => collectionValidationPage({ ...firstRaw, operationID: 'other' }, expected, receipt.id)).toThrow();
  expect(() => collectionValidationPage({ ...firstRaw, contentDigest: 'f'.repeat(64) }, expected, receipt.id)).toThrow();
  expect(() => collectionValidationPage({ ...firstRaw, items: [...firstRaw.items, firstRaw.items[0]] }, expected, receipt.id)).toThrow();
  for (const field of ['source', 'sourceDocument', 'sourceItem']) expect(() => collectionValidationPage({ ...sealed(), items: [{ ...sealed().items[0], [field]: field === 'source' ? 'private/path.yaml' : 0 }] }, identity, receipt.id)).toThrow();
});

it('unknown retained vocabulary is redacted/read-only and cannot become an activation grant across later pages', () => {
  const raw = sealed(101), expected = { ...identity, itemCount: 101 };
  const first = collectionValidationPage({ ...raw, items: raw.items.map((item, i) => i ? item : { ...item, change: 'private-unknown-change' }) }, expected, receipt.id);
  expect(first.supported).toBe(false); expect(first.items[0].change).toBe('unrecognized'); expect(JSON.stringify(first)).not.toContain('private-unknown-change');
  const second = collectionValidationPage({ ...raw, items: [{ ...raw.items[0], ordinal: 101 }], nextCursor: undefined }, expected, receipt.id, first.nextCursor, first);
  expect(second.supported).toBe(false);
});

it('only a known pending409 with original operation header and bounded Retry-After permits read polling', () => {
  const error = new ManagementError('http', 'redacted', 409, { code: 'validationPending' }, undefined, { status: 409, operationID: receipt.id, requestID: '', resourceVersion: '', retryAfterMs: 12000 });
  expect(collectionValidationPendingDelay(error, receipt.id)).toBe(12000);
  expect(collectionValidationPendingDelay(error, 'other')).toBeUndefined();
  for (const other of [new ManagementError('http', 'redacted', 409), new ManagementError('http', 'redacted', 503, { code: 'validationPending' }, undefined, error.response), new ManagementError('http', 'redacted', 409, { code: 'validationPending' }, 'invalid', error.response), new ManagementError('http', 'redacted', 409, { code: 'validationPending' }, undefined, { ...error.response!, retryAfterMs: Infinity })]) expect(collectionValidationPendingDelay(other, receipt.id)).toBeUndefined();
});

it('validation page GET uses same-origin auth, a4MiB response ceiling, and clears session changes without mutation', async () => {
  const { session, sent } = await fixture(async (url, init) => {
    expect(url).toBe('https://cpra.example/api/v2/operations/original-1/validation?limit=100');
    expect(init.method).toBe('GET'); expect(init.body).toBeUndefined(); expect(init.redirect).toBe('error');
    expect(new Headers(init.headers).get('Authorization')).toBe('Bearer fixture-token');
    return new Response('ignored', { headers: { 'Content-Length': String((4 << 20) + 1) } });
  });
  await expect(readCollectionValidation(session, identity, receipt.id)).rejects.toMatchObject({ reason: 'too-large' });
  expect(sent).toHaveBeenCalledTimes(1);
  const other = await fixture(async () => { other.session.signOut(); return response(sealed()); });
  await expect(readCollectionValidation(other.session, identity, receipt.id)).rejects.toMatchObject({ reason: 'session-changed' });
});

it('keeps the original profile when a sealed validation result is used to read current progress', () => {
  const expected = { ...identity, normalizationProfile: fileNormalizationProfile };
  const validation = collectionValidationPage(sealed(), expected, receipt.id);
  expect(validation.normalizationProfile).toBe(fileNormalizationProfile);
  expect(collectionOperation({ ...receipt, normalizationProfile: fileNormalizationProfile, state: 'validated' }, validation).state).toBe('validated');
  expect(() => collectionOperation(receipt, validation)).toThrow();
});

it('does not turn a pending error or unavailable history into valid:false and allows an explicit summary-only rejection', async () => {
  const pending = await fixture(async () => new Response(JSON.stringify({ code: 'validationPending' }), { status: 409, headers: { 'X-Operation-ID': receipt.id, 'Retry-After': '5' } }));
  await expect(readCollectionValidation(pending.session, identity, receipt.id)).rejects.toMatchObject({ status: 409, problem: { code: 'validationPending' } });
  const raw = sealed(1, false);
  const summary = collectionValidationPage({ ...raw, summary: { ...raw.summary, summaryOnly: true, count: 0, issue: 'validationLimit' }, items: [] }, identity, receipt.id);
  expect(summary.summary).toMatchObject({ summaryOnly: true, valid: false, count: 0 });
  expect(() => collectionValidationPage({ ...raw, summary: { ...raw.summary, summaryOnly: true, valid: true, count: 0 }, items: [] }, identity, receipt.id)).toThrow();
});


it('intersects the validation request ceiling with a smaller configured session ceiling without rejecting a small result', async () => {
  let oversized = false;
  const { session, sent } = await fixture(async () => oversized ? new Response('ignored', { headers: { 'Content-Length': String((64 << 10) + 1) } }) : response(sealed()), [...all], { maxResponseBytes: 64 << 10 });
  expect((await readCollectionValidation(session, identity, receipt.id)).data.summary.valid).toBe(true);
  oversized = true;
  await expect(readCollectionValidation(session, identity, receipt.id)).rejects.toMatchObject({ reason: 'too-large' });
  expect(sent).toHaveBeenCalledTimes(2);
});

it('rejects malformed per-request response ceilings before sending', async () => {
  const { session, sent } = await fixture(async () => response(sealed()));
  for (const maxResponseBytes of [0, -1, NaN, Infinity, 1.5, (64 << 20) + 1]) await expect(session.get('/api/v2/operations/original-1/validation', { maxResponseBytes })).rejects.toMatchObject({ reason: 'invalid' });
  expect(sent).not.toHaveBeenCalled();
});

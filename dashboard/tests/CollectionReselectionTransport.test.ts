// @vitest-environment node
import { expect, it, vi } from 'vitest';
vi.hoisted(() => { vi.stubGlobal('window', { location: { origin: 'https://cpra.example' } }); });
import { DashboardSession, ManagementError, type ReselectionRequestOptions } from '../src/api/session';
import { createReselection, discardReselection, getReselection, reselectionOperations, reselectionStatus, resumeReselection, uploadReselectionSource, verifyReselection, type ReselectionStatus } from '../src/api/collectionReselection';
import { fileNormalizationProfile } from '../src/api/collectionInventory';

const id = 'original-operation';
const attempt = '12345678-1234-1234-1234-123456789abc';
const all = new Set<string>(reselectionOperations);
const encoder = new TextEncoder();
const decoder = new TextDecoder();
const status: ReselectionStatus = { id: attempt, operationID: id, normalizationProfile: fileNormalizationProfile, phase: 'uploading', sourceCount: 1, sourcesCompleted: 0,
  rawBytes: 0, nextSource: 1, nextOffset: 0, expiresAt: '2026-09-30T12:00:00Z', operationUploaded: 1 };
const finished = { ...status, sourcesCompleted: 1, nextSource: 0, nextOffset: 0, rawBytes: 5 };
const response = (value: unknown = status, code = 200, headers?: HeadersInit) => new Response(code === 204 ? null : JSON.stringify(value), { status: code, headers });
async function fixture(handler: (url: string, init: RequestInit) => Promise<Response>, permissions = [...all], options: { maxResponseBytes?: number; timeoutMs?: number } = {}) {
  const sent = vi.fn(handler);
  const fetcher = vi.fn<typeof fetch>(async (url, init = {}) => String(url).endsWith('/self') ? response({ principalId: 'operator', role: 'operator', permissions }) : sent(String(url), init));
  const session = new DashboardSession({ origin: 'https://cpra.example', fetch: fetcher, ...options });
  await session.signIn('tab-only-reselection-token');
  return { session, sent };
}

it('uses six fixed contracts with binary sources, an explicit empty end, no CAS or automatic next operation', async () => {
  const requests: { path: string; init: RequestInit; text?: string; media: string | null }[] = [];
  const { session, sent } = await fixture(async (url, init) => {
    requests.push({ path: new URL(url).pathname + new URL(url).search, init, text: init.body === undefined ? undefined : decoder.decode(init.body as Uint8Array), media: new Headers(init.headers).get('Content-Type') });
    if (init.method === 'DELETE') return response(undefined, 204);
    if (url.endsWith('/verify')) return response({ ...finished, phase: 'verifying' }, 202);
    if (url.endsWith('/resume')) return response({ ...finished, phase: 'transferring' }, 202);
    if (url.includes('/sources/')) return response(finished);
    return response(status, url.endsWith('/reselection') ? 201 : 200);
  });
  expect((await createReselection(session, all, id, 1)).data).toEqual(status);
  await getReselection(session, all, id, attempt);
  const input = encoder.encode('hello');
  await uploadReselectionSource(session, all, id, attempt, { source: 1, offset: 0, end: false, data: input });
  await uploadReselectionSource(session, all, id, attempt, { source: 1, offset: 5, end: true, data: new Uint8Array(0) });
  await verifyReselection(session, all, id, attempt);
  await resumeReselection(session, all, id, attempt);
  await discardReselection(session, all, id, attempt);
  expect(sent).toHaveBeenCalledTimes(7);
  expect(requests.map(item => [item.path, item.init.method, item.media])).toEqual([
    [`/api/v2/operations/${id}/reselection`, 'POST', 'application/json'],
    [`/api/v2/operations/${id}/reselection/${attempt}`, 'GET', null],
    [`/api/v2/operations/${id}/reselection/${attempt}/sources/1?offset=0&end=false`, 'PUT', 'application/octet-stream'],
    [`/api/v2/operations/${id}/reselection/${attempt}/sources/1?offset=5&end=true`, 'PUT', 'application/octet-stream'],
    [`/api/v2/operations/${id}/reselection/${attempt}/verify`, 'POST', null],
    [`/api/v2/operations/${id}/reselection/${attempt}/resume`, 'POST', null],
    [`/api/v2/operations/${id}/reselection/${attempt}`, 'DELETE', null],
  ]);
  expect(JSON.parse(requests[0].text!)).toEqual({ sourceCount: 1, normalizationProfile: fileNormalizationProfile });
  expect(requests[2].text).toBe('hello'); expect(requests[3].text).toBe('');
  for (const request of requests) {
    expect(request.init).toMatchObject({ redirect: 'error', cache: 'no-store', credentials: 'omit' });
    expect(new Headers(request.init.headers).get('Authorization')).toBe('Bearer tab-only-reselection-token');
    expect(new Headers(request.init.headers).has('If-Match')).toBe(false);
    expect(request.path).not.toContain('tab-only');
  }
  expect(input.every(byte => byte === 0)).toBe(true);
  expect(JSON.stringify(session.getSnapshot())).not.toContain('tab-only-reselection-token');
});

it('requires discovery and exact current permission, clearing source bytes even before dispatch', async () => {
  const { session, sent } = await fixture(async () => response(status), ['GetCollectionReselection']);
  const data = encoder.encode('PRIVATE');
  await expect(uploadReselectionSource(session, all, id, attempt, { source: 1, offset: 0, end: true, data })).rejects.toMatchObject({ reason: 'forbidden' });
  expect(data.every(byte => byte === 0)).toBe(true);
  await expect(getReselection(session, new Set(), id, attempt)).rejects.toMatchObject({ reason: 'forbidden' });
  expect(sent).not.toHaveBeenCalled();
});

it('rejects route injection, malformed coordinates and oversized parts before sending and clears each owned buffer', async () => {
  const { session, sent } = await fixture(async () => response(status));
  const base: ReselectionRequestOptions = { id, attempt, source: 1, offset: 0, end: true };
  for (const change of [ { id: '../private' }, { id: 'x?private=1' }, { id: 'x%2fy' }, { id: 'x'.repeat(129) }, { attempt: 'wrong' },
    { attempt: attempt.toUpperCase() }, { source: 0 }, { source: 1001 }, { source: 1.5 }, { offset: -1 }, { offset: NaN }, { offset: 64 << 20 } ]) {
    const data = encoder.encode('PRIVATE');
    await expect(session.reselectionRequest('UploadCollectionReselectionSource', { ...base, ...change, body: data })).rejects.toMatchObject({ reason: 'invalid' });
    expect(data.every(byte => byte === 0)).toBe(true);
  }
  const tooLarge = new Uint8Array((1 << 20) + 1).fill(3);
  await expect(uploadReselectionSource(session, all, id, attempt, { source: 1, offset: 0, end: true, data: tooLarge })).rejects.toMatchObject({ reason: 'invalid' });
  expect(tooLarge.every(byte => byte === 0)).toBe(true);
  await expect(uploadReselectionSource(session, all, id, attempt, { source: 1, offset: 0, end: false, data: new Uint8Array(0) })).rejects.toMatchObject({ reason: 'invalid' });
  for (const count of [0, 1001, NaN, Infinity, 1.5]) await expect(createReselection(session, all, id, count)).rejects.toMatchObject({ reason: 'invalid' });
  await expect(session.reselectionRequest('GetCollectionReselection', { id, attempt, source: 1 })).rejects.toMatchObject({ reason: 'invalid' });
  const body = encoder.encode('PRIVATE');
  await expect(session.reselectionRequest('VerifyCollectionReselection', { id, attempt, body })).rejects.toMatchObject({ reason: 'invalid' });
  expect(body.every(byte => byte === 0)).toBe(true); expect(sent).not.toHaveBeenCalled();
});

it('accepts a maximum-sized part without converting arbitrary binary bytes into text', async () => {
  const data = new Uint8Array(1 << 20).fill(255); data[0] = 0;
  const { session, sent } = await fixture(async (_, init) => {
    expect(init.body).toBe(data); expect((init.body as Uint8Array)[0]).toBe(0); expect((init.body as Uint8Array)[1]).toBe(255);
    return response({ ...finished, rawBytes: data.length });
  });
  await uploadReselectionSource(session, all, id, attempt, { source: 1, offset: 0, end: true, data });
  expect(data.every(byte => byte === 0)).toBe(true); expect(sent).toHaveBeenCalledTimes(1);
});

it('rejects stale successful upload progress instead of allowing an implicit resend loop', async () => {
  for (const end of [true, false]) {
    const { session, sent } = await fixture(async () => response(status));
    const data = encoder.encode('PRIVATE');
    await expect(uploadReselectionSource(session, all, id, attempt, { source: 1, offset: 0, end, data })).rejects.toMatchObject({ reason: 'unconfirmed' });
    expect(sent).toHaveBeenCalledTimes(1); expect(data.every(byte => byte === 0)).toBe(true);
  }
  const { session } = await fixture(async () => response({ ...status, rawBytes: 7, nextOffset: 7 }));
  expect((await uploadReselectionSource(session, all, id, attempt, { source: 1, offset: 0, end: false, data: encoder.encode('PRIVATE') })).data.nextOffset).toBe(7);
  await expect(uploadReselectionSource(session, all, id, attempt, { source: 1, offset: 0, end: true, data: encoder.encode('PRIVATE') })).rejects.toMatchObject({ reason: 'unconfirmed' });
});

it('never retries uncertain mutations or starts verify/resume after a lost response', async () => {
  const { session, sent } = await fixture(async () => { throw new Error('PRIVATE transport diagnostic'); });
  const data = encoder.encode('PRIVATE source');
  await expect(uploadReselectionSource(session, all, id, attempt, { source: 1, offset: 0, end: true, data })).rejects.toMatchObject({ reason: 'unconfirmed', message: expect.not.stringContaining('PRIVATE') });
  await expect(createReselection(session, all, id, 1)).rejects.toMatchObject({ reason: 'unconfirmed' });
  await expect(verifyReselection(session, all, id, attempt)).rejects.toMatchObject({ reason: 'unconfirmed' });
  await expect(resumeReselection(session, all, id, attempt)).rejects.toMatchObject({ reason: 'unconfirmed' });
  await expect(discardReselection(session, all, id, attempt)).rejects.toMatchObject({ reason: 'unconfirmed' });
  await expect(getReselection(session, all, id, attempt)).rejects.toMatchObject({ reason: 'unavailable' });
  expect(data.every(byte => byte === 0)).toBe(true); expect(sent).toHaveBeenCalledTimes(6);
});

it('sanitizes status fields and rejects unsupported phases, identities, expiry and inconsistent completion', () => {
  expect(reselectionStatus({ ...status, rawSource: 'PRIVATE', identityKey: 'PRIVATE', diagnostic: 'PRIVATE', path: '/PRIVATE' }, id, attempt)).toEqual(status);
  for (const change of [{ id: 'wrong' }, { operationID: 'other' }, { normalizationProfile: 'future-profile' }, { phase: 'PRIVATE' }, { phase: 'verified' }, { nextSource: 0 },
    { nextOffset: 1 }, { expiresAt: 'PRIVATE' }, { sourcesCompleted: 2 }, { rawBytes: (64 << 20) + 1 }, { operationUploaded: 10001 }, { errorCode: 'input_mismatch' }]) {
    expect(() => reselectionStatus({ ...status, ...change }, id, attempt)).toThrow(ManagementError);
  }
  expect(reselectionStatus({ ...finished, phase: 'verified' }, id, attempt).phase).toBe('verified');
  expect(reselectionStatus({ ...status, phase: 'failed', errorCode: 'input_mismatch' }, id, attempt).errorCode).toBe('input_mismatch');
  for (const errorCode of [undefined, 'PRIVATE']) expect(() => reselectionStatus({ ...status, phase: 'failed', errorCode }, id, attempt)).toThrow();
});

it('requires expected status and original handle, with malformed mutation success remaining uncertain', async () => {
  const cases: { operation: 'create' | 'read' | 'verify' | 'resume' | 'discard'; value: unknown; status: number; header?: string }[] = [
    { operation: 'create', value: status, status: 200 }, { operation: 'create', value: { ...status, sourceCount: 2 }, status: 201 },
    { operation: 'read', value: { ...status, operationID: 'other' }, status: 200 }, { operation: 'read', value: status, status: 200, header: 'other' },
    { operation: 'verify', value: status, status: 202 }, { operation: 'resume', value: { ...finished, phase: 'verified' }, status: 202 },
    { operation: 'discard', value: status, status: 200 }, { operation: 'discard', value: undefined, status: 204, header: 'other' },
  ];
  for (const item of cases) {
    const { session, sent } = await fixture(async () => response(item.value, item.status, item.header ? { 'X-Operation-ID': item.header } : undefined));
    const promise = item.operation === 'create' ? createReselection(session, all, id, 1) : item.operation === 'read' ? getReselection(session, all, id, attempt) :
      item.operation === 'verify' ? verifyReselection(session, all, id, attempt) : item.operation === 'resume' ? resumeReselection(session, all, id, attempt) : discardReselection(session, all, id, attempt);
    await expect(promise).rejects.toMatchObject({ reason: item.operation === 'read' ? 'invalid' : 'unconfirmed' });
    expect(sent).toHaveBeenCalledTimes(1);
  }
});

it('bounds declared/streamed responses to16KiB, preserves smaller session limits and strips reflected errors', async () => {
  for (const declared of [true, false]) {
    const cancel = vi.fn();
    const { session, sent } = await fixture(async () => new Response(new ReadableStream({ start(controller) { if (!declared) controller.enqueue(new Uint8Array((16 << 10) + 1)); }, cancel }),
      declared ? { headers: { 'Content-Length': String((16 << 10) + 1) } } : undefined));
    await expect(getReselection(session, all, id, attempt)).rejects.toMatchObject({ reason: 'too-large' });
    expect(cancel).toHaveBeenCalledOnce(); expect(sent).toHaveBeenCalledTimes(1);
  }
  const small = await fixture(async () => response({ ...status, ignored: 'x'.repeat(1024) }), [...all], { maxResponseBytes: 1024 });
  await expect(getReselection(small.session, all, id, attempt)).rejects.toMatchObject({ reason: 'too-large' });
  const rejected = await fixture(async () => response({ code: 'PRIVATE', detail: 'PRIVATE SOURCE AND TOKEN', errors: [{ field: 'PRIVATE', message: 'PRIVATE' }] }, 409,
    { 'X-Request-ID': 'PRIVATE', 'X-Operation-ID': 'PRIVATE', 'X-Resource-Version': 'PRIVATE' }));
  try { await verifyReselection(rejected.session, all, id, attempt); throw new Error('expected rejection'); }
  catch (error) { expect(error).toMatchObject({ reason: 'http', status: 409 }); expect(JSON.stringify(error)).not.toContain('PRIVATE'); }
  const oversizedError = await fixture(async () => response({ detail: 'PRIVATE'.repeat(3000) }, 409));
  await expect(getReselection(oversizedError.session, all, id, attempt)).rejects.toMatchObject({ reason: 'http', status: 409, responseIssue: 'too-large', problem: undefined });
});

it('cancels before dispatch or on session change without retaining binary source contents', async () => {
  const { session, sent } = await fixture(async () => response(finished));
  const controller = new AbortController(); controller.abort('PRIVATE');
  const data = encoder.encode('PRIVATE');
  await expect(uploadReselectionSource(session, all, id, attempt, { source: 1, offset: 0, end: true, data }, controller.signal)).rejects.toMatchObject({ reason: 'cancelled' });
  expect(data.every(byte => byte === 0)).toBe(true); expect(sent).not.toHaveBeenCalled();
  const changed = await fixture(async () => { changed.session.signOut(); return response(finished); });
  const other = encoder.encode('PRIVATE');
  await expect(uploadReselectionSource(changed.session, all, id, attempt, { source: 1, offset: 0, end: true, data: other })).rejects.toMatchObject({ reason: 'unconfirmed' });
  expect(other.every(byte => byte === 0)).toBe(true); expect(changed.sent).toHaveBeenCalledTimes(1);
});

it('times out a stalled mutation response, clears its bytes and never retries or reflects a redirect', async () => {
  const cancel = vi.fn();
  const { session, sent } = await fixture(async () => new Response(new ReadableStream({ cancel })), [...all], { timeoutMs: 20 });
  const data = encoder.encode('PRIVATE');
  await expect(uploadReselectionSource(session, all, id, attempt, { source: 1, offset: 0, end: true, data })).rejects.toMatchObject({ reason: 'unconfirmed' });
  expect(cancel).toHaveBeenCalledOnce(); expect(sent).toHaveBeenCalledTimes(1); expect(data.every(byte => byte === 0)).toBe(true);
  const redirected = await fixture(async () => new Response(null, { status: 307, headers: { Location: 'https://other.example/PRIVATE' } }));
  await expect(createReselection(redirected.session, all, id, 1)).rejects.toMatchObject({ reason: 'unconfirmed', message: expect.not.stringContaining('PRIVATE') });
  expect(redirected.sent).toHaveBeenCalledTimes(1);
});

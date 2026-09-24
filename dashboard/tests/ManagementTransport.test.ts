import { afterEach, describe, expect, it, vi } from 'vitest';
import { DashboardSession, ManagementError } from '../src/api/session';

const operator = { principalId: 'alice', role: 'operator', permissions: ['CreateMonitor', 'PatchMonitor'] };
const json = (value: unknown, init?: ResponseInit) => new Response(JSON.stringify(value), init);
const token = 'test-opaque-bearer';
const allocationProblem = '{"type":"about:blank","title":"Service Unavailable","status":503,"code":"operationAllocationUnconfirmed"}';

async function signedIn(options: { maxResponseBytes?: number; timeoutMs?: number } = {}) {
  const fetcher = vi.fn<typeof fetch>().mockResolvedValueOnce(json(operator));
  const session = new DashboardSession({ origin: 'https://cpra.example', fetch: fetcher, ...options });
  await session.signIn(token);
  expect(session.getSnapshot().phase).toBe('authenticated');
  return { session, fetcher };
}

afterEach(() => { vi.useRealTimers(); vi.restoreAllMocks(); });

describe('management transport', () => {
  it('sends the required create-only validator without a replacement validator', async () => {
    const { session, fetcher } = await signedIn();
    fetcher.mockResolvedValueOnce(json({}));
    await session.mutate('/api/v2/monitors', { method: 'POST', operation: 'CreateMonitor', body: {} });
    const headers = new Headers(fetcher.mock.calls[1][1]?.headers);
    expect(headers.get('If-None-Match')).toBe('*');
    expect(headers.has('If-Match')).toBe(false);
    await expect(session.mutate('/api/v2/monitors', { method: 'POST', operation: 'CreateMonitor', resourceVersion: 'old', body: {} })).rejects.toMatchObject({ reason: 'invalid' });
    expect(fetcher).toHaveBeenCalledTimes(2);
  });
  it('sends a conditional merge patch once with credentials restricted to the configured origin', async () => {
    const { session, fetcher } = await signedIn();
    fetcher.mockResolvedValueOnce(json({ id: 'first' }, { headers: { ETag: '"v2"', 'X-Request-ID': 'request', 'X-Operation-ID': 'operation', 'Retry-After': '7' } }));
    const result = await session.mutate('/api/v2/monitors/first', { method: 'PATCH', operation: 'PatchMonitor', resourceVersion: 'v1', body: { enabled: false } });
    expect(fetcher).toHaveBeenCalledTimes(2);
    const [url, init] = fetcher.mock.calls[1];
    expect(url).toBe('https://cpra.example/api/v2/monitors/first');
    expect(init).toMatchObject({ method: 'PATCH', body: '{"enabled":false}', redirect: 'error', credentials: 'omit', cache: 'no-store' });
    expect(new Headers(init?.headers).get('Authorization')).toBe(`Bearer ${token}`);
    expect(new Headers(init?.headers).get('Content-Type')).toBe('application/merge-patch+json');
    expect(new Headers(init?.headers).get('If-Match')).toBe('"v1"');
    expect(result).toMatchObject({ resourceVersion: 'v2', operationID: 'operation', requestID: 'request', retryAfterMs: 7000 });
  });

  it('does not forward a token outside this API origin or through a redirect', async () => {
    const { session, fetcher } = await signedIn();
    for (const path of ['https://other.example/api/v2/monitors', '//other.example/api/v2/monitors', '/api/v2/../../metrics', '/api/v2/monitors#token', '/api/v2/\\other.example']) {
      await expect(session.get(path)).rejects.toMatchObject({ reason: 'invalid' });
    }
    await expect(session.legacy('https://other.example/api/v1/overview')).rejects.toMatchObject({ reason: 'invalid' });
    expect(fetcher).toHaveBeenCalledTimes(1);
    for (const version of ['*', 'W/"weak"', 'one,two', '"one", "two"']) {
      await expect(session.mutate('/api/v2/monitors/first', { method: 'PATCH', operation: 'PatchMonitor', resourceVersion: version })).rejects.toMatchObject({ reason: 'invalid' });
    }
    fetcher.mockResolvedValueOnce(new Response(null, { status: 307, headers: { Location: 'https://other.example/receive' } }));
    await expect(session.mutate('/api/v2/monitors', { method: 'POST', operation: 'CreateMonitor', body: { name: 'first' } })).rejects.toMatchObject({ reason: 'unconfirmed' });
    expect(fetcher).toHaveBeenCalledTimes(2);
  });

  it('requires TLS before sending any bearer token', async () => {
    const fetcher = vi.fn<typeof fetch>();
    const session = new DashboardSession({ origin: 'http://cpra.example', fetch: fetcher });
    await session.signIn(token);
    expect(session.getSnapshot()).toMatchObject({ phase: 'signed-out', message: expect.stringContaining('HTTPS') });
    expect(fetcher).not.toHaveBeenCalled();
  });

  it('does not infer operation permission from a role or discovery capabilities', async () => {
    const fetcher = vi.fn<typeof fetch>().mockResolvedValueOnce(json({ ...operator, permissions: [] }));
    const session = new DashboardSession({ origin: 'https://cpra.example', fetch: fetcher });
    await session.signIn(token);
    expect(session.can('CreateMonitor')).toBe(false);
    await expect(session.mutate('/api/v2/monitors', { method: 'POST', operation: 'CreateMonitor' })).rejects.toMatchObject({ reason: 'forbidden' });
    expect(fetcher).toHaveBeenCalledTimes(1);
  });

  it('requires the originally observed version and treats permission names as case sensitive', async () => {
    const { session, fetcher } = await signedIn();
    expect(session.can('createMonitor')).toBe(false);
    await expect(session.mutate('/api/v2/monitors/first', { method: 'PATCH', operation: 'PatchMonitor', resourceVersion: '' })).rejects.toMatchObject({ reason: 'invalid' });
    expect(fetcher).toHaveBeenCalledTimes(1);
  });

  it('keeps typed rejection details without putting response details in the default error message', async () => {
    const { session, fetcher } = await signedIn();
    fetcher.mockResolvedValueOnce(json({ code: 'conflict', detail: 'private field context', errors: [{ field: 'metadata.resourceVersion', message: 'changed' }], requestID: 'request' }, { status: 409 }));
    try {
      await session.mutate('/api/v2/monitors/first', { method: 'PATCH', operation: 'PatchMonitor', resourceVersion: 'old', body: {} });
      expect.unreachable();
    } catch (error) {
      expect(error).toBeInstanceOf(ManagementError);
      expect(error).toMatchObject({ status: 409, reason: 'http', problem: { code: 'conflict', errors: [{ field: 'metadata.resourceVersion', message: 'changed' }] } });
      expect(String(error)).not.toContain('private field context');
      expect(String(error)).not.toContain(token);
    }
    expect(fetcher).toHaveBeenCalledTimes(2);
  });

  it('never automatically retries an uncertain mutation', async () => {
    const { session, fetcher } = await signedIn();
    fetcher.mockRejectedValueOnce(new TypeError('raw transport failure with private details'));
    await expect(session.mutate('/api/v2/monitors', { method: 'POST', operation: 'CreateMonitor', body: {} })).rejects.toMatchObject({ reason: 'unconfirmed' });
    expect(fetcher).toHaveBeenCalledTimes(2);
  });

  it('distinguishes explicitly unconfirmed allocation from an unconfirmed active mutation without retrying', async () => {
    const { session, fetcher } = await signedIn();
    fetcher.mockResolvedValueOnce(json({ type: 'about:blank', title: 'Service Unavailable', status: 503, code: 'operationAllocationUnconfirmed', detail: 'private allocator context' },
      { status: 503, headers: { 'Content-Type': 'application/problem+json', 'X-CPRa-Admission': 'not-submitted' } }));
    let caught: unknown;
    try { await session.mutate('/api/v2/monitors', { method: 'POST', operation: 'CreateMonitor', body: {} }); }
    catch (error) { caught = error; }
    expect(caught).toMatchObject({ reason: 'not-admitted', status: 503, response: { operationID: '' } });
    expect(String(caught)).toContain('No resource change or action was submitted');
    expect(String(caught)).not.toContain('private allocator context');
    expect(fetcher).toHaveBeenCalledTimes(2);
  });

  it.each(['missing-contract', 'wrong-media-type', 'handle-header', 'handle-body', 'wrong-status', 'truncated', 'generic-503'])('keeps %s failures uncertain even when allocation-like text is present', async variant => {
    const { session, fetcher } = await signedIn();
    const problem = { type: 'about:blank', title: 'Service Unavailable', status: variant === 'wrong-status' ? 400 : 503, code: variant === 'generic-503' ? 'unavailable' : 'operationAllocationUnconfirmed',
      ...(variant === 'handle-body' ? { operationID: 'submitted-operation' } : {}) };
    const headers: Record<string, string> = { 'Content-Type': variant === 'wrong-media-type' ? 'application/json' : 'application/problem+json' };
    if (variant !== 'missing-contract') headers['X-CPRa-Admission'] = 'not-submitted';
    if (variant === 'handle-header') headers['X-Operation-ID'] = 'submitted-operation';
    fetcher.mockResolvedValueOnce(new Response(JSON.stringify(problem) + (variant === 'truncated' ? '{' : ''), { status: 503, headers }));
    await expect(session.mutate('/api/v2/monitors', { method: 'POST', operation: 'CreateMonitor', body: {} })).rejects.toMatchObject({ reason: 'unconfirmed' });
    expect(fetcher).toHaveBeenCalledTimes(2);
  });

  it.each([
    { name: 'duplicate-code', body: allocationProblem.slice(0, -1) + ',"code":"operationAllocationUnconfirmed"}' },
    { name: 'contradictory-duplicate-code', body: allocationProblem.replace('"code":', '"code":"outcomeUnconfirmed","code":') },
    { name: 'escaped-duplicate-code', body: allocationProblem.slice(0, -1) + ',"co\\u0064e":"operationAllocationUnconfirmed"}' },
    { name: 'case-alias', body: allocationProblem.slice(0, -1) + ',"Status":503}' },
    { name: 'empty-body-handle', body: allocationProblem.slice(0, -1) + ',"operationID":""}' },
    { name: 'null-body-handle', body: allocationProblem.slice(0, -1) + ',"operationID":null}' },
    { name: 'empty-header-handle', header: 'X-Operation-ID', value: '' },
    { name: 'duplicate-marker', header: 'X-CPRa-Admission', value: 'not-submitted', append: true },
    { name: 'duplicate-media', header: 'Content-Type', value: 'application/problem+json', append: true },
    { name: 'malformed-media-parameter', header: 'Content-Type', value: 'application/problem+json; malformed' },
    { name: 'unclosed-media-quote', header: 'Content-Type', value: 'application/problem+json; charset="utf-8' },
    { name: 'contradictory-media-parameter', header: 'Content-Type', value: 'application/problem+json; charset=utf-8; charset=other' },
    { name: 'blank-title', body: allocationProblem.replace('Service Unavailable', '   ') },
    { name: 'wrong-optional-type', body: allocationProblem.slice(0, -1) + ',"detail":42}' },
    { name: 'null-optional-type', body: allocationProblem.slice(0, -1) + ',"instance":null}' },
    { name: 'unknown-problem-field', body: allocationProblem.slice(0, -1) + ',"privateExtra":"value"}' },
    { name: 'wrong-errors-type', body: allocationProblem.slice(0, -1) + ',"errors":"wrong"}' },
    { name: 'null-errors', body: allocationProblem.slice(0, -1) + ',"errors":null}' },
    { name: 'null-field-error', body: allocationProblem.slice(0, -1) + ',"errors":[null]}' },
    { name: 'missing-field-error-member', body: allocationProblem.slice(0, -1) + ',"errors":[{"field":"x"}]}' },
    { name: 'null-field-error-member', body: allocationProblem.slice(0, -1) + ',"errors":[{"field":null,"message":"x"}]}' },
    { name: 'wrong-field-error-reason', body: allocationProblem.slice(0, -1) + ',"errors":[{"field":"x","message":"x","reason":3}]}' },
    { name: 'unknown-field-error-member', body: allocationProblem.slice(0, -1) + ',"errors":[{"field":"x","message":"x","extra":"x"}]}' },
    { name: 'escaped-nested-duplicate', body: allocationProblem.slice(0, -1) + ',"errors":[{"field":"x","fie\\u006cd":"x","message":"x"}]}' },
    { name: 'oversized-error', body: allocationProblem.slice(0, -1) + ',"detail":"' + 'x'.repeat(64 * 1024) + '"}' },
    { name: 'excessive-nesting', body: allocationProblem.slice(0, -1) + ',"extra":' + '['.repeat(130) + 'null' + ']'.repeat(130) + '}' },
  ])('keeps $name outside the non-admission exception and sends no retry', async test => {
    const { session, fetcher } = await signedIn();
    const headers = new Headers({ 'Content-Type': 'application/problem+json', 'X-CPRa-Admission': 'not-submitted' });
    if (test.header) {
      if (test.append) headers.append(test.header, test.value!);
      else headers.set(test.header, test.value!);
    }
    fetcher.mockResolvedValueOnce(new Response(test.body ?? allocationProblem, { status: 503, headers }));
    await expect(session.mutate('/api/v2/monitors', { method: 'POST', operation: 'CreateMonitor', body: {} })).rejects.toMatchObject({ reason: 'unconfirmed' });
    expect(fetcher).toHaveBeenCalledTimes(2);
  });

  it('rejects malformed UTF-8 error bytes instead of replacing them and claiming non-admission', async () => {
    const { session, fetcher } = await signedIn();
    const prefix = new TextEncoder().encode(allocationProblem.slice(0, -1) + ',"detail":"');
    const bytes = new Uint8Array(prefix.length + 3);
    bytes.set(prefix);
    bytes.set([0xff, 0x22, 0x7d], prefix.length);
    fetcher.mockResolvedValueOnce(new Response(bytes, { status: 503, headers: { 'Content-Type': 'application/problem+json', 'X-CPRa-Admission': 'not-submitted' } }));
    await expect(session.mutate('/api/v2/monitors', { method: 'POST', operation: 'CreateMonitor', body: {} })).rejects.toMatchObject({ reason: 'unconfirmed', responseIssue: 'invalid' });
    expect(fetcher).toHaveBeenCalledTimes(2);
  });

  it('accepts complete typed problem members and valid quoted media parameters without rendering private detail', async () => {
    const { session, fetcher } = await signedIn();
    fetcher.mockResolvedValueOnce(new Response(allocationProblem.slice(0, -1) + ',"instance":"/request/1","detail":"private field detail","errors":[{"field":"x","message":"private message","reason":"busy"}]}',
      { status: 503, headers: { 'Content-Type': 'application/problem+json; charset="utf-8"', 'X-CPRa-Admission': 'not-submitted' } }));
    let caught: unknown;
    try { await session.mutate('/api/v2/monitors', { method: 'POST', operation: 'CreateMonitor', body: {} }); }
    catch (error) { caught = error; }
    expect(caught).toMatchObject({ reason: 'not-admitted' });
    expect(String(caught)).not.toContain('private');
    expect(fetcher).toHaveBeenCalledTimes(2);
  });

  it('keeps ordinary successful response decoding compatible with existing endpoints', async () => {
    const { session, fetcher } = await signedIn();
    fetcher.mockResolvedValueOnce(new Response('{"value":"first","value":"last"}'));
    await expect(session.get('/api/v2/state')).resolves.toMatchObject({ data: { value: 'last' } });
    expect(fetcher).toHaveBeenCalledTimes(2);
  });

  it.each(['outcomeUnconfirmed', 'requestInterrupted'])('holds the server-declared %s outcome while retaining its receipt identity', async code => {
    const { session, fetcher } = await signedIn();
    fetcher.mockResolvedValueOnce(json({ code, detail: 'Do not repeat the write.' }, { status: 503, headers: { 'X-Operation-ID': 'original-operation', 'Retry-After': '5' } }));
    await expect(session.mutate('/api/v2/monitors', { method: 'POST', operation: 'CreateMonitor', body: {} })).rejects.toMatchObject({ reason: 'unconfirmed', status: 503, problem: { code }, response: { operationID: 'original-operation', retryAfterMs: 5000 } });
    expect(fetcher).toHaveBeenCalledTimes(2);
  });

  it('distinguishes cancellation before and after sending a mutation', async () => {
    const { session, fetcher } = await signedIn();
    const before = new AbortController();
    before.abort();
    await expect(session.mutate('/api/v2/monitors', { method: 'POST', operation: 'CreateMonitor', signal: before.signal })).rejects.toMatchObject({ reason: 'cancelled' });
    expect(fetcher).toHaveBeenCalledTimes(1);
    const after = new AbortController();
    fetcher.mockImplementationOnce((_url, init) => new Promise((_resolve, reject) => {
      init?.signal?.addEventListener('abort', () => reject(new DOMException('aborted', 'AbortError')), { once: true });
    }));
    const pending = session.mutate('/api/v2/monitors', { method: 'POST', operation: 'CreateMonitor', signal: after.signal });
    after.abort();
    await expect(pending).rejects.toMatchObject({ reason: 'unconfirmed' });
    expect(fetcher).toHaveBeenCalledTimes(2);
  });

  it('bounds streamed bytes even without a Content-Length header', async () => {
    const { session, fetcher } = await signedIn({ maxResponseBytes: 256 });
    const cancel = vi.fn();
    fetcher.mockResolvedValueOnce(new Response(new ReadableStream<Uint8Array>({
      start(controller) { controller.enqueue(new TextEncoder().encode(JSON.stringify('x'.repeat(300)))); }, cancel,
    })));
    await expect(session.get('/api/v2/monitors')).rejects.toMatchObject({ reason: 'too-large' });
    expect(cancel).toHaveBeenCalledOnce();
  });

  it('marks a successful mutation response with an unreadable body unconfirmed', async () => {
    const { session, fetcher } = await signedIn({ maxResponseBytes: 256 });
    fetcher.mockResolvedValueOnce(json('x'.repeat(300), { headers: { 'X-Operation-ID': 'original-operation' } }));
    await expect(session.mutate('/api/v2/monitors', { method: 'POST', operation: 'CreateMonitor' })).rejects.toMatchObject({ reason: 'unconfirmed', response: { operationID: 'original-operation' } });
  });

  it('reports oversized problem bodies explicitly while preserving the rejection status', async () => {
    const { session, fetcher } = await signedIn();
    fetcher.mockResolvedValueOnce(json({ detail: 'x'.repeat(65_537) }, { status: 409 }));
    await expect(session.get('/api/v2/monitors')).rejects.toMatchObject({ reason: 'http', status: 409, responseIssue: 'too-large' });
  });

  it('bounds the response body lifetime, including a stream that stops producing data', async () => {
    const { session, fetcher } = await signedIn({ timeoutMs: 25 });
    const cancel = vi.fn();
    fetcher.mockResolvedValueOnce(new Response(new ReadableStream({ cancel })));
    await expect(session.get('/api/v2/monitors')).rejects.toMatchObject({ reason: 'cancelled' });
    expect(cancel).toHaveBeenCalledOnce();
  });

  it('rejects an old response after logout even if the underlying fetch ignores cancellation', async () => {
    const { session, fetcher } = await signedIn();
    let resolve!: (response: Response) => void;
    fetcher.mockImplementationOnce(() => new Promise<Response>(done => { resolve = done; }));
    const clear = vi.fn();
    session.onReset(clear);
    const pending = session.get('/api/v2/monitors');
    session.signOut();
    resolve(json({ secretOldData: true }));
    await expect(pending).rejects.toMatchObject({ reason: 'session-changed' });
    expect(clear).toHaveBeenCalledOnce();
    expect(session.getSnapshot()).toMatchObject({ phase: 'signed-out' });
    expect(session.getSnapshot().access).toBeUndefined();
  });

  it('revokes the active session and clears observers when the server returns 401', async () => {
    const { session, fetcher } = await signedIn();
    const clear = vi.fn();
    session.onReset(clear);
    fetcher.mockResolvedValueOnce(json({ code: 'unauthorized' }, { status: 401 }));
    await expect(session.get('/api/v2/monitors')).rejects.toMatchObject({ reason: 'http', status: 401 });
    expect(clear).toHaveBeenCalledOnce();
    expect(session.getSnapshot().phase).toBe('signed-out');
    expect(session.can('CreateMonitor')).toBe(false);
  });

  it('falls back only on a missing v2 endpoint, without making any write request', async () => {
    const fetcher = vi.fn<typeof fetch>().mockResolvedValueOnce(json({}, { status: 404 })).mockResolvedValueOnce(json({ total: 1 }));
    const session = new DashboardSession({ origin: 'http://cpra.example', fetch: fetcher });
    await session.discover();
    expect(session.getSnapshot().phase).toBe('legacy');
    expect(await session.legacy('/api/v1/overview')).toEqual({ total: 1 });
    expect(new Headers(fetcher.mock.calls[1][1]?.headers).has('Authorization')).toBe(false);
    const failing = new DashboardSession({ origin: 'http://cpra.example', fetch: vi.fn<typeof fetch>().mockResolvedValueOnce(json({}, { status: 503 })) });
    await failing.discover();
    expect(failing.getSnapshot().phase).toBe('unavailable');
  });

  it('preserves existing browser Basic authentication only for read-only discovery and legacy reads', async () => {
    const fetcher = vi.fn<typeof fetch>().mockImplementation(async (_url, init) =>
      json({}, { status: init?.credentials === 'same-origin' ? 404 : 401 }));
    const session = new DashboardSession({ origin: 'https://cpra.example', fetch: fetcher });
    await session.discover();
    expect(session.getSnapshot().phase).toBe('legacy');
    expect(fetcher.mock.calls[0][1]).toMatchObject({ method: 'GET', credentials: 'same-origin', redirect: 'error' });
    expect(new Headers(fetcher.mock.calls[0][1]?.headers).has('Authorization')).toBe(false);
  });

  it('never writes the token to browser storage and a new tab session requires sign-in', async () => {
    const storage = vi.spyOn(Storage.prototype, 'setItem');
    const { session } = await signedIn();
    expect(JSON.stringify(session.getSnapshot())).not.toContain(token);
    expect(JSON.stringify(session)).not.toContain(token);
    expect(storage).not.toHaveBeenCalled();
    const next = new DashboardSession({ origin: 'https://cpra.example', fetch: vi.fn<typeof fetch>().mockResolvedValueOnce(json({}, { status: 401 })) });
    await next.discover();
    expect(next.getSnapshot().phase).toBe('signed-out');
    expect(next.can('CreateMonitor')).toBe(false);
  });
});

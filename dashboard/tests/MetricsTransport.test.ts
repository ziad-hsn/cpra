import { afterEach, describe, expect, it, vi } from 'vitest';
import { DashboardSession, PROMETHEUS_MAX_BYTES } from '../src/api/session';

const token = 'tab-only-metrics-token';
const plain = (body: BodyInit | null = 'cpra_checks_total 42\n', headers: HeadersInit = {}) => new Response(body, { headers: { 'Content-Type': 'text/plain; version=0.0.4; charset=utf-8', ...headers } });
async function authenticated(permissions = ['GetMetrics'], timeoutMs?: number) {
  const fetcher = vi.fn<typeof fetch>().mockResolvedValueOnce(new Response(JSON.stringify({ principalId: 'reader', role: 'reader', permissions })));
  const session = new DashboardSession({ origin: 'https://cpra.example', fetch: fetcher, timeoutMs });
  await session.signIn(token);
  return { session, fetcher };
}
afterEach(() => { vi.restoreAllMocks(); vi.useRealTimers(); });

describe('authenticated metrics transport', () => {
  it('uses a fixed GET and tab bearer without URL credentials, cookies, cache or redirects', async () => {
    const storage = vi.spyOn(Storage.prototype, 'setItem');
    const { session, fetcher } = await authenticated();
    fetcher.mockResolvedValueOnce(plain());
    expect(await session.prometheus()).toBe('cpra_checks_total 42\n');
    expect(fetcher).toHaveBeenCalledTimes(2);
    const [url, init] = fetcher.mock.calls[1];
    expect(url).toBe('https://cpra.example/metrics');
    expect(init).toMatchObject({ method: 'GET', cache: 'no-store', redirect: 'error', credentials: 'omit' });
    expect(init?.body).toBeUndefined();
    expect(new Headers(init?.headers).get('Authorization')).toBe(`Bearer ${token}`);
    expect(new Headers(init?.headers).get('Accept')).toContain('text/plain');
    expect(storage).not.toHaveBeenCalled();
    expect(JSON.stringify(session.getSnapshot())).not.toContain(token);
  });

  it('requires the exact server GetMetrics permission before dispatch', async () => {
    for (const permissions of [[], ['ExportMetrics'], ['GetState']]) {
      const { session, fetcher } = await authenticated(permissions);
      await expect(session.prometheus()).rejects.toMatchObject({ reason: 'forbidden' });
      expect(fetcher).toHaveBeenCalledTimes(1);
    }
  });

  it('allows browser Basic/read credentials only in discovered legacy phase', async () => {
    const fetcher = vi.fn<typeof fetch>().mockResolvedValueOnce(new Response('{}', { status: 404 })).mockResolvedValueOnce(plain());
    const session = new DashboardSession({ origin: 'http://cpra.example', fetch: fetcher });
    await expect(session.prometheus()).rejects.toMatchObject({ reason: 'forbidden' });
    await session.discover();
    expect(await session.prometheus()).toContain('42');
    expect(fetcher.mock.calls[1][1]).toMatchObject({ method: 'GET', credentials: 'same-origin', redirect: 'error' });
    expect(new Headers(fetcher.mock.calls[1][1]?.headers).has('Authorization')).toBe(false);
    session.signOut();
    await expect(session.prometheus()).rejects.toMatchObject({ reason: 'forbidden' });
    expect(fetcher).toHaveBeenCalledTimes(2);
  });

  it('decodes UTF-8 split across streamed chunks without dropping text', async () => {
    const { session, fetcher } = await authenticated();
    const bytes = new TextEncoder().encode('cpra_label{name="équipe"} 1\n');
    const split = bytes.indexOf(0xc3) + 1;
    fetcher.mockResolvedValueOnce(plain(new ReadableStream({ start(controller) { controller.enqueue(bytes.slice(0, split)); controller.enqueue(bytes.slice(split)); controller.close(); } })));
    expect(await session.prometheus()).toBe('cpra_label{name="équipe"} 1\n');
  });

  it.each(['text/html', 'application/json', 'text/plain; charset=iso-8859-1'])('rejects unsupported content type %s before reading its body', async type => {
    const { session, fetcher } = await authenticated();
    const cancel = vi.fn();
    fetcher.mockResolvedValueOnce(plain(new ReadableStream({ cancel }), { 'Content-Type': type }));
    await expect(session.prometheus()).rejects.toMatchObject({ reason: 'invalid' });
    expect(cancel).toHaveBeenCalledOnce();
    expect(fetcher).toHaveBeenCalledTimes(2);
  });

  it('rejects invalid UTF-8 and HTML disguised as text without reflecting the body', async () => {
    for (const body of [new Uint8Array([0xff]), '<html>private-server-body</html>']) {
      const { session, fetcher } = await authenticated();
      fetcher.mockResolvedValueOnce(plain(body));
      await expect(session.prometheus()).rejects.toMatchObject({ reason: 'invalid', message: expect.not.stringContaining('private-server-body') });
    }
  });

  it('rejects declared and streamed overflow without returning truncated metrics', async () => {
    for (const declared of [true, false]) {
      const { session, fetcher } = await authenticated();
      const cancel = vi.fn();
      const stream = new ReadableStream({ start(controller) { if (!declared) { controller.enqueue(new Uint8Array(PROMETHEUS_MAX_BYTES)); controller.enqueue(new Uint8Array(1)); } }, cancel });
      fetcher.mockResolvedValueOnce(plain(stream, declared ? { 'Content-Length': String(PROMETHEUS_MAX_BYTES + 1) } : {}));
      await expect(session.prometheus()).rejects.toMatchObject({ reason: 'too-large' });
      expect(cancel).toHaveBeenCalledOnce();
      expect(fetcher).toHaveBeenCalledTimes(2);
    }
  });

  it('cancels stalled response bodies on explicit cancellation and timeout', async () => {
    for (const explicit of [true, false]) {
      const { session, fetcher } = await authenticated(['GetMetrics'], 20);
      const cancel = vi.fn();
      fetcher.mockResolvedValueOnce(plain(new ReadableStream({ cancel })));
      const controller = new AbortController();
      const pending = session.prometheus({ signal: controller.signal });
      if (explicit) { await Promise.resolve(); controller.abort('private-reason'); }
      await expect(pending).rejects.toMatchObject({ reason: 'cancelled' });
      expect(cancel).toHaveBeenCalledOnce();
    }
    const { session, fetcher } = await authenticated();
    const controller = new AbortController(); controller.abort();
    await expect(session.prometheus({ signal: controller.signal })).rejects.toMatchObject({ reason: 'cancelled' });
    expect(fetcher).toHaveBeenCalledTimes(1);
  });

  it('rejects redirects with one request and no credential forwarding', async () => {
    const { session, fetcher } = await authenticated();
    fetcher.mockResolvedValueOnce(new Response(null, { status: 307, headers: { Location: `https://foreign.example/${token}` } }));
    await expect(session.prometheus()).rejects.toMatchObject({ reason: 'invalid', message: expect.not.stringContaining(token) });
    expect(fetcher).toHaveBeenCalledTimes(2);
  });

  it('clears session observers on 401 and never falls back or renders rejection details', async () => {
    const { session, fetcher } = await authenticated();
    const clear = vi.fn(); session.onReset(clear);
    fetcher.mockResolvedValueOnce(new Response('<html>private-error</html>', { status: 401 }));
    await expect(session.prometheus()).rejects.toMatchObject({ reason: 'http', status: 401, message: 'Metrics request rejected (HTTP 401).' });
    expect(clear).toHaveBeenCalledOnce();
    expect(session.getSnapshot().phase).toBe('signed-out');
    await expect(session.prometheus()).rejects.toMatchObject({ reason: 'forbidden' });
    expect(fetcher).toHaveBeenCalledTimes(2);
  });

  it('discards a late old-session body even when fetch ignores cancellation', async () => {
    const { session, fetcher } = await authenticated();
    let resolve!: (response: Response) => void;
    fetcher.mockImplementationOnce(() => new Promise<Response>(done => { resolve = done; }));
    const pending = session.prometheus();
    session.signOut();
    resolve(plain('old_private_metric 1\n'));
    await expect(pending).rejects.toMatchObject({ reason: 'session-changed' });
  });

  it('does not let a delayed old 401 revoke a newly signed-in identity', async () => {
    const { session, fetcher } = await authenticated();
    let enterCancel!: () => void, finishCancel!: () => void;
    const entered = new Promise<void>(resolve => { enterCancel = resolve; });
    const cancelled = new Promise<void>(resolve => { finishCancel = resolve; });
    fetcher.mockResolvedValueOnce(new Response(new ReadableStream({ cancel() { enterCancel(); return cancelled; } }), { status: 401 }));
    const old = session.prometheus().catch(error => error);
    await entered;
    fetcher.mockResolvedValueOnce(new Response(JSON.stringify({ principalId: 'new-reader', role: 'reader', permissions: ['GetMetrics'] })));
    await session.signIn('new-tab-token');
    expect(session.getSnapshot()).toMatchObject({ phase: 'authenticated', access: { principalId: 'new-reader' } });
    finishCancel();
    expect(await old).toMatchObject({ reason: 'session-changed' });
    expect(session.getSnapshot()).toMatchObject({ phase: 'authenticated', access: { principalId: 'new-reader' } });
    expect(session.can('GetMetrics')).toBe(true);
  });
});

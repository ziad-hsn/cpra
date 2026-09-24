import { afterEach, expect, it, vi } from 'vitest';
import { loadTimeline } from '../src/api/history';
import { DashboardSession } from '../src/api/session';
import { api } from '../src/api/client';

afterEach(() => vi.restoreAllMocks());

async function timeline(body: unknown, options: { permitted?: boolean; status?: number } = {}) {
  const fetcher = vi.fn<typeof fetch>().mockImplementation(async input => {
    const url = new URL(String(input));
    if (url.pathname === '/api/v2/self') return new Response(JSON.stringify({ principalId: 'alice', role: 'reader', permissions: options.permitted === false ? [] : ['GetHistory'] }));
    if (url.pathname === '/api/v2/discovery') return new Response(JSON.stringify({ apiVersions: ['cpra.io/v2'], resources: [], drivers: {}, patchTypes: [] }));
    return new Response(JSON.stringify(body), { status: options.status ?? 200 });
  });
  const session = new DashboardSession({ origin: 'https://cpra.example', fetch: fetcher });
  await session.discover(); await session.signIn('test-token');
  return { session, fetcher };
}

it('preserves public audit fields and frozen cursor while discarding arbitrary payloads', async () => {
  const { session, fetcher } = await timeline({ items: [{ id: 'event-1', monitorID: 'service', time: '2026-09-14T00:00:00Z', kind: 'action_result', actionKind: 'code', color: 'red', endpoint: 0, outcome: 'success', executionRevision: 'execution-1', controlRevision: 'control-1', actor: 'alice', reason: 'confirmed', note: 'operator note', incidentID: 'incident-1', actionID: 'action-1', privatePayload: 'must-not-retain' }], nextCursor: 'frozen-next' });
  const result = await loadTimeline(session, 'service', 'frozen-first');
  expect(result.events[0]).toEqual({ id: 'event-1', monitor_id: 'service', at: '2026-09-14T00:00:00Z', type: 'action_result', kind: 'code', color: 'red', endpoint: 0, outcome: 'success', revision: 'execution-1', control_revision: 'control-1', actor: 'alice', reason: 'confirmed', note: 'operator note', incident_id: 'incident-1', action_id: 'action-1' });
  expect(result.next_cursor).toBe('frozen-next');
  expect(JSON.stringify(result)).not.toContain('must-not-retain');
  const url = new URL(String(fetcher.mock.calls.at(-1)?.[0]));
  expect(Object.fromEntries(url.searchParams)).toEqual({ monitorID: 'service', limit: '100', cursor: 'frozen-first' });
});

it('rejects an event belonging to another monitor', async () => {
  const { session } = await timeline({ items: [{ id: 'foreign', monitorID: 'another-service', time: '2026-09-14T00:00:00Z', kind: 'control_acknowledge' }] });
  await expect(loadTimeline(session, 'service')).rejects.toMatchObject({ reason: 'invalid' });
});

it.each([403, 404, 410, 503])('does not downgrade a v2 history response with status %s', async status => {
  const legacy = vi.spyOn(api, 'getHistory');
  const { session } = await timeline({ code: 'unavailable' }, { status });
  await expect(loadTimeline(session, 'service')).rejects.toMatchObject({ status });
  expect(legacy).not.toHaveBeenCalled();
});

it('denies reads locally without the exact history permission', async () => {
  const { session, fetcher } = await timeline({ items: [] }, { permitted: false });
  const count = fetcher.mock.calls.length;
  await expect(loadTimeline(session, 'service')).rejects.toMatchObject({ reason: 'forbidden' });
  expect(fetcher).toHaveBeenCalledTimes(count);
});

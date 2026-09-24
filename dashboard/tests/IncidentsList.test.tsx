import { afterEach, expect, it, vi } from 'vitest';
import { act, cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { createMemoryRouter, RouterProvider } from 'react-router-dom';
import { DashboardSession } from '../src/api/session';
import { listIncidents } from '../src/api/incidents';
import { SessionBoundary } from '../src/auth/SessionBoundary';
import Alerts from '../src/pages/Alerts';

const incident = (id: number) => ({ id: `incident-${id}`, monitorID: `service-${id}`, revision: `revision-${id}`, state: 'open', openedAt: '2026-09-19T12:00:00Z' });
const page = (id: number, nextCursor = '', snapshot = 'fixed-snapshot') => ({ items: [incident(id)], nextCursor, snapshot, generatedAt: '2026-09-19T12:30:00Z' });
const json = (value: unknown, status = 200) => new Response(JSON.stringify(value), { status });

async function setup(reply: (url: URL) => Response, permissions = ['ListIncidents', 'GetMonitor']) {
  const reads: URL[] = [];
  const fetcher = vi.fn<typeof fetch>().mockImplementation(async (input, init) => {
    const url = new URL(String(input));
    if (url.pathname === '/api/v2/self') return new Headers(init?.headers).has('Authorization') ? json({ principalId: 'reader', role: 'reader', permissions }) : json({}, 401);
    reads.push(url);
    return reply(url);
  });
  const session = new DashboardSession({ origin: 'https://cpra.example', fetch: fetcher });
  await session.discover(); await session.signIn('private-tab-token');
  const router = createMemoryRouter([{ path: '/alerts', element: <SessionBoundary session={session}><Alerts /></SessionBoundary> },
    { path: '/monitors/by-id/:monitorID', element: <p>Stable monitor observation</p> }], { initialEntries: ['/alerts'] });
  render(<RouterProvider router={router} />);
  return { session, reads, fetcher, router };
}
afterEach(() => { cleanup(); vi.useRealTimers(); vi.restoreAllMocks(); });

it('replaces bounded pages, exposes page scope and links by stable identity without v1 reads or writes', async () => {
  const state = await setup(url => json(url.searchParams.has('cursor') ? page(101) : { ...page(1, 'second-page'), items: Array.from({ length: 100 }, (_, i) => incident(i + 1)) }));
  await screen.findByRole('link', { name: 'service-1' });
  expect(screen.getByLabelText('Incident page counts')).toHaveTextContent('100 latest incidents; 100 open');
  expect(screen.getByLabelText('Incident page counts')).toHaveTextContent('page counts, not fleet totals');
  expect(state.reads[0].searchParams.get('limit')).toBe('100');
  fireEvent.click(screen.getByRole('button', { name: 'Next incident page' }));
  const link = await screen.findByRole('link', { name: 'service-101' });
  expect(screen.queryByRole('link', { name: 'service-1' })).not.toBeInTheDocument();
  expect(state.reads[1].searchParams.get('cursor')).toBe('second-page');
  expect(link).toHaveAttribute('href', '/monitors/by-id/service-101');
  fireEvent.click(link);
  await screen.findByText('Stable monitor observation');
  expect(state.router.state.location.pathname).toBe('/monitors/by-id/service-101');
  expect(state.reads.every(url => url.pathname === '/api/v2/incidents')).toBe(true);
  expect(state.fetcher.mock.calls.every(([, init]) => init?.method === 'GET')).toBe(true);
});

it('resets the cursor when applying an exact monitor filter', async () => {
  const state = await setup(() => json(page(1, 'more')));
  await screen.findByRole('link', { name: 'service-1' });
  fireEvent.click(screen.getByRole('button', { name: 'Next incident page' }));
  await waitFor(() => expect(state.reads).toHaveLength(2));
  await waitFor(() => expect(screen.getByRole('button', { name: 'Apply filter' })).toBeEnabled());
  fireEvent.change(screen.getByLabelText('Exact monitor ID (optional)'), { target: { value: 'service-1' } });
  fireEvent.click(screen.getByRole('button', { name: 'Apply filter' }));
  await waitFor(() => expect(state.reads).toHaveLength(3));
  expect(state.reads[2].searchParams.get('monitorID')).toBe('service-1');
  expect(state.reads[2].searchParams.has('cursor')).toBe(false);
  expect(state.reads.every(url => !url.href.includes('private-tab-token'))).toBe(true);
});

it('does not fall back to legacy observations when ListIncidents is not permitted', async () => {
  const state = await setup(() => json({}), ['GetMonitor']);
  expect(screen.getByRole('status')).toHaveTextContent('cannot list incidents');
  expect(state.reads).toHaveLength(0);
});

it('stops at an expired snapshot until an explicit refresh', async () => {
  vi.useFakeTimers();
  const state = await setup(url => url.searchParams.has('cursor') ? json({}, 410) : json(page(1, 'expired')));
  await act(async () => { await vi.advanceTimersByTimeAsync(100); });
  fireEvent.click(screen.getByRole('button', { name: 'Next incident page' }));
  await act(async () => { await vi.advanceTimersByTimeAsync(100); });
  expect(screen.getByRole('alert')).toHaveTextContent('snapshot expired');
  await act(async () => { await vi.advanceTimersByTimeAsync(60_000); });
  expect(state.reads).toHaveLength(2);
  fireEvent.click(screen.getByRole('button', { name: 'Refresh from first page' }));
  await act(async () => { await vi.advanceTimersByTimeAsync(100); });
  expect(state.reads).toHaveLength(3);
  expect(state.reads[2].searchParams.has('cursor')).toBe(false);
});

it('rejects changed snapshots and does not show a misleading empty fleet', async () => {
  await setup(url => json(url.searchParams.has('cursor') ? page(2, '', 'different') : page(1, 'next')));
  await screen.findByRole('link', { name: 'service-1' });
  fireEvent.click(screen.getByRole('button', { name: 'Next incident page' }));
  expect(await screen.findByRole('alert')).toHaveTextContent('does not establish that incidents are resolved');
  expect(screen.queryByRole('link', { name: 'service-2' })).not.toBeInTheDocument();
});

it('shows acknowledgment, dismissed notifications and closed incidents without inferring check health', async () => {
  await setup(() => json({ ...page(1), items: [{ ...incident(1), state: 'closed', acknowledgedBy: 'alice', acknowledgedAt: '2026-09-19T12:05:00Z', dismissed: true, closedAt: '2026-09-19T12:20:00Z' }] }));
  await screen.findByRole('link', { name: 'service-1' });
  expect(screen.getByLabelText('Incident page counts')).toHaveTextContent('0 open; 1 acknowledged; 1 dismissed');
  expect(screen.getByRole('region', { name: 'Latest incident page' })).toHaveTextContent('alice');
  expect(screen.getByText('Dismissed for this incident')).toBeVisible();
  expect(screen.queryByText(/fleet is healthy/i)).not.toBeInTheDocument();
});

it('strips unrelated payloads and distinguishes missing, false and unknown observations', async () => {
  let result: unknown = { ...page(1), items: [{ ...incident(1), state: 'future-state', dismissed: false, acknowledgedAt: '0001-01-01T00:00:00Z', closedAt: '0001-01-01T00:00:00Z', spec: { token: 'private-secret' } }] };
  const state = await setup(() => json(result));
  const clean = await listIncidents(state.session);
  expect(JSON.stringify(clean)).not.toMatch(/private-secret|0001-01-01/);
  expect(clean.items[0].state).toBe('unrecognized');
  expect(clean.items[0].dismissed).toBe(false);
  for (const bad of [
    { ...page(1), items: [incident(1), incident(1)] },
    { ...page(1), items: [incident(1), { ...incident(2), monitorID: 'service-1' }] },
    { ...page(1), items: Array.from({ length: 101 }, (_, i) => incident(i + 1)) },
    { ...page(1, 'next'), snapshot: '' }, { ...page(1), generatedAt: 'not-a-time' },
    { ...page(1), items: [{ ...incident(1), dismissed: null }] },
  ]) {
    result = bad;
    await expect(listIncidents(state.session)).rejects.toThrow();
  }
  result = page(1, 'same');
  await expect(listIncidents(state.session, 'same')).rejects.toThrow('did not advance');
  await expect(listIncidents(state.session, '', 'another-monitor')).rejects.toThrow();
});

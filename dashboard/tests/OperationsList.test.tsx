import { afterEach, expect, it, vi } from 'vitest';
import { act, cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { createMemoryRouter, RouterProvider } from 'react-router-dom';
import { DashboardSession } from '../src/api/session';
import { collectionIdentityFormat } from '../src/api/collectionInventory';
import { listOperations } from '../src/api/operations';
import { SessionBoundary } from '../src/auth/SessionBoundary';
import OperationDetail from '../src/pages/OperationDetail';

const id = (sequence: number) => `op.c47948b9-084b-4109-a862-0312e7a77102.${String(sequence).padStart(20, '0')}`;
const receipt = (sequence: number) => ({ id: id(sequence), state: 'committed', committed: 1, applied: 0, contentDigest: 'a'.repeat(64) });
const page = (sequence: number, nextCursor = '', snapshot = 'fixed-snapshot') => ({ items: [receipt(sequence)], nextCursor, snapshot, generatedAt: '2026-09-14T12:00:00Z' });
const json = (value: unknown, status = 200) => new Response(JSON.stringify(value), { status });

async function setup(reply: (url: URL) => Response, permissions = ['ListOperations', 'GetOperation']) {
  const reads: URL[] = [];
  const fetcher = vi.fn<typeof fetch>().mockImplementation(async (input, init) => {
    const url = new URL(String(input));
    if (url.pathname === '/api/v2/self') return new Headers(init?.headers).has('Authorization') ? json({ principalId: 'reader', role: 'reader', permissions }) : json({}, 401);
    reads.push(url);
    return reply(url);
  });
  const session = new DashboardSession({ origin: 'https://cpra.example', fetch: fetcher });
  await session.discover(); await session.signIn('private-tab-token');
  const router = createMemoryRouter([{ path: '/operations/:id?', element: <SessionBoundary session={session}><OperationDetail /></SessionBoundary> }], { initialEntries: ['/operations'] });
  render(<RouterProvider router={router} />);
  return { session, reads, fetcher, router };
}

afterEach(() => { cleanup(); vi.useRealTimers(); vi.restoreAllMocks(); });

it('replaces one bounded page at a time and opens the exact original receipt without writes', async () => {
  const state = await setup(url => url.pathname === '/api/v2/operations' ? json(url.searchParams.has('cursor') ? page(2) : page(1, 'original-page-two')) : json(receipt(2)));
  await screen.findByRole('link', { name: id(1) });
  expect(state.reads[0].searchParams.get('limit')).toBe('100');
  fireEvent.click(screen.getByRole('button', { name: 'Next operation page' }));
  await screen.findByRole('link', { name: id(2) });
  expect(screen.queryByRole('link', { name: id(1) })).not.toBeInTheDocument();
  expect(state.reads[1].searchParams.get('cursor')).toBe('original-page-two');
  fireEvent.click(screen.getByRole('link', { name: id(2) }));
  await screen.findByRole('heading', { name: 'Committed' });
  expect(state.router.state.location.pathname).toBe(`/operations/${id(2)}`);
  expect(state.fetcher.mock.calls.every(([, init]) => init?.method === 'GET')).toBe(true);
});

it('uses an explicit exact monitor filter and resets the old cursor when changing the selection', async () => {
  const state = await setup(() => json(page(1, 'more')));
  await screen.findByRole('link', { name: id(1) });
  fireEvent.click(screen.getByRole('button', { name: 'Next operation page' }));
  await waitFor(() => expect(state.reads).toHaveLength(2));
  await waitFor(() => expect(screen.getByRole('button', { name: 'Apply filter' })).toBeEnabled());
  fireEvent.change(screen.getByLabelText('Exact monitor ID (optional)'), { target: { value: 'payments-api' } });
  fireEvent.click(screen.getByRole('button', { name: 'Apply filter' }));
  await waitFor(() => expect(state.reads).toHaveLength(3));
  expect(state.reads[2].searchParams.get('monitorID')).toBe('payments-api');
  expect(state.reads[2].searchParams.has('cursor')).toBe(false);
  expect(state.reads[2].searchParams.has('selector')).toBe(false);
  expect(state.reads.every(url => !url.href.includes('private-tab-token'))).toBe(true);
});

it('does not request a list without its exact permission and preserves manual lookup', async () => {
  const state = await setup(() => json({}), ['GetOperation']);
  expect(screen.getByRole('form', { name: 'Find operation' })).toBeInTheDocument();
  expect(screen.queryByRole('heading', { name: 'Retained operations' })).not.toBeInTheDocument();
  expect(state.reads).toHaveLength(0);
});

it('stops at expired cursors and only creates a fresh snapshot after an explicit refresh', async () => {
  vi.useFakeTimers();
  const state = await setup(url => url.searchParams.has('cursor') ? json({}, 410) : json(page(1, 'expired-next')));
  await act(async () => { await vi.advanceTimersByTimeAsync(100); });
  fireEvent.click(screen.getByRole('button', { name: 'Next operation page' }));
  await act(async () => { await vi.advanceTimersByTimeAsync(100); });
  expect(screen.getByRole('alert')).toHaveTextContent('snapshot expired');
  await act(async () => { await vi.advanceTimersByTimeAsync(60_000); });
  expect(state.reads).toHaveLength(2);
  fireEvent.click(screen.getByRole('button', { name: 'Refresh from first page' }));
  await act(async () => { await vi.advanceTimersByTimeAsync(100); });
  expect(state.reads).toHaveLength(3);
  expect(state.reads[2].searchParams.has('cursor')).toBe(false);
});

it('rejects a changed snapshot instead of combining incompatible pages', async () => {
  await setup(url => json(url.searchParams.has('cursor') ? page(2, '', 'different-snapshot') : page(1, 'next')));
  await screen.findByRole('link', { name: id(1) });
  fireEvent.click(screen.getByRole('button', { name: 'Next operation page' }));
  await screen.findByRole('alert');
  expect(screen.queryByRole('link', { name: id(2) })).not.toBeInTheDocument();
});

it('permits bounded empty scan pages with a continuation without inventing an empty history result', async () => {
  await setup(() => json({ ...page(1, 'next-scan-part'), items: [] }));
  await screen.findByText('No matching operations in this part of the snapshot. Continue to the next page.');
  expect(screen.getByRole('button', { name: 'Next operation page' })).toBeEnabled();
});

it('strips extra receipt payloads and rejects duplicate or excessive page members', async () => {
  let result: unknown = { ...page(1), items: [{ ...receipt(1), provider: 'private-provider-config', request: { value: 'private-secret' } }] };
  const state = await setup(() => json(result));
  const clean = await listOperations(state.session);
  expect(JSON.stringify(clean)).not.toContain('private-');
  for (const bad of [
    { ...page(1), items: [receipt(1), receipt(1)] },
    { ...page(1), items: Array.from({ length: 101 }, (_, index) => receipt(index + 1)) },
    { ...page(1, 'next'), snapshot: '' },
    { ...page(1, 'next'), generatedAt: 'not-a-time' },
    { ...page(1, 'next'), generatedAt: '0001-01-01T00:00:00Z' },
  ]) {
    result = bad;
    await expect(listOperations(state.session)).rejects.toThrow();
  }
  result = page(1, 'unchanged');
  await expect(listOperations(state.session, 'unchanged')).rejects.toThrow('did not advance');
});

 it('continues an empty scan page to a collection receipt without inventing missing resource outcomes', async () => {
  const collection = { ...receipt(2), state: 'validated', identityFormat: collectionIdentityFormat, itemCount: 2, uploaded: 2, committed: 0, applied: 0, validated: true, items: [] };
  const state = await setup(url => url.pathname === '/api/v2/operations' ? json(url.searchParams.has('cursor') ? { ...page(2), items: [collection] } : { ...page(1, 'next-scan'), items: [] }) : json(collection));
  await screen.findByText('No matching operations in this part of the snapshot. Continue to the next page.');
  fireEvent.click(screen.getByRole('button', { name: 'Next operation page' }));
  await screen.findByRole('link', { name: id(2) });
  expect(screen.getByText('Collection · 2 resources · 2 uploaded')).toBeInTheDocument();
  fireEvent.click(screen.getByRole('link', { name: id(2) }));
  await screen.findByRole('heading', { name: 'Validated' });
  expect(screen.getByText('Your identity cannot read collection validation results.')).toBeInTheDocument();
  expect(state.reads).toHaveLength(3);
  expect(state.reads.every(url => !url.pathname.endsWith('/validation'))).toBe(true);
});

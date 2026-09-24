import { afterEach, expect, it, vi } from 'vitest';
import { act, cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react';
import { createMemoryRouter, RouterProvider } from 'react-router-dom';
import { DashboardSession } from '../src/api/session';
import { operationApplied, operationView, supportedCollectionIdentity } from '../src/api/operations';
import { collectionIdentityFormat } from '../src/api/collectionInventory';
import { SessionBoundary } from '../src/auth/SessionBoundary';
import OperationDetail from '../src/pages/OperationDetail';

const id = 'op.c47948b9-084b-4109-a862-0312e7a77102.00000000000000000001';
const identity = { identityFormat: collectionIdentityFormat, contentDigest: 'a'.repeat(64), itemCount: 2 };
const operation = (overrides: Record<string, unknown> = {}) => ({ ...identity, id, state: 'validated', uploaded: 2, committed: 0, applied: 0, validated: true, ...overrides });
const summary = { resultID: '11111111-1111-1111-1111-111111111111', valid: true, summaryOnly: false, count: 2,
  digest: 'b'.repeat(64), capabilitiesDigest: 'c'.repeat(64), planID: '22222222-2222-2222-2222-222222222222', planDigest: 'd'.repeat(64),
  finalizedAt: '2026-09-20T00:00:00Z', expiresAt: '2026-10-20T00:00:00Z' };
const item = (ordinal: number) => ({ ordinal, kind: 'Monitor', id: `monitor-${ordinal}`, source: 'source.00000000000000000001', sourceDocument: ordinal, sourceItem: 1, change: 'create' });
const result = (second = false) => ({ ...identity, operationID: id, summary, items: [item(second ? 2 : 1)], ...(second ? {} : { nextCursor: 'original-next-page' }) });
const json = (value: unknown, status = 200, headers: HeadersInit = {}) => new Response(JSON.stringify(value), { status, headers });

async function setup(options: { receipt?: Record<string, unknown>; permissions?: string[]; reply?: (url: URL, init?: RequestInit) => Response | Promise<Response> } = {}) {
  const reads: { url: URL; signal?: AbortSignal | null }[] = [];
  let current = options.receipt ?? operation();
  const fetcher = vi.fn<typeof fetch>().mockImplementation(async (input, init) => {
    const url = new URL(String(input));
    if (url.pathname === '/api/v2/self') return new Headers(init?.headers).has('Authorization')
      ? json({ principalId: 'reader', role: 'reader', permissions: options.permissions ?? ['GetOperation', 'GetOperationValidation'] }) : json({}, 401);
    if (url.pathname.endsWith('/validation')) { reads.push({ url, signal: init?.signal }); return options.reply?.(url, init) ?? json(result(url.searchParams.has('cursor'))); }
    return json(current);
  });
  const session = new DashboardSession({ origin: 'https://cpra.example', fetch: fetcher });
  await session.discover(); await session.signIn('private-reader-token');
  const router = createMemoryRouter([{ path: '/operations/:id?', element: <SessionBoundary session={session}><OperationDetail /></SessionBoundary> }], { initialEntries: [`/operations/${id}`] });
  render(<RouterProvider router={router} />);
  return { session, reads, router, fetcher, change: (value: Record<string, unknown>) => { current = value; } };
}
const read = async () => { fireEvent.click(await screen.findByRole('button', { name: 'Read original validation result' })); };
afterEach(() => { cleanup(); vi.useRealTimers(); vi.restoreAllMocks(); });

it('keeps only bounded collection identity metadata, including explicit rejection and unknown read-only formats', () => {
  const clean = operationView(operation({ validated: false, request: { secret: 'not-retained' }, admissionTicket: 'not-retained' }));
  expect(clean).toMatchObject({ ...identity, uploaded: 2, validated: false });
  expect(JSON.stringify(clean)).not.toContain('not-retained');
  expect(supportedCollectionIdentity(clean)).toEqual(identity);
  const unknown = operationView(operation({ identityFormat: 'future.collection.v2' }));
  expect(unknown.identityFormat).toBe('future.collection.v2'); expect(supportedCollectionIdentity(unknown)).toBeUndefined();
  expect(operationApplied({ ...clean, state: 'completed', committed: 2, applied: 2, items: [] })).toBe(false);
  const pending = operationView(operation({ validated: undefined, state: 'validating' }));
  expect(pending.validated).toBeUndefined();
});

it.each([
  { identityFormat: undefined }, { itemCount: undefined }, { identityFormat: null }, { identityFormat: '' }, { identityFormat: 'x'.repeat(129) },
  { itemCount: null }, { itemCount: -1 }, { itemCount: 10_000_001 }, { itemCount: 1.5 }, { contentDigest: `sha256:${'a'.repeat(64)}` },
  { uploaded: 3 }, { committed: 3 }, { applied: 3 }, { applied: 1, committed: 0 }, { validated: null }, { validated: 'false' },
])('rejects malformed collection inventory/progress %# before any retained result read', invalid => {
  expect(() => operationView(operation(invalid))).toThrow();
});

it('reads results explicitly, replaces one page, and refreshes the original summary without any mutation', async () => {
  const state = await setup({ reply: url => json({ ...result(url.searchParams.has('cursor')), provider: 'private-provider-value', filename: '/private/source.yaml' }) });
  await screen.findByRole('heading', { name: 'Validated' });
  expect(state.reads).toHaveLength(0);
  expect(screen.getByRole('region', { name: 'Operation receipt' })).toHaveTextContent('Collection: 2 resources');
  expect(screen.queryByText('Item-level outcomes are not reported.')).not.toBeInTheDocument();
  await read();
  let rows = await screen.findByRole('region', { name: 'Original validation resource results' });
  expect(rows).toHaveTextContent('monitor-1'); expect(rows).not.toHaveTextContent('monitor-2');
  expect(screen.getByText(/Original verdict:/)).toHaveTextContent('Passed');
  expect(state.reads[0].url.search).toBe('?limit=100');
  fireEvent.click(screen.getByRole('button', { name: 'Next validation page' }));
  await waitFor(() => expect(screen.getByRole('region', { name: 'Original validation resource results' })).toHaveTextContent('monitor-2'));
  rows = screen.getByRole('region', { name: 'Original validation resource results' });
  expect(rows).not.toHaveTextContent('monitor-1');
  expect(state.reads[1].url.searchParams.get('cursor')).toBe('original-next-page');
  expect(screen.queryByRole('button', { name: 'Next validation page' })).not.toBeInTheDocument();
  fireEvent.click(screen.getByRole('button', { name: 'Refresh validation from first page' }));
  await waitFor(() => expect(screen.getByRole('region', { name: 'Original validation resource results' })).toHaveTextContent('monitor-1'));
  expect(state.reads[2].url.search).toBe('?limit=100');
  expect(document.body.textContent).not.toMatch(/private-provider-value|private\/source|private-reader-token/);
  expect(state.reads.every(({ url }) => url.pathname === `/api/v2/operations/${id}/validation` && !url.href.includes('private-reader-token'))).toBe(true);
  expect(state.fetcher.mock.calls.every(([, init]) => init?.method === 'GET')).toBe(true);
});

it('keeps the receipt phase independent of the retained verdict and distinguishes false from pending', async () => {
  const invalid = { ...summary, valid: false, planID: undefined, planDigest: undefined, issue: 'invalidResource' };
  await setup({ receipt: operation({ state: 'canceled', validated: false }), reply: () => json({ ...result(), summary: invalid, items: [{ ...item(1), change: undefined, issue: 'notEvaluated' }] }) });
  await screen.findByRole('heading', { name: 'Canceled' });
  expect(screen.getByRole('region', { name: 'Operation receipt' })).toHaveTextContent('Validation: Rejected');
  await read(); await screen.findByRole('region', { name: 'Original validation resource results' });
  expect(screen.getByRole('heading', { name: 'Canceled' })).toBeInTheDocument();
  expect(screen.getByText(/Original verdict:/)).toHaveTextContent('Rejected');
  expect(screen.getByText('Not evaluated')).toBeInTheDocument();
});

it('reports authenticated pending without hidden validation polling or submitting another request', async () => {
  vi.useFakeTimers();
  const state = await setup({ receipt: operation({ state: 'uploading', validated: undefined }), reply: () => json({ code: 'validationPending' }, 409, { 'X-Operation-ID': id, 'Retry-After': '13' }) });
  await act(async () => { await vi.advanceTimersByTimeAsync(50); });
  expect(screen.getByRole('region', { name: 'Operation receipt' })).toHaveTextContent('Validation: Not reported');
  fireEvent.click(screen.getByRole('button', { name: 'Read original validation result' }));
  await act(async () => { await vi.advanceTimersByTimeAsync(50); });
  const region = screen.getByRole('region', { name: 'Original collection validation' });
  expect(within(region).getByRole('status')).toHaveTextContent('Read again after 13 seconds');
  expect(region).not.toHaveTextContent('Original verdict:');
  await act(async () => { await vi.advanceTimersByTimeAsync(60_000); });
  expect(state.reads).toHaveLength(1);
  expect(state.fetcher.mock.calls.every(([, init]) => init?.method === 'GET')).toBe(true);
});

it.each([
  [409, 'validationNotRequested', 'No validation request has been committed'],
  [409, 'validationInterrupted', 'No verdict is available'], [409, 'validationCanceled', 'canceled before a verdict'],
  [503, 'historyUnavailable', 'history is unavailable'], [403, 'forbidden', 'identity cannot read'],
  [404, 'notFound', 'unavailable to this identity'], [410, 'operationExpired', 'evidence has expired'],
])('reports original result %s/%s without manufacturing a verdict or falling back', async (status, code, expected) => {
  const state = await setup({ reply: () => json({ code, detail: 'secret-provider-detail' }, status) });
  await read(); const region = screen.getByRole('region', { name: 'Original collection validation' });
  await waitFor(() => expect(within(region).getByRole('alert')).toHaveTextContent(expected));
  expect(region).not.toHaveTextContent('Original verdict:'); expect(region).not.toHaveTextContent('secret-provider-detail');
  expect(state.reads).toHaveLength(1); expect(state.fetcher.mock.calls.every(([, init]) => init?.method === 'GET')).toBe(true);
});

it('requires the exact read permission without inferring it from an accessible operation', async () => {
  const state = await setup({ permissions: ['GetOperation'] });
  await screen.findByText('Your identity cannot read collection validation results.');
  expect(screen.queryByRole('button', { name: 'Read original validation result' })).not.toBeInTheDocument();
  expect(state.reads).toHaveLength(0);
});

it('shows unknown identity formats as observations without making unsupported result requests', async () => {
  const state = await setup({ receipt: operation({ identityFormat: 'future.collection.v2' }) });
  await screen.findByText(/This collection uses an identity format/);
  expect(screen.getByText('future.collection.v2')).toBeInTheDocument();
  expect(screen.queryByRole('button', { name: 'Read original validation result' })).not.toBeInTheDocument();
  expect(state.reads).toHaveLength(0);
});

it('pins the original summary through next-page and hidden-result refreshes', async () => {
  let changed = false;
  const state = await setup({ reply: url => json({ ...result(url.searchParams.has('cursor')), summary: changed ? { ...summary, digest: 'e'.repeat(64) } : summary }) });
  await read(); await screen.findByRole('region', { name: 'Original validation resource results' });
  fireEvent.click(screen.getByRole('button', { name: 'Hide validation result' }));
  expect(screen.queryByText(summary.resultID)).not.toBeInTheDocument();
  changed = true; await read();
  await waitFor(() => expect(within(screen.getByRole('region', { name: 'Original collection validation' })).getByRole('alert')).toHaveTextContent('could not be confirmed'));
  expect(screen.queryByRole('region', { name: 'Original validation resource results' })).not.toBeInTheDocument();
  expect(state.reads).toHaveLength(2);
});

it('clears the preceding page on actor denial without showing stale rows as readable', async () => {
  let denied = false;
  await setup({ reply: () => denied ? json({}, 403) : json(result()) });
  await read(); await screen.findByRole('region', { name: 'Original validation resource results' });
  denied = true; fireEvent.click(screen.getByRole('button', { name: 'Next validation page' }));
  await waitFor(() => expect(within(screen.getByRole('region', { name: 'Original collection validation' })).getByRole('alert')).toHaveTextContent('identity cannot read'));
  expect(screen.queryByText('monitor-1')).not.toBeInTheDocument(); expect(screen.queryByText(summary.resultID)).not.toBeInTheDocument();
});

it('cancels only the in-flight read and discards a late response after stopping', async () => {
  let resolve!: (response: Response) => void;
  const state = await setup({ reply: () => new Promise<Response>(done => { resolve = done; }) });
  await read(); await waitFor(() => expect(state.reads).toHaveLength(1));
  fireEvent.click(screen.getByRole('button', { name: 'Stop reading validation' }));
  expect(state.reads[0].signal?.aborted).toBe(true);
  await act(async () => { resolve(json(result())); });
  expect(screen.queryByText('monitor-1')).not.toBeInTheDocument();
  expect(screen.getByText('Reading stopped. The operation was not canceled or changed.')).toBeInTheDocument();
  expect(state.fetcher.mock.calls.every(([, init]) => init?.method === 'GET')).toBe(true);
});

it('clears retained rows on sign-out and aborts an in-flight next page', async () => {
  const state = await setup({ reply: url => url.searchParams.has('cursor') ? new Promise<Response>(() => {}) : json(result()) });
  await read(); await screen.findByText('monitor-1');
  fireEvent.click(screen.getByRole('button', { name: 'Next validation page' }));
  await waitFor(() => expect(state.reads).toHaveLength(2));
  await act(async () => { state.session.signOut(); });
  expect(state.reads[1].signal?.aborted).toBe(true);
  expect(screen.queryByText('monitor-1')).not.toBeInTheDocument(); expect(screen.queryByText(summary.resultID)).not.toBeInTheDocument();
  expect(screen.getByRole('heading', { name: 'Sign in to CPRa' })).toBeInTheDocument();
});

it('removes already-read validation when a later operation receipt denies access', async () => {
  vi.useFakeTimers();
  const state = await setup({ receipt: operation({ state: 'validating', validated: undefined }) });
  await act(async () => { await vi.advanceTimersByTimeAsync(50); });
  fireEvent.click(screen.getByRole('button', { name: 'Read original validation result' }));
  await act(async () => { await vi.advanceTimersByTimeAsync(50); });
  expect(screen.getByText('monitor-1')).toBeInTheDocument();
  state.fetcher.mockImplementationOnce(async () => json({}, 403));
  await act(async () => { await vi.advanceTimersByTimeAsync(5000); });
  expect(screen.queryByRole('region', { name: 'Original collection validation' })).not.toBeInTheDocument();
  expect(screen.queryByText('monitor-1')).not.toBeInTheDocument();
  expect(screen.getByRole('alert')).toHaveTextContent('identity cannot read this operation');
  const requests = state.fetcher.mock.calls.length;
  await act(async () => { await vi.advanceTimersByTimeAsync(60_000); });
  expect(state.fetcher.mock.calls).toHaveLength(requests);
});

it('aborts a result read when navigating to another original operation and does not show its late rows', async () => {
  let resolve!: (response: Response) => void;
  const state = await setup({ reply: () => new Promise<Response>(done => { resolve = done; }) });
  await read(); await waitFor(() => expect(state.reads).toHaveLength(1));
  const nextID = id.replace(/1$/, '2');
  state.change(operation({ id: nextID }));
  await act(async () => { await state.router.navigate(`/operations/${nextID}`); });
  expect(state.reads[0].signal?.aborted).toBe(true);
  await act(async () => { resolve(json(result())); });
  await screen.findByRole('heading', { name: 'Validated' });
  expect(screen.queryByText('monitor-1')).not.toBeInTheDocument();
  expect(state.reads).toHaveLength(1);
});

it('rejects a pending reply that does not confirm the requested operation identity', async () => {
  await setup({ reply: () => json({ code: 'validationPending' }, 409, { 'Retry-After': '5' }) });
  await read();
  await waitFor(() => expect(within(screen.getByRole('region', { name: 'Original collection validation' })).getByRole('alert')).toHaveTextContent('could not be confirmed'));
  expect(screen.queryByText(/Validation is pending for this original operation/)).not.toBeInTheDocument();
});

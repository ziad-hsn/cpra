import { afterEach, expect, it, vi } from 'vitest';
import { act, cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { createMemoryRouter, RouterProvider } from 'react-router-dom';
import { CollectionExecution } from '../src/components/CollectionExecution';
import { SessionBoundary } from '../src/auth/SessionBoundary';
import { DashboardSession } from '../src/api/session';
import { collectionIdentityFormat } from '../src/api/collectionInventory';
import type { ExecutionResultAvailability, Operation } from '../src/api/generated';
import OperationDetail from '../src/pages/OperationDetail';
import { operationMessage } from '../src/api/operations';

const id = 'original-operation';
const identity = { identityFormat: collectionIdentityFormat, contentDigest: 'a'.repeat(64), itemCount: 2 };
const counts = { processed: 2, accepted: 1, unchanged: 0, conflicts: 1, dependencyBlocked: 0, unattempted: 0, childPending: 0, childApplied: 0, childFailed: 1, childSuperseded: 0, childInvalidated: 0 };
const summary = { ...counts, resultID: '11111111-1111-4111-8111-111111111111', uploadID: '22222222-2222-4222-8222-222222222222', planID: '33333333-3333-4333-8333-333333333333', planDigest: 'b'.repeat(64), outcome: 'partial', itemCount: 2, bytes: 700, digest: 'c'.repeat(64), finalizedAt: '2026-09-23T12:00:00Z', expiresAt: '2026-10-23T12:00:00Z' };
const ready: ExecutionResultAvailability = { state: 'ready', counts, summary };
function result(second = false): Operation {
  return { ...identity, id, state: 'partial', committed: 1, applied: 0, executionResult: structuredClone(ready),
    items: [{ id: `Monitor/service-${second ? 2 : 1}`, kind: 'Monitor', inputOrdinal: second ? 2 : 1, planOrdinal: second ? 1 : 2, source: 'source.00000000000000000001', sourceDocument: 2, sourceItem: second ? 2 : 1,
      decidedAt: summary.finalizedAt, committedIndex: second ? 12 : 11, catalogDecision: second ? 'conflict' : 'accepted', outcome: second ? 'conflict' : 'accepted', committed: !second,
      ...(second ? {} : { originalUID: 'original-uid', uid: 'original-uid', oldVersion: 'old', newVersion: 'new', generation: 2, applied: false, childDisposition: { operationID: 'original-child', state: 'failed', outcome: 'projection_failed', updatedAt: summary.finalizedAt } }),
    }], nextCursor: second ? undefined : 'original-continuation',
  };
}
const json = (value: unknown, status = 200, headers: HeadersInit = {}) => new Response(JSON.stringify(value), { status, headers });
async function setup(options: { observation?: ExecutionResultAvailability; permissions?: string[]; detail?: boolean; reply?: (url: URL, init?: RequestInit) => Response | Promise<Response> } = {}) {
  const reads: { url: URL; signal?: AbortSignal | null }[] = [];
  const fetcher = vi.fn<typeof fetch>().mockImplementation(async (input, init) => {
    const url = new URL(String(input));
    if (url.pathname === '/api/v2/self') return new Headers(init?.headers).has('Authorization')
      ? json({ principalId: 'operator', role: 'operator', permissions: options.permissions ?? ['GetOperation'] }) : json({}, 401);
    reads.push({ url, signal: init?.signal });
    return options.reply?.(url, init) ?? json(result(url.searchParams.has('cursor')));
  });
  const session = new DashboardSession({ origin: 'https://cpra.example', fetch: fetcher });
  await session.discover(); await session.signIn('private-test-token');
  const content = options.detail ? <OperationDetail /> : <CollectionExecution id={id} identity={identity} observation={options.observation ?? ready} />;
  const router = createMemoryRouter([{ path: '/operations/:id', element: <SessionBoundary session={session}>{content}</SessionBoundary> }, { path: '/away', element: <p>Away</p> }], { initialEntries: [`/operations/${id}`] });
  render(<RouterProvider router={router} />);
  return { reads, fetcher, session, router };
}
const read = async () => fireEvent.click(await screen.findByRole('button', { name: 'Read resource results' }));
afterEach(() => { cleanup(); vi.useRealTimers(); vi.restoreAllMocks(); });

it('describes completed collections only from known complete result counts', () => {
  const operation = result(); operation.state = 'completed';
  const unchanged = { ...counts, accepted: 0, unchanged: 2, conflicts: 0, childFailed: 0 };
  operation.executionResult = { state: 'ready', counts: unchanged, summary: { ...summary, ...unchanged, outcome: 'completed' } };
  expect(operationMessage(operation)).toBe('Every resource was already up to date. No new changes were committed.');
  const applied = { ...counts, accepted: 2, conflicts: 0, childFailed: 0, childApplied: 2 };
  operation.executionResult = { state: 'ready', counts: applied, summary: { ...summary, ...applied, outcome: 'completed' } };
  expect(operationMessage(operation)).toBe('Every resource was applied by the controller or was already up to date.');
  operation.executionResult.state = 'futureAvailability';
  expect(operationMessage(operation)).toContain('does not confirm');
});

it('separates decisions from controller results and replaces pages in original input order using only GET', async () => {
  const state = await setup({ reply: url => {
    const value = result(url.searchParams.has('cursor'));
    Object.assign(value.items![0], { providerError: 'private-provider', sourcePath: '/private/source.yaml' });
    return json(value);
  } });
  expect(state.reads).toHaveLength(0);
  await read();
  let table = await screen.findByRole('region', { name: 'Application resource results' });
  expect(table).toHaveTextContent('Monitor/service-1'); expect(table).toHaveTextContent('Change committed'); expect(table).toHaveTextContent('Application failed');
  expect(table).toHaveTextContent('original-child'); expect(screen.getByLabelText('Application counts')).toHaveTextContent('Committed changes: 1');
  fireEvent.click(screen.getByRole('button', { name: 'Next results page' }));
  await waitFor(() => expect(screen.getByRole('region', { name: 'Application resource results' })).toHaveTextContent('Monitor/service-2'));
  table = screen.getByRole('region', { name: 'Application resource results' });
  expect(table).not.toHaveTextContent('Monitor/service-1'); expect(table).toHaveTextContent('Version conflict'); expect(table).toHaveTextContent('No controller operation');
  expect(screen.getByText('Showing 2–2 of 2 resources in original input order.')).toBeInTheDocument();
  expect(state.reads.map(({ url }) => url.search)).toEqual(['?limit=100', '?limit=100&cursor=original-continuation']);
  expect(screen.queryByRole('button', { name: 'Next results page' })).not.toBeInTheDocument();
  fireEvent.click(screen.getByRole('button', { name: 'Refresh results from first page' }));
  await screen.findByText('Monitor/service-1');
  expect(document.body.textContent).not.toMatch(/private-provider|private\/source|private-test-token/);
  expect(state.fetcher.mock.calls.every(([, init]) => init?.method === 'GET')).toBe(true);
});

it('keeps the initial collection row page out of the ordinary receipt cache and renders explicit result browsing', async () => {
  const state = await setup({ detail: true });
  await screen.findByRole('heading', { name: 'Partial or superseded' });
  expect(screen.getByRole('region', { name: 'Operation receipt' })).not.toHaveTextContent('Monitor/service-1');
  expect(screen.queryByRole('region', { name: 'Operation item results' })).not.toBeInTheDocument();
  await read(); await screen.findByText('Monitor/service-1');
  expect(state.reads).toHaveLength(2);
});

it('waits for a canceled parent result independently and respects the longer retry interval', async () => {
  vi.useFakeTimers();
  let complete = false;
  const pending: ExecutionResultAvailability = { state: 'pending', counts: { ...counts, processed: 1, conflicts: 0, unattempted: 1, childPending: 1, childFailed: 0 } };
  const state = await setup({ observation: pending, reply: () => {
    if (!complete) return json({ ...identity, id, state: 'canceled', committed: 1, applied: 0, executionResult: pending, retryAfterSeconds: 7 }, 200, { 'Retry-After': '11' });
    const final = result(); final.state = 'canceled';
    const completed = { ...counts, processed: 1, conflicts: 0, unattempted: 1 };
    final.executionResult = { state: 'ready', counts: completed, summary: { ...summary, ...completed, outcome: 'canceled' } };
    return json(final);
  } });
  fireEvent.click(screen.getByRole('button', { name: 'Wait for resource results' }));
  await act(async () => { await vi.advanceTimersByTimeAsync(50); });
  expect(screen.getByText('Results are pending. Reading again in 11 seconds.')).toBeInTheDocument();
  await act(async () => { await vi.advanceTimersByTimeAsync(10_000); });
  expect(state.reads).toHaveLength(1); complete = true;
  await act(async () => { await vi.advanceTimersByTimeAsync(1000); });
  expect(screen.getByText('Monitor/service-1')).toBeInTheDocument();
  await act(async () => { await vi.advanceTimersByTimeAsync(60_000); });
  expect(state.reads).toHaveLength(2);
  expect(state.fetcher.mock.calls.every(([, init]) => init?.method === 'GET')).toBe(true);
});

it('stops a pending wait without canceling work or making later requests', async () => {
  vi.useFakeTimers();
  const pending = { state: 'pending' };
  const state = await setup({ observation: pending, reply: () => json({ ...identity, id, state: 'canceled', executionResult: pending }) });
  fireEvent.click(screen.getByRole('button', { name: 'Wait for resource results' }));
  await act(async () => { await vi.advanceTimersByTimeAsync(50); });
  fireEvent.click(screen.getByRole('button', { name: 'Stop waiting for results' }));
  await act(async () => { await vi.advanceTimersByTimeAsync(60_000); });
  expect(state.reads).toHaveLength(1); expect(screen.getByText('Waiting stopped. The operation was not canceled or changed.')).toBeInTheDocument();
});

it.each([[403, 'forbidden', 'identity cannot read'], [404, 'notFound', 'unavailable to this identity'], [410, 'cursorExpired', 'Read again from the first page'], [410, 'operationExpired', 'Committed changes were not reversed'], [503, 'historyUnavailable', 'does not establish success or failure']])('clears prior rows on %s/%s without displaying server error details', async (status, code, message) => {
  let error = false;
  await setup({ reply: () => error ? json({ code, detail: 'private-provider-detail' }, Number(status)) : json(result()) });
  await read(); await screen.findByText('Monitor/service-1'); error = true;
  fireEvent.click(screen.getByRole('button', { name: 'Next results page' }));
  await waitFor(() => expect(screen.getByRole('alert')).toHaveTextContent(message));
  expect(screen.queryByText('Monitor/service-1')).not.toBeInTheDocument(); expect(document.body.textContent).not.toContain('private-provider-detail');
  expect(screen.queryByText('Resource results are retained and ready to read.')).not.toBeInTheDocument();
  expect(screen.queryByLabelText('Application counts')).not.toBeInTheDocument();
  expect(screen.getByText('Current resource-result availability could not be confirmed.')).toBeInTheDocument();
});

it('pins the immutable summary through hiding and restarting pagination', async () => {
  let changed = false;
  await setup({ reply: () => { const value = result(); if (changed) value.executionResult!.summary!.digest = 'd'.repeat(64); return json(value); } });
  await read(); await screen.findByText('Monitor/service-1');
  fireEvent.click(screen.getByRole('button', { name: 'Hide resource results' })); changed = true;
  await read(); await screen.findByRole('alert');
  expect(screen.queryByRole('region', { name: 'Application resource results' })).not.toBeInTheDocument();
});

it('does not revive an invalidated ready observation when an explicit retry is stopped', async () => {
  let attempt = 0;
  const state = await setup({ reply: () => ++attempt === 1 ? json({ code: 'operationExpired' }, 410) : new Promise<Response>(() => {}) });
  await read(); await screen.findByRole('alert');
  await read(); await waitFor(() => expect(state.reads).toHaveLength(2));
  fireEvent.click(screen.getByRole('button', { name: 'Stop waiting for results' }));
  expect(state.reads[1].signal?.aborted).toBe(true);
  expect(screen.queryByText('Resource results are retained and ready to read.')).not.toBeInTheDocument();
  expect(screen.queryByLabelText('Application counts')).not.toBeInTheDocument();
  expect(screen.getByText('Current resource-result availability could not be confirmed.')).toBeInTheDocument();
});

it.each(['expired', 'futureAvailability'])('shows %s without treating it as success or automatically polling', async state => {
  const setupState = await setup({ observation: { state }, reply: () => json({ ...identity, id, state: 'canceled', executionResult: { state } }) });
  await read(); await waitFor(() => expect(setupState.reads).toHaveLength(1));
  expect(screen.queryByRole('region', { name: 'Application resource results' })).not.toBeInTheDocument();
  expect(screen.queryByRole('button', { name: 'Wait for resource results' })).not.toBeInTheDocument();
});

it('requires read permission', async () => {
  const state = await setup({ permissions: [] });
  expect(screen.getByText('Your identity cannot read application results.')).toBeInTheDocument();
  expect(screen.queryByRole('button', { name: 'Read resource results' })).not.toBeInTheDocument(); expect(state.reads).toHaveLength(0);
});

it.each(['stop', 'sign-out', 'navigate'])('aborts the read and discards late rows after %s', async action => {
  let resolve!: (value: Response) => void;
  const state = await setup({ reply: () => new Promise(done => { resolve = done; }) });
  await read(); await waitFor(() => expect(state.reads).toHaveLength(1));
  await act(async () => {
    if (action === 'stop') fireEvent.click(screen.getByRole('button', { name: 'Stop waiting for results' }));
    else if (action === 'sign-out') state.session.signOut();
    else await state.router.navigate('/away');
  });
  expect(state.reads[0].signal?.aborted).toBe(true);
  await act(async () => { resolve(json(result())); });
  expect(screen.queryByText('Monitor/service-1')).not.toBeInTheDocument();
});

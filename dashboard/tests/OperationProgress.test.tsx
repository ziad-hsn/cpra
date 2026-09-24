import { afterEach, expect, it, vi } from 'vitest';
import { act, cleanup, fireEvent, render, screen } from '@testing-library/react';
import { createMemoryRouter, RouterProvider } from 'react-router-dom';
import { DashboardSession, ManagementError, type APIResponse } from '../src/api/session';
import { operationApplied, operationMessage, operationPollInterval, operationView, type OperationObservation } from '../src/api/operations';
import { mutationReceipt } from '../src/api/resources';
import type { Operation } from '../src/api/generated';
import { SessionBoundary } from '../src/auth/SessionBoundary';
import OperationDetail from '../src/pages/OperationDetail';
import { fixture, resource } from './managementFixture';

const receipt = (state = 'committed', outcome = 'committed'): Operation => ({ id: 'original-operation', state, contentDigest: 'a'.repeat(64), committed: 1, applied: state === 'completed' ? 1 : 0,
  validated: true, items: [{ id: 'checkout', outcome, committed: true, applied: state === 'completed', oldVersion: 'rv-1', newVersion: 'rv-2' }] });
const json = (value: unknown, status = 200, headers: HeadersInit = {}) => new Response(JSON.stringify(value), { status, headers });
const wrapped = (data: OperationObservation, retryAfterMs = 0): APIResponse<OperationObservation> => ({ data, retryAfterMs, requestID: '', operationID: data.id, resourceVersion: '', status: 200 });

async function operationFixture(options: { loggedOut?: boolean; permission?: boolean; status?: number; retryAfter?: string; initial?: Operation; id?: string } = {}) {
  const id = options.id ?? 'original-operation';
  let current = options.initial ?? receipt();
  const fetcher = vi.fn<typeof fetch>().mockImplementation(async (input, init) => {
    const url = new URL(String(input));
    if (url.pathname === '/api/v2/self') return new Headers(init?.headers).has('Authorization') ? json({ principalId: 'reader', role: 'reader', permissions: options.permission === false ? [] : ['GetOperation'] }) : json({}, 401);
    if (url.pathname === `/api/v2/operations/${id}`) return json(current, options.status ?? 200, options.retryAfter ? { 'Retry-After': options.retryAfter } : {});
    return json({}, 404);
  });
  const session = new DashboardSession({ origin: 'https://cpra.example', fetch: fetcher });
  await session.discover();
  if (!options.loggedOut) await session.signIn('reader-token');
  const router = createMemoryRouter([{ path: '/operations/:id?', element: <SessionBoundary session={session}><OperationDetail /></SessionBoundary> }], { initialEntries: [`/operations/${id}`] });
  render(<RouterProvider router={router} />);
  return { session, router, fetcher, change: (value: Operation) => { current = value; }, reads: () => fetcher.mock.calls.filter(([url]) => String(url).includes('/api/v2/operations/')) };
}

afterEach(() => { cleanup(); vi.useRealTimers(); vi.restoreAllMocks(); });

const reservedReceipt = (id: string, outcome = 'reserved'): Operation => ({
  id, state: outcome === 'reserved' ? 'reserved' : 'failed', contentDigest: 'a'.repeat(64),
  uploaded: 1, committed: 0, applied: 0, validated: true,
  items: [{ id: 'checkout', outcome, committed: false, applied: false }],
});

it('polls one exact reserved handle through commit and application without treating reservation as a saved change', async () => {
  vi.useFakeTimers();
  const id = 'op.c47948b9-084b-4109-a862-0312e7a77102.00000000000000000002';
  const state = await operationFixture({ id, initial: reservedReceipt(id) });
  await act(async () => { await vi.advanceTimersByTimeAsync(100); });
  expect(screen.getByRole('heading', { name: 'Reserved' })).toBeInTheDocument();
  expect(screen.getByRole('status')).toHaveTextContent('No resource change or action has been committed');
  expect(screen.getByRole('status')).not.toHaveTextContent(/saved/i);
  expect(screen.getByRole('region', { name: 'Operation receipt' })).toHaveTextContent('Committed: 0 · Applied: 0');
  expect(screen.getByRole('region', { name: 'Operation item results' })).toHaveTextContent('Reserved; no resource change committed');
  expect(operationApplied(reservedReceipt(id))).toBe(false);
  expect(state.reads()).toHaveLength(1);

  state.change({ ...receipt(), id });
  await act(async () => { await vi.advanceTimersByTimeAsync(5000); });
  expect(state.reads()).toHaveLength(2);
  expect(screen.getByRole('status')).toHaveTextContent('Saved durably. Controller application has not yet been confirmed.');
  expect(screen.getByRole('region', { name: 'Operation receipt' })).toHaveTextContent('Committed: 1 · Applied: 0');

  state.change({ ...receipt('completed', 'applied'), id });
  await act(async () => { await vi.advanceTimersByTimeAsync(5000); });
  expect(screen.getByRole('status')).toHaveTextContent('Saved durably and applied by the controller.');
  expect(state.reads()).toHaveLength(3);
  await act(async () => { await vi.advanceTimersByTimeAsync(60_000); });
  expect(state.reads()).toHaveLength(3);
  expect(state.router.state.location.pathname).toBe(`/operations/${id}`);
  expect(screen.getByText(id, { exact: true })).toBeInTheDocument();
  expect(state.reads().every(([url, init]) => new URL(String(url)).pathname === `/api/v2/operations/${id}` && (!init?.method || init.method === 'GET'))).toBe(true);
});

it.each([
  ['activation_rejected', 'rejected before committing', 'Rejected before resource/action commit'],
  ['reservation_expired', 'unused operation reservation expired', 'Reservation expired; no resource/action commit'],
] as const)('ends polling after %s with zero committed changes and retains the original handle', async (outcome, message, itemMessage) => {
  vi.useFakeTimers();
  const id = 'op.c47948b9-084b-4109-a862-0312e7a77102.00000000000000000003';
  const terminal = reservedReceipt(id, outcome);
  const state = await operationFixture({ id, initial: reservedReceipt(id) });
  await act(async () => { await vi.advanceTimersByTimeAsync(100); });
  expect(state.reads()).toHaveLength(1);
  state.change(terminal);
  await act(async () => { await vi.advanceTimersByTimeAsync(5000); });
  expect(screen.getByRole('heading', { name: 'Failed' })).toBeInTheDocument();
  expect(screen.getByRole('status')).toHaveTextContent(message);
  expect(screen.getByRole('status')).not.toHaveTextContent(/saved/i);
  expect(screen.getByRole('region', { name: 'Operation receipt' })).toHaveTextContent('Committed: 0 · Applied: 0');
  expect(screen.getByRole('region', { name: 'Operation item results' })).toHaveTextContent(itemMessage);
  expect(operationApplied(operationView(terminal))).toBe(false);
  expect(operationPollInterval(wrapped(terminal))).toBe(false);
  await act(async () => { await vi.advanceTimersByTimeAsync(60_000); });
  expect(state.reads()).toHaveLength(2);
  expect(state.router.state.location.pathname).toBe(`/operations/${id}`);
  expect(screen.getByText(id, { exact: true })).toBeInTheDocument();
  expect(state.fetcher.mock.calls.every(([, init]) => !init?.method || init.method === 'GET')).toBe(true);
});

it('automatically polls only interpreted reserved and committed receipt states', () => {
  for (const state of ['reserved', 'committed']) expect(operationPollInterval(wrapped({ ...reservedReceipt('one-operation'), state }))).toBe(5000);
  for (const state of ['completed', 'failed', 'partial', 'unrecognized']) expect(operationPollInterval(wrapped({ ...reservedReceipt('one-operation'), state }))).toBe(false);
});

it('keeps the exact sequenced handle across committed progress and expiry without repeating work', async () => {
  vi.useFakeTimers();
  const id = 'op.c47948b9-084b-4109-a862-0312e7a77102.00000000000000000001';
  const state = await operationFixture({ id, initial: { ...receipt(), id } });
  await act(async () => { await vi.advanceTimersByTimeAsync(100); });
  expect(state.router.state.location.pathname).toBe(`/operations/${id}`);
  expect(screen.getByText(id, { exact: true })).toBeInTheDocument();
  expect(screen.getByRole('region', { name: 'Operation receipt' })).toHaveTextContent('Controller application has not yet been confirmed');
  expect(state.reads()).toHaveLength(1);
  state.fetcher.mockImplementationOnce(async () => json({ type: 'about:blank', status: 410, title: 'Operation expired' }, 410));
  await act(async () => { await vi.advanceTimersByTimeAsync(5000); });
  expect(screen.getByRole('alert')).toHaveTextContent('does not undo');
  await act(async () => { await vi.advanceTimersByTimeAsync(60_000); });
  expect(state.reads()).toHaveLength(2);
  expect(state.reads().every(([url, init]) => new URL(String(url)).pathname === `/api/v2/operations/${id}` && (!init?.method || init.method === 'GET'))).toBe(true);
});

it('polls a committed receipt at the longer server interval and stops after confirmed application', async () => {
  vi.useFakeTimers();
  const state = await operationFixture({ initial: { ...receipt(), retryAfterSeconds: 7 }, retryAfter: '11' });
  await act(async () => { await vi.advanceTimersByTimeAsync(100); });
  expect(screen.getByRole('region', { name: 'Operation receipt' })).toHaveTextContent('Controller application has not yet been confirmed');
  expect(state.reads()).toHaveLength(1);
  await act(async () => { await vi.advanceTimersByTimeAsync(10_000); });
  expect(state.reads()).toHaveLength(1);
  state.change(receipt('completed', 'applied'));
  await act(async () => { await vi.advanceTimersByTimeAsync(1000); });
  expect(state.reads()).toHaveLength(2);
  expect(screen.getByRole('status')).toHaveTextContent('Saved durably and applied');
  await act(async () => { await vi.advanceTimersByTimeAsync(60_000); });
  expect(state.reads()).toHaveLength(2);
  expect(state.fetcher.mock.calls.every(([, init]) => !init?.method || init.method === 'GET')).toBe(true);
});

it.each([['failed', 'projection_failed', 'controller application failed'], ['partial', 'superseded', 'superseded']] as const)('reports terminal %s without claiming controller application or repeating work', async (state, outcome, message) => {
  await operationFixture({ initial: receipt(state, outcome) });
  expect(await screen.findByRole('status')).toHaveTextContent(message);
  expect(screen.queryByText('Saved durably and applied by the controller.')).not.toBeInTheDocument();
});

it('does not claim a failed operation was saved when the server omits its committed count', async () => {
  const failed = receipt('failed', 'projection_failed');
  delete failed.committed;
  await operationFixture({ initial: failed });
  const status = await screen.findByRole('status');
  expect(status).not.toHaveTextContent(/saved durably/i);
  expect(screen.getByRole('region', { name: 'Operation receipt' })).toHaveTextContent('Committed: Not reported');
  expect(operationApplied(failed)).toBe(false);
  expect(operationPollInterval(wrapped(failed))).toBe(false);
});

it('does not invent a later committed mutation when a zero-commit reservation is superseded', async () => {
  const superseded: Operation = { ...reservedReceipt('op.c47948b9-084b-4109-a862-0312e7a77102.00000000000000000004', 'superseded'), state: 'partial' };
  await operationFixture({ id: superseded.id, initial: superseded });
  const status = await screen.findByRole('status');
  expect(status).toHaveTextContent(/superseded/i);
  expect(status).not.toHaveTextContent(/saved|newer committed change/i);
  expect(screen.getByRole('region', { name: 'Operation receipt' })).toHaveTextContent('Committed: 0 · Applied: 0');
  expect(operationApplied(superseded)).toBe(false);
  expect(operationPollInterval(wrapped(superseded))).toBe(false);
});

it('preserves an operation address through sign-in after a refresh and clears the receipt on logout', async () => {
  const state = await operationFixture({ loggedOut: true });
  expect(screen.getByRole('heading', { name: 'Sign in to CPRa' })).toBeInTheDocument();
  expect(state.reads()).toHaveLength(0);
  fireEvent.change(screen.getByLabelText('Bearer token'), { target: { value: 'reader-token' } });
  fireEvent.click(screen.getByRole('button', { name: 'Sign in' }));
  expect(await screen.findByRole('region', { name: 'Operation receipt' })).toBeInTheDocument();
  expect(state.router.state.location.pathname).toBe('/operations/original-operation');
  expect(state.reads()).toHaveLength(1);
  await act(async () => { state.session.signOut(); });
  expect(screen.queryByRole('region', { name: 'Operation receipt' })).not.toBeInTheDocument();
  expect(state.router.state.location.pathname).toBe('/operations/original-operation');
});

it.each([404, 410])('stops automatically polling an unavailable %s receipt and never treats absence as rollback', async status => {
  vi.useFakeTimers();
  const state = await operationFixture({ status });
  await act(async () => { await vi.advanceTimersByTimeAsync(100); });
  expect(screen.getByRole('alert')).toHaveTextContent(status === 410 ? 'does not undo' : 'does not prove');
  await act(async () => { await vi.advanceTimersByTimeAsync(60_000); });
  expect(state.reads()).toHaveLength(1);
  fireEvent.click(screen.getByRole('button', { name: 'Read receipt again' }));
  await act(async () => { await vi.advanceTimersByTimeAsync(100); });
  expect(state.reads()).toHaveLength(2);
});

it('requires the exact GetOperation permission and never substitutes an operation list', async () => {
  const state = await operationFixture({ permission: false });
  expect(await screen.findByRole('status')).toHaveTextContent('cannot read operation receipts');
  expect(state.reads()).toHaveLength(0);
  expect(state.fetcher.mock.calls.some(([url]) => String(url).endsWith('/api/v2/operations'))).toBe(false);
});

it('keeps echoed secrets, provider configuration and arbitrary messages out of operation state', () => {
  const view = operationView({ ...receipt(), spec: { value: 'private-secret' }, providerConfig: { password: 'private-password' }, items: [{ ...receipt().items![0], message: 'private-provider-message', parameters: 'private-parameters' }] });
  expect(JSON.stringify(view)).not.toMatch(/private-|providerConfig|parameters|message/);
  expect(() => operationView(receipt(), 'other-operation')).toThrow('unsupported operation receipt');
  expect(() => operationView({ ...receipt(), items: Array.from({ length: 501 }, () => receipt().items![0]) })).toThrow('500-item');
  expect(operationView({ ...receipt(), state: 'private-unknown-state' }).state).toBe('unrecognized');
  expect(operationMessage(receipt('completed', 'superseded'))).toContain('does not confirm');
  expect(operationApplied(receipt('committed', 'applied'))).toBe(false);
});

it('keeps resource response flags from proving application and accepts a complete delete receipt', () => {
  const resourceResponse = mutationReceipt({ ...wrapped(receipt()), data: { kind: 'Credential', spec: { value: 'private-secret' }, status: { applied: true } } });
  expect(resourceResponse.operationID).toBe('original-operation');
  expect(JSON.stringify(resourceResponse.data)).not.toMatch(/private-secret|applied/);
  const deletion = mutationReceipt(wrapped(receipt('completed', 'applied')));
  expect(deletion.data).toEqual({ operation: receipt('completed', 'applied') });
});

it('honors Retry-After errors and treats unrecognized or terminal receipts conservatively', () => {
  const retry = new ManagementError('http', 'unavailable', 503, undefined, undefined, { ...wrapped(receipt()), retryAfterMs: 45_000 });
  expect(operationPollInterval(undefined, retry)).toBe(45_000);
  expect(operationPollInterval(wrapped({ ...receipt(), retryAfterSeconds: 12 }, 7000))).toBe(12_000);
  expect(operationPollInterval(wrapped(receipt('failed', 'projection_failed')))).toBe(false);
  expect(operationPollInterval(wrapped(operationView({ ...receipt(), state: 'future-state' })))).toBe(false);
});

it('links a successful resource write to its receipt instead of trusting an echoed applied flag', async () => {
  const state = await fixture('recipients', 'oncall', { initial: [resource('recipients', 'oncall', { endpointRefs: ['ops-mail'] })] });
  fireEvent.click(await screen.findByRole('button', { name: 'Edit recipient' }));
  fireEvent.change(screen.getByLabelText('Name'), { target: { value: 'Primary oncall' } });
  fireEvent.click(screen.getByRole('button', { name: 'Save' }));
  const link = await screen.findByRole('link', { name: 'View operation progress' });
  expect(link).toHaveAttribute('href', '/operations/operation-1');
  fireEvent.click(link);
  expect(await screen.findByRole('region', { name: 'Operation receipt' })).toHaveTextContent('Controller application has not yet been confirmed');
  expect(state.writes).toHaveLength(1);
});

it('retains a reconciliation link when headers arrive but the mutation response body is interrupted', async () => {
  const state = await fixture('credentials', 'new', { lostResponseBody: true });
  await screen.findByRole('form', { name: 'Create secret' });
  fireEvent.change(screen.getByLabelText('Stable ID'), { target: { value: 'provider-key' } });
  fireEvent.change(screen.getByLabelText('New secret value'), { target: { value: 'never-retain-operation-secret' } });
  fireEvent.click(screen.getByRole('button', { name: 'Save' }));
  expect(await screen.findByRole('alert')).toHaveTextContent('outcome is unconfirmed');
  const link = screen.getByRole('link', { name: 'Reconcile original operation' });
  expect(link).toHaveAttribute('href', '/operations/operation-1');
  expect(screen.getByRole('button', { name: 'Save' })).toBeDisabled();
  expect(screen.getByLabelText('New secret value')).toHaveValue('');
  fireEvent.click(link);
  fireEvent.click(screen.getByRole('button', { name: 'Discard draft and leave' }));
  expect(await screen.findByRole('region', { name: 'Operation receipt' })).toBeInTheDocument();
  expect(document.body.innerHTML).not.toContain('never-retain-operation-secret');
  expect(state.writes).toHaveLength(1);
});

it('uses the deletion receipt when the resource no longer exists', async () => {
  const state = await fixture('recipients', 'remove', { initial: [resource('recipients', 'remove', { endpointRefs: ['ops-mail'] })] });
  fireEvent.click(await screen.findByRole('button', { name: 'Delete recipient' }));
  fireEvent.click(screen.getByRole('button', { name: 'Confirm deletion' }));
  const link = await screen.findByRole('link', { name: 'View operation progress' });
  expect(state.records.has('Recipient:remove')).toBe(false);
  fireEvent.click(link);
  expect(await screen.findByRole('region', { name: 'Operation item results' })).toHaveTextContent('remove');
  expect(state.writes).toHaveLength(1);
  expect(state.writes[0].method).toBe('DELETE');
});

it('clears late operation responses when identity changes', async () => {
  const state = await operationFixture();
  await screen.findByRole('region', { name: 'Operation receipt' });
  let resolve!: (response: Response) => void;
  state.fetcher.mockImplementationOnce(() => new Promise<Response>(done => { resolve = done; }));
  const pending = state.session.get('/api/v2/operations/original-operation');
  await act(async () => { state.session.signOut(); });
  resolve(json(receipt('completed', 'applied')));
  await expect(pending).rejects.toMatchObject({ reason: 'session-changed' });
  expect(screen.queryByRole('region', { name: 'Operation receipt' })).not.toBeInTheDocument();
});


it('recognizes collection validation states and never mistakes a retained verdict or interruption for activation', () => {
  for (const state of ['validating', 'validated', 'rejected', 'interrupted', 'invalidated', 'canceled', 'expired']) {
    const value = operationView({ ...receipt(), state, committed: 0, applied: 0, items: [] });
    expect(value.state).toBe(state); expect(operationApplied(value)).toBe(false);
    expect(operationMessage(value)).not.toContain('cannot interpret');
    expect(operationPollInterval(wrapped(value))).toBe(state === 'validating' ? 5000 : false);
  }
});

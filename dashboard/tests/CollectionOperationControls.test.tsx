import { afterEach, expect, it, vi } from 'vitest';
import { act, cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react';
import { SessionBoundary } from '../src/auth/SessionBoundary';
import { CollectionOperationControls } from '../src/components/CollectionOperationControls';
import { ConfirmDialog } from '../src/components/ConfirmDialog';
import { DashboardSession } from '../src/api/session';
import { collectionIdentityFormat, fileNormalizationProfile } from '../src/api/collectionInventory';
import { operationContracts, type Operation } from '../src/api/generated';

const id = 'original-control-operation';
const identity = { identityFormat: collectionIdentityFormat, normalizationProfile: fileNormalizationProfile, contentDigest: 'a'.repeat(64), itemCount: 2 };
const initial: Operation = { ...identity, id, state: 'uploading', uploaded: 2, committed: 0, applied: 0 };
const steps = ['ValidateOperation', 'GetOperationValidation', 'ActivateOperation', 'CancelOperation', 'GetOperation'];
const json = (value: unknown, status = 200, headers?: HeadersInit) => new Response(JSON.stringify(value), { status, headers });
const sealed = (count = 2, offset = 0) => ({ ...identity, itemCount: count, operationID: id, summary: {
  resultID: '11111111-1111-1111-1111-111111111111', valid: true, summaryOnly: false, count,
  digest: 'b'.repeat(64), capabilitiesDigest: 'c'.repeat(64), finalizedAt: '2026-09-20T00:00:00Z', expiresAt: '2099-10-20T00:00:00Z',
  planID: '22222222-2222-2222-2222-222222222222', planDigest: 'd'.repeat(64),
}, items: Array.from({ length: Math.min(100, count - offset) }, (_, index) => ({ ordinal: offset + index + 1, kind: 'Monitor', id: `monitor-${offset + index + 1}`,
  source: 'source.00000000000000000001', sourceDocument: offset + index + 1, sourceItem: 1, change: 'create', resource: 'PRIVATE-RESOURCE', message: 'PRIVATE-DIAGNOSTIC' })),
  ...(offset + 100 < count ? { nextCursor: 'page100' } : {}), sourcePath: '/PRIVATE/source.yaml', identityKey: 'PRIVATE-KEY' });
type Request = { url: URL; init: RequestInit };
async function fixture(options: { state?: string; count?: number; uploaded?: number; supported?: string[]; permissions?: string[]; reader?: boolean; loss?: 'validate' | 'activate' | 'cancel';
  profile?: string | null; activation?: (operation: Operation) => Response; validation?: (url: URL) => Response | Promise<Response>; progress?: () => Response; afterValidation?: string; busyValidation?: boolean } = {}) {
  const count = options.count ?? 2;
  const expected = { ...identity, itemCount: count, ...(options.profile === null ? { normalizationProfile: undefined } : {}) };
  const operation: Operation = { ...initial, ...expected, state: options.state ?? 'uploading', uploaded: options.uploaded ?? count,
    ...(options.profile === null ? { normalizationProfile: undefined } : options.profile ? { normalizationProfile: options.profile } : {}) };
  const requests: Request[] = [];
  const fetcher = vi.fn<typeof fetch>(async (input, init = {}) => {
    const url = new URL(String(input));
    if (url.pathname.endsWith('/self')) return new Headers(init.headers).has('Authorization')
      ? json({ principalId: 'operator', role: options.reader ? 'reader' : 'operator', permissions: options.permissions ?? Object.keys(operationContracts) }) : json({}, 401);
    if (url.pathname.endsWith('/discovery')) return json({ resourceOperations: { Collection: options.supported ?? steps, Operation: options.supported ?? steps } });
    requests.push({ url, init });
    if (init.method === 'GET' && url.pathname.endsWith('/validation')) {
      if (options.afterValidation) operation.state = options.afterValidation;
      return options.validation?.(url) ?? json(sealed(count, url.searchParams.has('cursor') ? 100 : 0));
    }
    if (init.method === 'GET') return options.progress?.() ?? json(operation);
    if (url.pathname.endsWith('/validate')) {
      if (options.busyValidation) return json({ code: 'admissionBusy', detail: 'PRIVATE' }, 429);
      operation.state = 'validating';
      if (options.loss === 'validate') throw new Error('PRIVATE-WRITE-FAILURE');
      return json(operation, 202, { 'X-Operation-ID': id });
    }
    if (url.pathname.endsWith('/activate')) {
      operation.state = 'applying'; operation.executionResult = { state: 'pending' };
      if (options.loss === 'activate') throw new Error('PRIVATE-WRITE-FAILURE');
      return options.activation?.(operation) ?? json(operation, 200, { 'X-Operation-ID': id });
    }
    if (url.pathname.endsWith('/cancel')) {
      operation.state = 'canceled';
      if (options.loss === 'cancel') throw new Error('PRIVATE-WRITE-FAILURE');
      return json(operation);
    }
    return json({}, 404);
  });
  const session = new DashboardSession({ origin: 'https://cpra.example', fetch: fetcher });
  await session.discover(); await session.signIn('PRIVATE-CONTROL-TOKEN');
  const onProgress = vi.fn();
  const rendered = render(<SessionBoundary session={session}><CollectionOperationControls id={id} identity={expected} operation={{ ...operation }} onProgress={onProgress} /></SessionBoundary>);
  await waitFor(() => expect(fetcher.mock.calls.some(([url]) => String(url).endsWith('/discovery'))).toBe(true));
  return { session, operation, requests, fetcher, onProgress, ...rendered };
}
const writes = (requests: Request[]) => requests.filter(request => request.init.method !== 'GET');
const privateDOM = () => expect(document.body.textContent).not.toContain('PRIVATE-');
async function click(name: string) { const button = await screen.findByRole('button', { name }); await waitFor(() => expect(button).toBeEnabled()); await act(async () => { fireEvent.click(button); }); }
afterEach(() => { cleanup(); vi.restoreAllMocks(); vi.useRealTimers(); });

it('validates a fully uploaded original with one bodyless request and never activates automatically', async () => {
  const state = await fixture();
  expect(state.requests).toHaveLength(0);
  await click('Validate original collection');
  await screen.findByText(/Validation was accepted for the original collection/);
  expect(writes(state.requests)).toHaveLength(1);
  expect(writes(state.requests)[0]).toMatchObject({ init: { method: 'POST', body: undefined } });
  expect(writes(state.requests)[0].url.pathname).toBe(`/api/v2/operations/${id}/validate`);
  expect(new Headers(writes(state.requests)[0].init.headers).has('If-Match')).toBe(false);
  expect(screen.getByRole('button', { name: 'Validate original collection' })).toBeDisabled();
  expect(screen.getByRole('button', { name: 'Review activation' })).toBeDisabled();
  expect(state.requests.some(request => request.url.pathname.endsWith('/validation'))).toBe(false);
  expect(state.onProgress).toHaveBeenCalledTimes(1); privateDOM();
});

it.each([{ uploaded: 1 }, { state: 'validating' }, { state: 'rejected' }, { state: 'applying' }, { state: 'future-state' }])('does not validate ineligible progress %#', async options => {
  const state = await fixture(options);
  expect(screen.getByRole('button', { name: 'Validate original collection' })).toBeDisabled(); expect(writes(state.requests)).toHaveLength(0);
});

it('reviews retained proof and fresh progress before explicit original-id/count activation confirmation', async () => {
  const storage = vi.spyOn(Storage.prototype, 'setItem');
  const state = await fixture({ state: 'validated' });
  await click('Review activation');
  const dialog = await screen.findByRole('alertdialog');
  expect(dialog).toHaveTextContent(id); expect(dialog).toHaveTextContent('Original resource count: 2');
  expect(dialog).toHaveTextContent('11111111-1111-1111-1111-111111111111');
  expect(dialog).toHaveTextContent('partial commits');
  expect(within(dialog).getByRole('button', { name: 'Keep reviewing' })).toHaveFocus();
  expect(screen.getByRole('region', { name: 'Activation review results' })).toHaveTextContent('Monitor/monitor-1');
  expect(screen.getByRole('region', { name: 'Activation review resource results' })).toHaveAttribute('tabindex', '0');
  expect(writes(state.requests)).toHaveLength(0);
  expect(state.requests.map(request => request.url.pathname)).toEqual([`/api/v2/operations/${id}/validation`, `/api/v2/operations/${id}`]);
  await click('Confirm activation');
  await screen.findByText(/Activation was requested for the original collection/);
  expect(writes(state.requests)).toHaveLength(1);
  expect(writes(state.requests)[0].url.pathname).toBe(`/api/v2/operations/${id}/activate`);
  expect(writes(state.requests)[0].init.body).toBeUndefined();
  expect(state.requests.filter(request => request.url.pathname === `/api/v2/operations/${id}`)).toHaveLength(2);
  expect(screen.queryByRole('alertdialog')).not.toBeInTheDocument(); expect(storage).not.toHaveBeenCalled(); privateDOM();
});

it.each(['missing-plan', 'summary-only', 'expired', 'unsupported', 'wrong-identity', 'rejected'])('cannot activate from %s retained validation', async mode => {
  const raw = sealed();
  const value = mode === 'missing-plan' ? { ...raw, summary: { ...raw.summary, planID: undefined, planDigest: undefined } }
    : mode === 'summary-only' ? { ...raw, summary: { ...raw.summary, summaryOnly: true } }
      : mode === 'expired' ? { ...raw, summary: { ...raw.summary, expiresAt: '2026-09-21T00:00:00Z' } }
        : mode === 'unsupported' ? { ...raw, items: raw.items.map(item => ({ ...item, change: 'future-change' })) }
          : mode === 'wrong-identity' ? { ...raw, contentDigest: 'f'.repeat(64) }
            : { ...raw, summary: { ...raw.summary, valid: false, planID: undefined, planDigest: undefined }, items: raw.items.map(item => ({ ...item, change: undefined, issue: 'invalidResource' })) };
  const state = await fixture({ state: 'validated', validation: () => json(value) });
  await click('Review activation');
  await waitFor(() => expect(screen.getByRole('button', { name: 'Review activation' })).toBeEnabled());
  expect(screen.queryByRole('alertdialog')).not.toBeInTheDocument(); expect(writes(state.requests)).toHaveLength(0); privateDOM();
});

it('keeps the sealed result separate from current cancellation and refuses stale confirmation', async () => {
  const state = await fixture({ state: 'validated', afterValidation: 'canceled' });
  await click('Review activation'); await screen.findByText(/not eligible for activation/);
  expect(screen.queryByRole('alertdialog')).not.toBeInTheDocument(); expect(writes(state.requests)).toHaveLength(0);
  cleanup();
  const changed = await fixture({ state: 'validated' });
  await click('Review activation'); await screen.findByRole('alertdialog');
  changed.operation.state = 'applying'; changed.operation.executionResult = { state: 'pending' };
  await click('Confirm activation'); await screen.findByText(/Original progress changed/);
  expect(writes(changed.requests)).toHaveLength(0);
});

it('pages through one pinned review result and disables confirmation on unknown later vocabulary', async () => {
  const state = await fixture({ state: 'validated', count: 101, validation: url => {
    const raw = sealed(101, url.searchParams.has('cursor') ? 100 : 0);
    return json(url.searchParams.has('cursor') ? { ...raw, items: raw.items.map(item => ({ ...item, change: 'future-change' })) } : raw);
  } });
  await click('Review activation'); await screen.findByRole('alertdialog');
  await click('Keep reviewing'); await click('Next activation review page');
  await screen.findByText(/unsupported vocabulary/);
  const review = screen.getByRole('region', { name: 'Activation review results' });
  expect(review).toHaveTextContent('Monitor/monitor-101'); expect(review).not.toHaveTextContent('Monitor/monitor-100');
  expect(screen.queryByRole('alertdialog')).not.toBeInTheDocument(); expect(writes(state.requests)).toHaveLength(0); privateDOM();
});

it('keeps ambiguous activation locked until explicit original progress read without a second POST', async () => {
  const state = await fixture({ state: 'validated', loss: 'activate' });
  await click('Review activation'); await screen.findByRole('alertdialog'); await click('Confirm activation');
  await screen.findByRole('alert');
  expect(screen.getByRole('button', { name: 'Cancel inactive operation' })).toBeDisabled();
  expect(screen.getByRole('button', { name: 'Review activation' })).toBeDisabled();
  expect(writes(state.requests)).toHaveLength(1);
  await click('Read original progress'); await screen.findByText(/Original progress was read/);
  expect(screen.getByRole('button', { name: 'Review activation' })).toBeDisabled();
  expect(screen.getByRole('button', { name: 'Cancel inactive operation' })).toBeDisabled();
  expect(writes(state.requests)).toHaveLength(1); privateDOM();
});

it.each(['validated', 'future-state'])('keeps a stale same-ID 200 %s activation response uncertain until an explicit read', async staleState => {
  const state = await fixture({ state: 'validated', activation: operation => json({ ...operation, state: staleState, executionResult: undefined }, 200, { 'X-Operation-ID': id }) });
  await click('Review activation'); await screen.findByRole('alertdialog'); await click('Confirm activation');
  await screen.findByText(/The original operation outcome is unconfirmed/);
  expect(screen.queryByText(/Activation was requested/)).not.toBeInTheDocument();
  expect(screen.getByRole('button', { name: 'Review activation' })).toBeDisabled();
  expect(screen.getByRole('button', { name: 'Cancel inactive operation' })).toBeDisabled();
  expect(writes(state.requests)).toHaveLength(1);
  // A background receipt refresh cannot clear this mutation's uncertainty.
  state.rerender(<SessionBoundary session={state.session}><CollectionOperationControls id={id} identity={identity} operation={{ ...initial, state: 'validated' }} onProgress={state.onProgress} /></SessionBoundary>);
  expect(screen.getByRole('button', { name: 'Review activation' })).toBeDisabled();
  expect(screen.getByRole('button', { name: 'Cancel inactive operation' })).toBeDisabled();
  await click('Read original progress'); await screen.findByText(/Original progress was read/);
  expect(screen.getByRole('button', { name: 'Review activation' })).toBeDisabled();
  expect(screen.getByRole('button', { name: 'Cancel inactive operation' })).toBeDisabled();
  expect(writes(state.requests)).toHaveLength(1);
  expect(writes(state.requests)[0].url.pathname).toBe(`/api/v2/operations/${id}/activate`);
  privateDOM();
});

it('permits validation retry only after an explicit original read and another explicit click', async () => {
  const state = await fixture({ loss: 'validate' });
  await click('Validate original collection'); await screen.findByRole('alert');
  state.operation.state = 'uploading';
  expect(screen.getByRole('button', { name: 'Validate original collection' })).toBeDisabled();
  await click('Read original progress'); await screen.findByText(/Original progress was read/);
  expect(screen.getByRole('button', { name: 'Validate original collection' })).toBeEnabled(); expect(writes(state.requests)).toHaveLength(1);
  await click('Validate original collection'); await screen.findByRole('alert'); expect(writes(state.requests)).toHaveLength(2);
});

it('requires cancellation confirmation and rechecks that the original operation is still inactive', async () => {
  const state = await fixture();
  await click('Cancel inactive operation'); const dialog = await screen.findByRole('alertdialog');
  expect(dialog).toHaveTextContent(id); expect(dialog).toHaveTextContent('does not undo'); expect(writes(state.requests)).toHaveLength(0);
  await click('Confirm cancellation'); await screen.findByText(/Cancellation was requested/);
  expect(writes(state.requests)).toHaveLength(1); expect(writes(state.requests)[0].url.pathname).toBe(`/api/v2/operations/${id}/cancel`);
  cleanup();
  const changed = await fixture();
  await click('Cancel inactive operation'); await screen.findByRole('alertdialog'); changed.operation.state = 'applying';
  await click('Confirm cancellation'); await screen.findByText(/Original progress changed/); expect(writes(changed.requests)).toHaveLength(0);
});

it('enforces both discovery and identity permission and leaves readers/unknown profiles read only', async () => {
  const noDiscovery = await fixture({ supported: ['GetOperation'] });
  expect(screen.getByRole('button', { name: 'Validate original collection' })).toBeDisabled(); expect(writes(noDiscovery.requests)).toHaveLength(0);
  cleanup();
  const noPermission = await fixture({ permissions: ['GetCapabilities', 'GetOperation'] });
  expect(screen.getByRole('button', { name: 'Validate original collection' })).toBeDisabled(); expect(writes(noPermission.requests)).toHaveLength(0);
  cleanup();
  const reader = await fixture({ reader: true });
  expect(screen.queryByRole('button', { name: 'Validate original collection' })).not.toBeInTheDocument(); expect(writes(reader.requests)).toHaveLength(0);
  cleanup();
  const unknown = await fixture({ profile: 'cpra.file.future.v2' });
  expect(screen.getByText(/identity or normalization profile is unsupported/)).toBeInTheDocument(); expect(unknown.requests).toHaveLength(0);
  cleanup();
  const unprofiled = await fixture({ profile: null });
  expect(screen.getByText(/Unprofiled collections can be read or canceled while inactive/)).toBeInTheDocument();
  expect(screen.getByRole('button', { name: 'Validate original collection' })).toBeDisabled(); expect(screen.getByRole('button', { name: 'Review activation' })).toBeDisabled();
  await click('Cancel inactive operation'); await screen.findByRole('alertdialog'); await click('Confirm cancellation'); await screen.findByText(/Cancellation was requested/);
  expect(writes(unprofiled.requests)).toHaveLength(1);
});

it('allows a confirmed busy validation rejection to retry only after read and explicit request', async () => {
  const state = await fixture({ busyValidation: true });
  await click('Validate original collection'); await screen.findByRole('alert');
  expect(screen.getByRole('button', { name: 'Validate original collection' })).toBeDisabled(); expect(writes(state.requests)).toHaveLength(1);
  await click('Read original progress'); await screen.findByText(/Original progress was read/);
  expect(screen.getByRole('button', { name: 'Validate original collection' })).toBeEnabled(); expect(writes(state.requests)).toHaveLength(1);
  await click('Validate original collection'); await screen.findByRole('alert'); expect(writes(state.requests)).toHaveLength(2); privateDOM();
});

it('clears confirmation on session reset without submitting a control', async () => {
  const state = await fixture({ state: 'validated' });
  await click('Review activation'); await screen.findByRole('alertdialog');
  act(() => state.session.signOut());
  await screen.findByRole('heading', { name: 'Sign in to CPRa' });
  expect(screen.queryByRole('alertdialog')).not.toBeInTheDocument(); expect(writes(state.requests)).toHaveLength(0); privateDOM();
});

it('ignores an old retained read after operation identity changes', async () => {
  let finish!: (response: Response) => void;
  const state = await fixture({ state: 'validated', validation: () => new Promise(resolve => { finish = resolve; }) });
  await click('Review activation');
  await waitFor(() => expect(state.requests.some(request => request.url.pathname.endsWith('/validation'))).toBe(true));
  const changed = { ...identity, contentDigest: 'e'.repeat(64) };
  const other = { ...initial, ...changed, id: 'other-operation' };
  state.rerender(<SessionBoundary session={state.session}><CollectionOperationControls id={other.id} identity={changed} operation={other} onProgress={state.onProgress} /></SessionBoundary>);
  await act(async () => { finish(json(sealed())); });
  expect(screen.queryByRole('alertdialog')).not.toBeInTheDocument(); expect(screen.queryByRole('region', { name: 'Activation review results' })).not.toBeInTheDocument();
  expect(screen.getByRole('region', { name: 'Original collection controls' })).toHaveTextContent('other-operation');
  expect(state.onProgress).not.toHaveBeenCalled(); expect(writes(state.requests)).toHaveLength(0);
});

it('rechecks retained expiry when confirmation is delayed', async () => {
  const state = await fixture({ state: 'validated' });
  await click('Review activation'); await screen.findByRole('alertdialog');
  vi.useFakeTimers({ toFake: ['Date'] }); vi.setSystemTime(new Date('2100-01-01T00:00:00Z'));
  await click('Confirm activation'); await screen.findByText(/Original progress changed or the review expired/);
  expect(writes(state.requests)).toHaveLength(0);
});

it('preserves deletion labels for existing ConfirmDialog consumers', () => {
  render(<ConfirmDialog title="Delete resource?" pending={false} onCancel={vi.fn()} onConfirm={vi.fn()}>Confirm resource.</ConfirmDialog>);
  expect(screen.getByRole('button', { name: 'Keep resource' })).toBeInTheDocument(); expect(screen.getByRole('button', { name: 'Confirm deletion' })).toBeInTheDocument();
});

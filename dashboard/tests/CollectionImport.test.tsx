import { afterEach, expect, it, vi } from 'vitest';
// Node TextEncoder supplies host buffers; align this DOM fixture's typed-array
// constructors with it. Production Worker transfers use the receiving realm.
vi.hoisted(() => { const encoded = new TextEncoder().encode(''); vi.stubGlobal('Uint8Array', encoded.constructor); vi.stubGlobal('ArrayBuffer', encoded.buffer.constructor); });
import { act, cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react';
import { createMemoryRouter, RouterProvider } from 'react-router-dom';
import CollectionImport from '../src/pages/CollectionImport';
import { SessionBoundary } from '../src/auth/SessionBoundary';
import { DashboardSession } from '../src/api/session';
import { BrowserCollection, CollectionImportError, PrivateCollectionBody } from '../src/import/collection';
import { collectionIdentityFormat, collectionSourceToken, type WorkerCollectionSummary } from '../src/api/collectionInventory';
import { operationContracts, type Operation } from '../src/api/generated';

const encoder = new TextEncoder(), decoder = new TextDecoder();
const response = (value: unknown, status = 200, headers: HeadersInit = {}) => new Response(JSON.stringify(value), { status, headers });
const makeBody = (value: unknown, next?: number) => new PrivateCollectionBody(encoder.encode(JSON.stringify(value)).buffer, next);
const identity = { identityFormat: collectionIdentityFormat, contentDigest: 'a'.repeat(64), itemCount: 1 };
const summary: WorkerCollectionSummary = { ...identity, sourceCount: 1, sourceBytes: 64, normalizedBytes: 64, preflightAvailable: true };
const steps = ['PrepareCollection', 'CreateOperation', 'UploadOperation', 'ValidateOperation', 'GetOperationValidation', 'ActivateOperation', 'CancelOperation', 'GetOperation', 'PreflightCollection'];

function frozen(count = 1) {
  const all = Array.from({ length: count }, (_, index) => ({ ordinal: index + 1, id: `Monitor/monitor-${index + 1}`, source: { token: collectionSourceToken(1), document: index + 1, item: 1 }, bytes: 64 }));
  const value = { summary: { ...summary, itemCount: count }, close: vi.fn(), sourceName: vi.fn(() => 'service.yaml'),
    page: vi.fn(async (offset = 0) => all.slice(offset, offset + 100)),
    createBody: vi.fn(async () => makeBody({ ...identity, itemCount: count, identityKey: 'private-key', sourceFingerprint: 'private-fingerprint' })),
    preflightBody: vi.fn(async () => makeBody({ ...identity, itemCount: count, identityKey: 'private-key', sourceFingerprint: 'private-fingerprint', items: [] })),
    uploadBody: vi.fn(async (offset: number) => makeBody({ items: all.slice(offset, offset + 256) }, Math.min(count, offset + 256))),
  };
  vi.spyOn(BrowserCollection, 'prepare').mockResolvedValue(value as unknown as BrowserCollection);
  return value;
}
async function fixture(options: { supported?: string[]; reader?: boolean; lostCreate?: boolean; lostUpload?: boolean; count?: number; invalidValidation?: boolean; unknownState?: boolean; lostValidation?: boolean; validationPending?: number; validationError?: { status: number; code: string }; validationHeaders?: HeadersInit; validationExpiresAt?: string; progressError?: boolean; afterValidationState?: string; lostActivation?: boolean; activationHeader?: string; operationRead?: (url: URL, operation: Operation) => unknown } = {}) {
  const supported = options.supported ?? steps, count = options.count ?? 1;
  const writes: { path: string; method: string; body?: Record<string, unknown>; match: string | null }[] = [];
  const operation: Operation = { ...identity, itemCount: count, id: 'original-1', state: 'uploading', uploaded: 0, committed: 0, applied: 0 };
  let createLost = options.lostCreate, uploadLost = options.lostUpload;
  let validationReads = 0;
  const fetcher = vi.fn<typeof fetch>(async (input, init = {}) => {
    const url = new URL(String(input)), method = init.method ?? 'GET', headers = new Headers(init.headers);
    if (url.pathname.endsWith('/self')) return headers.has('Authorization') ? response({ principalId: 'alice', role: options.reader ? 'reader' : 'operator', permissions: Object.keys(operationContracts) }) : response({}, 401);
    if (url.pathname.endsWith('/discovery')) return response({ resourceOperations: { Collection: supported, Operation: supported } });
    if (method === 'GET' && url.pathname.endsWith('/validation')) {
      validationReads++;
      const headers = { 'X-Operation-ID': operation.id, 'Retry-After': '12', ...options.validationHeaders };
      if (options.validationError) return response({ code: options.validationError.code }, options.validationError.status, headers);
      if (validationReads <= (options.validationPending ?? 0)) return response({ code: 'validationPending' }, 409, headers);
      operation.state = options.afterValidationState ?? (options.invalidValidation ? 'rejected' : 'validated');
      const offset = Number(url.searchParams.get('cursor')?.slice(1) ?? 0);
      return response({ ...identity, itemCount: count, operationID: operation.id, summary: { resultID: '11111111-1111-1111-1111-111111111111', valid: !options.invalidValidation, summaryOnly: false, count, digest: 'b'.repeat(64), capabilitiesDigest: 'c'.repeat(64), finalizedAt: '2026-09-20T00:00:00Z', expiresAt: options.validationExpiresAt ?? '2026-10-20T00:00:00Z', ...(options.invalidValidation ? { issue: 'invalidResource' } : { planID: '22222222-2222-2222-2222-222222222222', planDigest: 'd'.repeat(64) }) },
        items: Array.from({ length: Math.min(100, count - offset) }, (_, index) => ({ ordinal: offset + index + 1, kind: 'Monitor', id: `monitor-${offset + index + 1}`, source: collectionSourceToken(1), sourceDocument: offset + index + 1, sourceItem: 1, ...(options.invalidValidation ? { issue: index === 0 ? 'invalidResource' : 'notEvaluated' } : { change: 'create' }), message: 'private-server-message', resource: 'private-resource-value' })), ...(offset + 100 < count ? { nextCursor: `v${offset + 100}` } : {}) });
    }
    if (method === 'GET' && options.progressError && validationReads > 0) return response({ code: 'storageUnavailable' }, 503);
    if (method === 'GET') return response(options.operationRead?.(url, operation) ?? (options.unknownState ? { ...operation, state: 'future' } : operation));
    const body = init.body ? JSON.parse(decoder.decode(init.body as Uint8Array)) as Record<string, unknown> : undefined;
    writes.push({ path: url.pathname, method, body, match: headers.get('If-Match') });
    if (url.pathname.endsWith('/prepare')) return response({ ticket: 'original-private-ticket', expiresAt: '2099-01-01T00:00:00Z' });
    if (url.pathname === '/api/v2/operations') {
      if (createLost) { createLost = false; throw new Error('response lost'); }
      return response(operation);
    }
    if (url.pathname.endsWith('/items')) {
      operation.uploaded = operation.uploaded! + (body?.items as unknown[]).length;
      if (uploadLost) { uploadLost = false; throw new Error('response lost'); }
      return response(operation);
    }
    if (url.pathname.endsWith('/validate')) { operation.state = 'validating'; if (options.lostValidation) throw new Error('lost private response'); return response(operation, 202, { 'Retry-After': '5', 'X-Operation-ID': operation.id }); }
    if (url.pathname.endsWith('/preflight')) return response({ ...identity, itemCount: count, valid: !options.invalidValidation, items: [{ id: 'Monitor/monitor-1', outcome: options.invalidValidation ? 'invalid' : 'create', message: 'private-server-message', oldVersion: 'rv-1', committed: false, applied: false }] });
    if (url.pathname.endsWith('/activate')) {
      operation.state = 'applying'; operation.executionResult = { state: 'pending' };
      if (options.lostActivation) throw new Error('private activation response lost');
      return response(operation, 200, { 'X-Operation-ID': options.activationHeader ?? operation.id, 'Retry-After': '5' });
    }
    if (url.pathname.endsWith('/cancel')) { operation.state = 'canceled'; return response(operation); }
    return response({}, 404);
  });
  const session = new DashboardSession({ origin: 'https://cpra.example', fetch: fetcher });
  await session.discover(); await session.signIn('private-bearer');
  const router = createMemoryRouter([{ path: '/import', element: <SessionBoundary session={session}><CollectionImport /></SessionBoundary> }, { path: '/elsewhere', element: <p>Elsewhere</p> }], { initialEntries: ['/import'] });
  render(<RouterProvider router={router} />);
  await waitFor(() => expect(fetcher.mock.calls.some(call => String(call[0]).endsWith('/discovery'))).toBe(true));
  return { session, writes, fetcher, router, operation };
}
async function select() {
  await act(async () => { fireEvent.change(screen.getByLabelText('Configuration files'), { target: { files: [new File(['private-file'], 'service.yaml')] } }); });
  await screen.findByRole('heading', { name: 'Source-attributed preview' });
}
async function click(name: string) { const button = await screen.findByRole('button', { name }); await waitFor(() => expect(button).toBeEnabled()); await act(async () => { fireEvent.click(button); }); }
async function reviewCollection() {
  await select(); await click('Create inactive operation'); await screen.findByText(/Inactive operation created/);
  await click('Upload remaining resources'); await screen.findByText(/All resources are staged/);
  await click('Validate whole collection'); await screen.findByText(/Validation was accepted/);
  await click('Read original validation result'); await screen.findByText(/The original validation passed/);
}
afterEach(() => { cleanup(); vi.restoreAllMocks(); });

it('offers real preflight on current capabilities and honestly disables durable writes; keeps values and keys out of DOM/storage', async () => {
  frozen(); const storage = vi.spyOn(Storage.prototype, 'setItem');
  const { writes } = await fixture({ supported: ['PreflightCollection', 'GetOperation'] });
  await select(); expect(screen.getByText(/does not offer the complete staging and validation flow/)).toBeInTheDocument();
  expect(screen.getByRole('button', { name: 'Create inactive operation' })).toBeDisabled();
  await click('Preview on server'); await screen.findByText(/Server preview passed/);
  expect(writes.map(value => value.path)).toEqual(['/api/v2/collections/preflight']);
  expect(screen.getByRole('table')).toHaveTextContent('service.yaml'); expect(screen.getByRole('table')).toHaveTextContent('rv-1');
  for (const secret of ['private-key', 'private-file', 'private-bearer', 'private-server-message']) expect(document.body.textContent).not.toContain(secret);
  expect(storage).not.toHaveBeenCalled();
});

it('does not parse files or mutate for reader identities even if a server incorrectly grants write permission names', async () => {
  const collection = frozen(); const { writes } = await fixture({ reader: true });
  expect(screen.getByLabelText('Configuration files')).toBeDisabled(); expect(screen.getByText(/requires an operator/)).toBeInTheDocument();
  expect(collection.page).not.toHaveBeenCalled(); expect(writes).toHaveLength(0);
});

it('freezes before any mutation; malformed final files show local attribution and cause zero writes', async () => {
  vi.spyOn(BrowserCollection, 'prepare').mockRejectedValue(new CollectionImportError('invalid', { source: 2, document: 7, item: 3 }));
  const { writes } = await fixture();
  fireEvent.change(screen.getByLabelText('Configuration files'), { target: { files: [new File(['valid'], 'a.yaml'), new File(['invalid-private'], 'z.yaml')] } });
  expect(await screen.findByText(/Source: z.yaml; document 7, item 3/)).toBeInTheDocument(); expect(writes).toHaveLength(0);
  expect(document.body.textContent).not.toContain('invalid-private');
});

it('separates explicit create/upload/validate/activation, chunks 300 resources, and prevents double submission', async () => {
  const collection = frozen(300); const { writes } = await fixture({ count: 300 });
  await select();
  const create = screen.getByRole('button', { name: 'Create inactive operation' });
  fireEvent.click(create); fireEvent.click(create);
  await screen.findByText(/Inactive operation created/); expect(writes.map(write => write.path)).toEqual(['/api/v2/collections/prepare', '/api/v2/operations']);
  expect(writes[1].body?.admissionTicket).toBe('original-private-ticket');
  expect(screen.getByRole('button', { name: 'Review activation' })).toBeDisabled();
  await click('Upload remaining resources'); await screen.findByText(/All resources are staged/);
  expect(collection.uploadBody.mock.calls.map(call => call[0])).toEqual([0, 256]);
  expect(writes.filter(write => write.method === 'PUT').map(write => [(write.body?.items as unknown[]).length, write.match])).toEqual([[256, null], [44, null]]);
  await click('Validate whole collection'); await screen.findByText(/Validation was accepted/);
  await click('Read original validation result'); await screen.findByText(/The original validation passed/);
  await click('Read original progress');
  expect(writes.some(write => write.path.endsWith('/activate'))).toBe(false);
  await click('Review activation'); expect(screen.getByRole('alertdialog')).toBeInTheDocument();
  expect(screen.getByRole('alertdialog')).toHaveTextContent('300 resources');
  expect(screen.getByRole('alertdialog')).toHaveTextContent('original-1');
  expect(screen.getByRole('alertdialog')).toHaveTextContent('11111111-1111-1111-1111-111111111111');
  const activate = screen.getByRole('button', { name: 'Confirm activation' });
  await act(async () => { fireEvent.click(activate); fireEvent.click(activate); });
  await screen.findByText(/Activation was requested/);
  expect(writes.filter(write => write.path.endsWith('/activate'))).toHaveLength(1);
  expect(writes.find(write => write.path.endsWith('/activate'))).toMatchObject({ body: undefined, method: 'POST', match: null });
  await click('Stop waiting'); expect(writes.some(write => write.path.endsWith('/cancel'))).toBe(false);
  expect(screen.getByRole('button', { name: 'Wait for progress' })).toBeEnabled();
});

it('reuses the original ticket and frozen identity after uncertain create without automatic re-prepare or retry', async () => {
  frozen(); const { writes } = await fixture({ lostCreate: true }); await select();
  await click('Create inactive operation'); await screen.findByText(/Outcome unconfirmed/);
  expect(writes).toHaveLength(2); await click('Retry original create'); await screen.findByText(/Inactive operation created/);
  expect(writes.map(write => write.path)).toEqual(['/api/v2/collections/prepare', '/api/v2/operations', '/api/v2/operations']);
  expect(writes[2].body).toEqual(writes[1].body);
});

it('lost upload cannot trigger another chunk or activation; explicit original progress read reconciles without retransmission', async () => {
  frozen(300); const { writes } = await fixture({ count: 300, lostUpload: true }); await select();
  await click('Create inactive operation'); await screen.findByText(/Inactive operation created/);
  await click('Upload remaining resources'); await screen.findByText(/Outcome unconfirmed/);
  expect(writes.filter(write => write.method === 'PUT')).toHaveLength(1); expect(screen.getByRole('button', { name: 'Upload remaining resources' })).toBeDisabled();
  await click('Read original progress'); await waitFor(() => expect(screen.getByRole('button', { name: 'Upload remaining resources' })).toBeEnabled());
  await click('Upload remaining resources'); await screen.findByText(/All resources are staged/);
  expect(writes.filter(write => write.method === 'PUT').map(write => (write.body?.items as unknown[]).length)).toEqual([256, 44]);
});

it('invalid validation and unknown future states never allow activation', async () => {
  frozen(); const { writes } = await fixture({ invalidValidation: true, unknownState: true }); await select();
  await click('Create inactive operation'); await screen.findByText(/Inactive operation created/);
  await click('Upload remaining resources'); await screen.findByText(/All resources are staged/);
  await click('Validate whole collection'); await screen.findByText(/Validation was accepted/);
  await click('Read original validation result'); await screen.findByText(/The original validation was rejected/);
  expect(screen.getByRole('button', { name: 'Review activation' })).toBeDisabled();
  await click('Read original progress'); await screen.findByText(/Unsupported or unconfirmed state/);
  expect(screen.getByRole('button', { name: 'Cancel original operation' })).toBeDisabled(); expect(writes.some(write => write.path.endsWith('/activate'))).toBe(false);
});

it('cancellation requires its own confirmation and closes private worker only after terminal receipt', async () => {
  const collection = frozen(); const { writes } = await fixture(); await select(); await click('Create inactive operation'); await screen.findByText(/Inactive operation created/);
  await click('Cancel original operation'); expect(writes.some(write => write.path.endsWith('/cancel'))).toBe(false);
  await click('Confirm cancellation'); await screen.findByText(/Cancellation was requested/);
  expect(writes.filter(write => write.path.endsWith('/cancel'))).toHaveLength(1); expect(collection.close).toHaveBeenCalled();
});

it('keeps previews bounded and warns on leaving before releasing the worker and private ticket', async () => {
  const collection = frozen(101); const { router } = await fixture({ count: 101 }); await select();
  expect(within(screen.getByRole('table')).getAllByRole('row')).toHaveLength(101);
  await click('Next resources'); await waitFor(() => expect(within(screen.getByRole('table')).getAllByRole('row')).toHaveLength(2));
  await act(async () => { await router.navigate('/elsewhere'); });
  expect(screen.getByRole('alertdialog')).toHaveTextContent('Leaving does not cancel');
  await act(async () => { fireEvent.click(screen.getByRole('button', { name: 'Discard draft and leave' })); }); await screen.findByText('Elsewhere'); expect(collection.close).toHaveBeenCalled();
});

it('sign-out closes a prepared worker and drops local preview without mutation', async () => {
  const collection = frozen(); const { session, writes } = await fixture(); await select();
  await act(async () => session.signOut()); expect(collection.close).toHaveBeenCalled(); expect(writes).toHaveLength(0);
  expect(document.body.textContent).not.toContain('service.yaml');
});

it('an ephemeral preview cannot replace durable whole-collection validation before activation', async () => {
  frozen(); await fixture(); await select(); await click('Create inactive operation'); await screen.findByText(/Inactive operation created/);
  await click('Upload remaining resources'); await screen.findByText(/All resources are staged/);
  await click('Preview on server'); await screen.findByText(/Server preview passed/);
  expect(screen.getByRole('button', { name: 'Review activation' })).toBeDisabled();
  await click('Validate whole collection'); await screen.findByText(/Validation was accepted/);
  await click('Read original validation result'); await screen.findByText(/The original validation passed/);
  await click('Read original progress');
  await click('Review activation');
  expect(screen.getByRole('button', { name: 'Keep reviewing' })).toHaveFocus();
  fireEvent.keyDown(screen.getByRole('alertdialog'), { key: 'Escape' }); expect(screen.queryByRole('alertdialog')).not.toBeInTheDocument();
});

it('sign-out aborts a still-running parser and closes even a late worker result without API mutation', async () => {
  let finish!: (value: BrowserCollection) => void;
  let signal: AbortSignal | undefined;
  const late = { close: vi.fn() };
  vi.spyOn(BrowserCollection, 'prepare').mockImplementation((_files, options) => { signal = options?.signal; return new Promise(resolve => { finish = resolve; }); });
  const { session, writes } = await fixture();
  fireEvent.change(screen.getByLabelText('Configuration files'), { target: { files: [new File(['private'], 'service.yaml')] } });
  await screen.findByRole('button', { name: 'Cancel preparation' });
  await act(async () => session.signOut()); expect(signal?.aborted).toBe(true);
  await act(async () => finish(late as unknown as BrowserCollection));
  expect(late.close).toHaveBeenCalled(); expect(writes).toHaveLength(0); expect(document.body.textContent).not.toContain('service.yaml');
});

it('stops waiting without cancelling and honors the server polling delay', async () => {
  frozen(); const { fetcher, writes, operation } = await fixture(); await select();
  await click('Create inactive operation'); await screen.findByText(/Inactive operation created/);
  operation.retryAfterSeconds = 12;
  const reads = () => fetcher.mock.calls.filter(call => String(call[0]).includes('/operations/original-1?')).length;
  vi.useFakeTimers();
  try {
    fireEvent.click(screen.getByRole('button', { name: 'Wait for progress' }));
    await act(async () => { await vi.advanceTimersByTimeAsync(4999); }); expect(reads()).toBe(0);
    await act(async () => { await vi.advanceTimersByTimeAsync(1); }); expect(reads()).toBe(1);
    await act(async () => { await vi.advanceTimersByTimeAsync(11999); }); expect(reads()).toBe(1);
    await act(async () => { await vi.advanceTimersByTimeAsync(1); }); expect(reads()).toBe(2);
    fireEvent.click(screen.getByRole('button', { name: 'Stop waiting' }));
    await act(async () => { await vi.advanceTimersByTimeAsync(30000); }); expect(reads()).toBe(2); expect(writes.some(write => write.path.endsWith('/cancel'))).toBe(false);
  } finally { vi.useRealTimers(); }
});


it('supports staging and async validation without advertising activation, rendering one original source-attributed page at a time', async () => {
  frozen(201); const storage = vi.spyOn(Storage.prototype, 'setItem');
  const { writes, fetcher } = await fixture({ count: 201, supported: steps.filter(step => step !== 'ActivateOperation') }); await select();
  await click('Create inactive operation'); await screen.findByText(/Inactive operation created/);
  await click('Upload remaining resources'); await screen.findByText(/All resources are staged/);
  await click('Validate whole collection'); await screen.findByText(/Validation was accepted/);
  expect(screen.queryByRole('heading', { name: 'Original sealed validation result' })).not.toBeInTheDocument();
  expect(screen.getByRole('button', { name: 'Validate whole collection' })).toBeDisabled();
  await click('Read original validation result'); await screen.findByRole('heading', { name: 'Original sealed validation result' });
  const results = screen.getByRole('region', { name: 'Original validation result' });
  expect(within(results).getByRole('table')).toHaveTextContent('service.yaml');
  expect(within(results).getAllByRole('row')).toHaveLength(101);
  expect(within(results).getByText('Monitor/monitor-1')).toBeInTheDocument();
  await click('Next validation page'); await within(results).findByText('Monitor/monitor-101');
  expect(within(results).queryByText('Monitor/monitor-1')).not.toBeInTheDocument();
  await click('Next validation page'); await within(results).findByText('Monitor/monitor-201');
  expect(within(results).getAllByRole('row')).toHaveLength(2);
  expect(screen.queryByRole('button', { name: 'Next validation page' })).not.toBeInTheDocument();
  expect(screen.getByRole('button', { name: 'Review activation' })).toBeDisabled();
  expect(writes.filter(write => write.path.endsWith('/validate'))).toHaveLength(1);
  expect(writes.some(write => write.path.endsWith('/activate'))).toBe(false);
  const urls = fetcher.mock.calls.map(call => String(call[0])).join(' ');
  for (const secret of ['private-key', 'private-file', 'private-bearer', 'private-server-message', 'private-resource-value', 'service.yaml']) expect(urls).not.toContain(secret);
  for (const secret of ['private-key', 'private-file', 'private-bearer', 'private-server-message', 'private-resource-value']) expect(document.body.textContent).not.toContain(secret);
  expect(storage).not.toHaveBeenCalled();
});

it('polls only original validation GET after202, honors confirmed pending Retry-After, and stops at sealed result', async () => {
  frozen(); const { fetcher, writes } = await fixture({ validationPending: 1 }); await select();
  await click('Create inactive operation'); await screen.findByText(/Inactive operation created/);
  await click('Upload remaining resources'); await screen.findByText(/All resources are staged/);
  vi.useFakeTimers();
  try {
    await act(async () => fireEvent.click(screen.getByRole('button', { name: 'Validate whole collection' })));
    const reads = () => fetcher.mock.calls.filter(call => String(call[0]).includes('/validation?')).length;
    await act(async () => { await vi.advanceTimersByTimeAsync(4999); }); expect(reads()).toBe(0);
    await act(async () => { await vi.advanceTimersByTimeAsync(1); }); expect(reads()).toBe(1);
    expect(screen.getByText('Pending — no verdict yet')).toBeInTheDocument();
    expect(screen.queryByRole('heading', { name: 'Original sealed validation result' })).not.toBeInTheDocument();
    await act(async () => { await vi.advanceTimersByTimeAsync(11999); }); expect(reads()).toBe(1);
    await act(async () => { await vi.advanceTimersByTimeAsync(1); }); expect(reads()).toBe(2);
    expect(screen.getByRole('heading', { name: 'Original sealed validation result' })).toBeInTheDocument();
    await act(async () => { await vi.advanceTimersByTimeAsync(30000); }); expect(reads()).toBe(2);
    expect(writes.filter(write => write.path.endsWith('/validate'))).toHaveLength(1);
    expect(writes.some(write => /\/(activate|cancel)$/.test(write.path))).toBe(false);
  } finally { vi.useRealTimers(); }
});

it('lost Validate reply is uncertain and cannot cause a second POST; original result read reconciles', async () => {
  frozen(); const { writes } = await fixture({ lostValidation: true }); await select();
  await click('Create inactive operation'); await screen.findByText(/Inactive operation created/);
  await click('Upload remaining resources'); await screen.findByText(/All resources are staged/);
  await click('Validate whole collection'); await screen.findByText(/Outcome unconfirmed/);
  expect(screen.getByRole('button', { name: 'Validate whole collection' })).toBeDisabled();
  await click('Read original progress'); expect(screen.getByRole('button', { name: 'Validate whole collection' })).toBeDisabled();
  await click('Read original validation result'); await screen.findByText(/The original validation passed/);
  expect(writes.filter(write => write.path.endsWith('/validate'))).toHaveLength(1);
  expect(writes.some(write => write.path.endsWith('/activate'))).toBe(false);
});

it.each([
  [409, 'validationInterrupted', /original validation was interrupted/],
  [409, 'validationCanceled', /canceled before a verdict/],
  [409, 'validationNotRequested', /No validation request has been committed/],
  [410, 'operationExpired', /validation evidence has expired/],
  [503, 'historyUnavailable', /validation history is unavailable/],
] as const)('keeps %s %s distinct from a sealed rejection and never revalidates', async (status, code, text) => {
  frozen(); const { writes } = await fixture({ validationError: { status, code } }); await select();
  await click('Create inactive operation'); await screen.findByText(/Inactive operation created/);
  await click('Upload remaining resources'); await screen.findByText(/All resources are staged/);
  await click('Validate whole collection'); await screen.findByText(/Validation was accepted/);
  await click('Read original validation result'); expect(screen.getAllByText(text).length).toBeGreaterThan(0);
  expect(screen.queryByRole('heading', { name: 'Original sealed validation result' })).not.toBeInTheDocument();
  expect(screen.getByRole('button', { name: 'Validate whole collection' })).toBeDisabled();
  expect(screen.getByRole('button', { name: 'Review activation' })).toBeDisabled();
  expect(writes.filter(write => write.path.endsWith('/validate'))).toHaveLength(1);
});

it('stopping an async validation wait or signing out performs no cancel and releases private files', async () => {
  const collection = frozen(); const { writes, session, fetcher } = await fixture({ validationPending: 10 }); await select();
  await click('Create inactive operation'); await screen.findByText(/Inactive operation created/);
  await click('Upload remaining resources'); await screen.findByText(/All resources are staged/);
  await click('Validate whole collection'); await screen.findByText(/Validation was accepted/);
  await click('Stop waiting');
  expect(writes.filter(write => /\/(validate|cancel|activate)$/.test(write.path)).map(write => write.path)).toEqual(['/api/v2/operations/original-1/validate']);
  expect(fetcher.mock.calls.filter(call => String(call[0]).includes('/validation?'))).toHaveLength(0);
  await act(async () => session.signOut()); expect(collection.close).toHaveBeenCalled();
  expect(document.body.textContent).not.toContain('service.yaml');
});


it('reads authoritative phase after a sealed verdict without fabricating success if that progress read fails', async () => {
  frozen(); const { writes } = await fixture({ progressError: true }); await select();
  await click('Create inactive operation'); await screen.findByText(/Inactive operation created/);
  await click('Upload remaining resources'); await screen.findByText(/All resources are staged/);
  await click('Validate whole collection'); await screen.findByText(/Validation was accepted/);
  await click('Read original validation result'); await screen.findByText(/sealed result is retained below/);
  expect(screen.getByRole('heading', { name: 'Original sealed validation result' })).toBeInTheDocument();
  expect(screen.getByRole('button', { name: 'Review activation' })).toBeDisabled();
  expect(screen.getByText('validating')).toBeInTheDocument();
  expect(writes.filter(write => /\/(validate|activate|cancel)$/.test(write.path))).toHaveLength(1);
});

it.each(['rejected', 'canceled'])('reconciles actual %s phase after a sealed result and closes private input without another write', async state => {
  const collection = frozen(); const { writes } = await fixture({ invalidValidation: state === 'rejected', afterValidationState: state }); await select();
  await click('Create inactive operation'); await screen.findByText(/Inactive operation created/);
  await click('Upload remaining resources'); await screen.findByText(/All resources are staged/);
  await click('Validate whole collection'); await screen.findByText(/Validation was accepted/);
  await click('Read original validation result'); await screen.findByText(state);
  expect(collection.close).toHaveBeenCalled(); expect(screen.getByRole('heading', { name: 'Original sealed validation result' })).toBeInTheDocument();
  expect(screen.getByRole('button', { name: 'Review activation' })).toBeDisabled();
  expect(writes.filter(write => /\/(validate|activate|cancel)$/.test(write.path))).toHaveLength(1);
});


it('retains a previously read sealed verdict but disables later mutation if current validation history becomes unavailable', async () => {
  frozen(); const options: { validationError?: { status: number; code: string } } = {};
  await fixture(options); await select();
  await click('Create inactive operation'); await screen.findByText(/Inactive operation created/);
  await click('Upload remaining resources'); await screen.findByText(/All resources are staged/);
  await click('Validate whole collection'); await screen.findByText(/Validation was accepted/);
  await click('Read original validation result'); await screen.findByText(/The original validation passed/);
  expect(screen.getByRole('button', { name: 'Review activation' })).toBeEnabled();
  options.validationError = { status: 503, code: 'historyUnavailable' };
  await click('Read original validation result'); expect(screen.getAllByText(/validation history is unavailable/).length).toBeGreaterThan(0);
  expect(screen.getByRole('heading', { name: 'Original sealed validation result' })).toBeInTheDocument();
  expect(screen.getByRole('button', { name: 'Review activation' })).toBeDisabled();
});

it('reconciles a lost activation on the original handle and permits cancellation while accepted results remain pending', async () => {
  const collection = frozen(); const storage = vi.spyOn(Storage.prototype, 'setItem');
  const { writes, fetcher } = await fixture({ lostActivation: true }); await reviewCollection();
  await click('Review activation'); await click('Confirm activation');
  await screen.findByText(/Activation outcome unconfirmed/);
  expect(screen.getByRole('button', { name: 'Review activation' })).toBeDisabled();
  expect(screen.getByRole('link', { name: 'original-1' })).toHaveAttribute('href', '/operations/original-1');
  expect(writes.filter(write => write.path.endsWith('/activate'))).toHaveLength(1);
  await click('Read original progress'); await screen.findByText('applying');
  expect(screen.getByRole('button', { name: 'Review activation' })).toBeDisabled();
  expect(screen.getByRole('button', { name: 'Wait for progress' })).toBeEnabled();
  await click('Cancel original operation'); await click('Confirm cancellation');
  await screen.findByText(/Cancellation was requested/);
  expect(collection.close).toHaveBeenCalled();
  expect(screen.getByRole('region', { name: 'Collection application results' })).toHaveTextContent('Resource results are pending');
  const requests = writes.length;
  await click('Wait for resource results'); await screen.findByText(/Results are pending. Reading again/);
  await click('Stop waiting for results');
  expect(writes).toHaveLength(requests);
  expect(writes.filter(write => write.path.endsWith('/activate'))).toHaveLength(1);
  expect(writes.filter(write => write.path.endsWith('/cancel'))).toHaveLength(1);
  expect(fetcher.mock.calls.filter(([url]) => String(url).includes('/operations/original-1?')).every(([, init]) => init?.method === 'GET')).toBe(true);
  expect(storage).not.toHaveBeenCalled(); expect(document.body.textContent).not.toContain('private activation response');
});

it('requires original progress reconciliation and a new confirmation before retrying uncertain activation', async () => {
  frozen(); const options = { lostActivation: true };
  const { operation, writes } = await fixture(options); await reviewCollection();
  await click('Review activation'); await click('Confirm activation'); await screen.findByText(/Activation outcome unconfirmed/);
  expect(screen.getByRole('button', { name: 'Review activation' })).toBeDisabled();
  operation.state = 'validated'; delete operation.executionResult; options.lostActivation = false;
  await click('Read original progress');
  expect(writes.filter(write => write.path.endsWith('/activate'))).toHaveLength(1);
  await click('Review activation');
  expect(writes.filter(write => write.path.endsWith('/activate'))).toHaveLength(1);
  await click('Confirm activation'); await screen.findByText(/Activation was requested/);
  expect(writes.filter(write => write.path.endsWith('/activate')).map(write => write.path)).toEqual(['/api/v2/operations/original-1/activate', '/api/v2/operations/original-1/activate']);
  expect(writes.filter(write => write.path.endsWith('/prepare') || write.path === '/api/v2/operations')).toHaveLength(2);
  expect(writes.filter(write => write.path.endsWith('/validate'))).toHaveLength(1);
});

function partialImportResult(operation: Operation, cursor: string | null): Operation {
  const counts = { processed: 2, accepted: 1, unchanged: 0, conflicts: 1, dependencyBlocked: 0, unattempted: 0, childPending: 0, childApplied: 0, childFailed: 1, childSuperseded: 0, childInvalidated: 0 };
  const finalizedAt = '2026-09-24T00:00:00Z';
  const ordinal = cursor ? 2 : 1;
  return { ...operation, state: 'partial', committed: 1, applied: 0, executionResult: { state: 'ready', counts,
    summary: { ...counts, resultID: 'original-result', uploadID: 'original-upload', planID: 'original-plan', planDigest: 'b'.repeat(64), outcome: 'partial', itemCount: 2, bytes: 700, digest: 'c'.repeat(64), finalizedAt, expiresAt: '2026-10-24T00:00:00Z' } },
    items: [{ id: `Monitor/monitor-${ordinal}`, kind: 'Monitor', inputOrdinal: ordinal, planOrdinal: ordinal, source: collectionSourceToken(1), sourceDocument: ordinal, sourceItem: 1,
      decidedAt: finalizedAt, committedIndex: ordinal, catalogDecision: cursor ? 'conflict' : 'accepted', outcome: cursor ? 'conflict' : 'accepted', committed: !cursor,
      ...(cursor ? {} : { uid: 'original-uid', newVersion: 'rv-1', generation: 1, applied: false, childDisposition: { operationID: 'original-child', state: 'failed', outcome: 'projection_failed', updatedAt: finalizedAt } }),
    }], nextCursor: cursor ? undefined : 'result-next',
  };
}

it('connects partial application results to bounded source-attributed pages without repeating activation', async () => {
  const collection = frozen(2);
  const options: { count: number; operationRead?: (url: URL, operation: Operation) => unknown } = { count: 2 };
  const { writes, fetcher } = await fixture(options); await reviewCollection();
  await click('Review activation'); await click('Confirm activation'); await screen.findByText(/Activation was requested/);
  await click('Stop waiting'); options.operationRead = (url, operation) => partialImportResult(operation, url.searchParams.get('cursor'));
  await click('Read original progress'); await screen.findByText('partial');
  expect(collection.close).toHaveBeenCalled();
  expect(screen.queryByRole('region', { name: 'Application resource results' })).not.toBeInTheDocument();
  await click('Read resource results');
  const table = await screen.findByRole('region', { name: 'Application resource results' });
  expect(table).toHaveTextContent('Change committed'); expect(table).toHaveTextContent('Application failed');
  expect(table).toHaveTextContent('original-child'); expect(table).toHaveTextContent('service.yaml');
  expect(screen.getByLabelText('Application counts')).toHaveTextContent('Committed changes: 1');
  expect(screen.getByLabelText('Application counts')).toHaveTextContent('Controller applied: 0');
  await click('Next results page');
  await waitFor(() => expect(screen.getByRole('region', { name: 'Application resource results' })).toHaveTextContent('Monitor/monitor-2'));
  expect(screen.getByRole('region', { name: 'Application resource results' })).not.toHaveTextContent('Monitor/monitor-1');
  expect(screen.getByRole('region', { name: 'Application resource results' })).toHaveTextContent('Version conflict');
  const pages = fetcher.mock.calls.filter(([url]) => String(url).includes('/operations/original-1?'));
  expect(String(pages.at(-1)![0])).toContain('?limit=100&cursor=result-next');
  expect(pages.every(([, init]) => init?.method === 'GET')).toBe(true);
  expect(writes.filter(write => write.path.endsWith('/activate'))).toHaveLength(1);
  expect(screen.getByRole('button', { name: 'Cancel original operation' })).toBeDisabled();
});

it('keeps an expired sealed validation read-only and does not open activation confirmation', async () => {
  frozen(); await fixture({ validationExpiresAt: '2026-09-21T00:00:00Z' }); await reviewCollection();
  expect(screen.getByRole('heading', { name: 'Original sealed validation result' })).toBeInTheDocument();
  expect(screen.getByRole('button', { name: 'Review activation' })).toBeDisabled();
  expect(screen.queryByRole('alertdialog')).not.toBeInTheDocument();
});

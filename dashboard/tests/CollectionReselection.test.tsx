import { afterAll, afterEach, beforeAll, expect, it, vi } from 'vitest';
// Node fetch/TextEncoder use host buffers. FileReader's DOM buffer is copied
// into that realm before it crosses the actual session's binary boundary.
vi.hoisted(() => { const encoded = new TextEncoder().encode(''); vi.stubGlobal('Uint8Array', encoded.constructor); vi.stubGlobal('ArrayBuffer', encoded.buffer.constructor); });
import { act, cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { CollectionReselection } from '../src/components/CollectionReselection';
import { SessionBoundary } from '../src/auth/SessionBoundary';
import { DashboardSession } from '../src/api/session';
import { reselectionOperations, type ReselectionStatus } from '../src/api/collectionReselection';
import { fileNormalizationProfile } from '../src/api/collectionInventory';

const id = 'original-operation';
const attempt = '12345678-1234-1234-1234-123456789abc';
const contents = 'PRIVATE-COMPONENT-FILE';
const original: ReselectionStatus = { id: attempt, operationID: id, normalizationProfile: fileNormalizationProfile, phase: 'uploading', sourceCount: 1, sourcesCompleted: 0,
  rawBytes: 0, nextSource: 1, nextOffset: 0, expiresAt: '2099-09-30T12:00:00Z', operationUploaded: 1 };
const received: ReselectionStatus = { ...original, sourcesCompleted: 1, rawBytes: contents.length, nextSource: 0 };
const json = (value: unknown, status = 200) => new Response(status === 204 ? null : JSON.stringify(value), { status });
const arrayBufferDescriptor = Object.getOwnPropertyDescriptor(Blob.prototype, 'arrayBuffer');
beforeAll(() => { Object.defineProperty(Blob.prototype, 'arrayBuffer', { configurable: true, value: function (this: Blob): Promise<ArrayBuffer> {
  return new Promise((resolve, reject) => { const reader = new FileReader(); reader.onerror = () => reject(reader.error); reader.onload = () => resolve(Uint8Array.from(new Uint8Array(reader.result as ArrayBuffer)).buffer); reader.readAsArrayBuffer(this); });
} }); });
afterAll(() => { if (arrayBufferDescriptor) Object.defineProperty(Blob.prototype, 'arrayBuffer', arrayBufferDescriptor); else Reflect.deleteProperty(Blob.prototype, 'arrayBuffer'); });
afterEach(() => { cleanup(); vi.restoreAllMocks(); });

async function fixture(handler: (url: URL, init: RequestInit) => Promise<Response>) {
  const requests: { url: URL; init: RequestInit }[] = [];
  const onProgress = vi.fn();
  const fetcher = vi.fn<typeof fetch>(async (input, init = {}) => {
    const url = new URL(String(input));
    if (url.pathname.endsWith('/self')) return new Headers(init.headers).has('Authorization')
      ? json({ principalId: 'operator', role: 'operator', permissions: ['GetCapabilities', ...reselectionOperations] }) : json({}, 401);
    if (url.pathname.endsWith('/discovery')) return json({ resourceOperations: { Operation: reselectionOperations } });
    requests.push({ url, init });
    return handler(url, init);
  });
  const session = new DashboardSession({ origin: 'https://cpra.example', fetch: fetcher });
  await session.discover(); await session.signIn('PRIVATE-COMPONENT-BEARER');
  const rendered = render(<SessionBoundary session={session}><CollectionReselection id={id} onProgress={onProgress} /></SessionBoundary>);
  await screen.findByLabelText('Original collection files');
  return { session, requests, onProgress, ...rendered };
}
async function selectFile() {
  const input = await screen.findByLabelText<HTMLInputElement>('Original collection files');
  await act(async () => { fireEvent.change(input, { target: { files: [new File([contents], 'PRIVATE-FILENAME.yaml')] } }); });
  expect(input.value).toBe('');
  expect(screen.getByText(/1 files selected/)).toBeInTheDocument();
}
async function click(name: string) { const button = await screen.findByRole('button', { name }); await waitFor(() => expect(button).toBeEnabled()); await act(async () => { fireEvent.click(button); }); }
const writes = (requests: { init: RequestInit }[]) => requests.filter(request => request.init.method !== 'GET');
const puts = (requests: { init: RequestInit }[]) => requests.filter(request => request.init.method === 'PUT');
function privateDOM() { for (const value of [contents, 'PRIVATE-FILENAME', 'PRIVATE-COMPONENT-BEARER', 'PRIVATE-ERROR']) expect(document.body.textContent).not.toContain(value); }

it('rejects a stale200 source reply without sending a second PUT or automatic verify', async () => {
  const { requests } = await fixture(async (_, init) => json(original, init.method === 'POST' ? 201 : 200));
  await selectFile(); await click('Verify original files');
  await screen.findByText(/response was lost or could not confirm progress/);
  expect(puts(requests)).toHaveLength(1); expect(writes(requests)).toHaveLength(2);
  expect(requests.some(request => request.url.pathname.endsWith('/verify'))).toBe(false);
  expect(screen.getByRole('button', { name: 'Continue file verification' })).toBeDisabled();
  expect((puts(requests)[0].init.body as Uint8Array).every(byte => byte === 0)).toBe(true);
  privateDOM();
});

it('reconciles a lost verify reply by GET, releases selected files, and requires explicit resume', async () => {
  const storage = vi.spyOn(Storage.prototype, 'setItem');
  const { requests } = await fixture(async (url, init) => {
    if (url.pathname.endsWith('/verify')) throw new Error('PRIVATE-ERROR');
    if (init.method === 'PUT') return json(received);
    if (init.method === 'GET') return json({ ...received, phase: 'verified' });
    if (url.pathname.endsWith('/resume')) return json({ ...received, phase: 'completed', operationUploaded: 2 }, 202);
    return json(original, 201);
  });
  await selectFile(); await click('Verify original files'); await screen.findByText(/response was lost or could not confirm progress/);
  expect(screen.getByText(/1 files selected/)).toBeInTheDocument();
  await click('Read attempt progress'); await screen.findByRole('button', { name: 'Resume original upload' });
  expect(screen.queryByText(/files selected/)).not.toBeInTheDocument();
  expect(screen.queryByRole('button', { name: 'Continue file verification' })).not.toBeInTheDocument();
  expect(requests.filter(request => request.url.pathname.endsWith('/verify'))).toHaveLength(1);
  expect(requests.some(request => request.url.pathname.endsWith('/resume'))).toBe(false);
  const unload = new Event('beforeunload', { cancelable: true }); window.dispatchEvent(unload); expect(unload.defaultPrevented).toBe(false);
  await click('Resume original upload'); await screen.findByText(/original upload is complete/);
  expect(requests.filter(request => request.url.pathname.endsWith('/resume'))).toHaveLength(1);
  expect(puts(requests)).toHaveLength(1); expect(storage).not.toHaveBeenCalled(); privateDOM();
});

it.each([404, 410])('clears an unavailable attempt (%i), refreshes the original, and waits for explicit reselection', async status => {
  const { requests, onProgress } = await fixture(async (_, init) => {
    if (init.method === 'PUT') throw new Error('PRIVATE-ERROR');
    if (init.method === 'GET') return json({ code: 'reselectionNotFound' }, status);
    return json(original, 201);
  });
  await selectFile(); await click('Verify original files'); await screen.findByText(/response was lost or could not confirm progress/);
  await click('Read attempt progress'); await screen.findByText(/temporary attempt is no longer available/);
  expect(onProgress).toHaveBeenCalledTimes(1); expect(screen.getByLabelText('Original collection files')).toBeEnabled();
  expect(screen.queryByText(/files selected/)).not.toBeInTheDocument();
  expect(screen.queryByRole('button', { name: 'Verify original files' })).not.toBeInTheDocument();
  expect(writes(requests)).toHaveLength(2);
  await selectFile(); expect(writes(requests)).toHaveLength(2);
  await click('Verify original files'); await screen.findByText(/response was lost or could not confirm progress/);
  expect(requests.filter(request => request.init.method === 'POST' && request.url.pathname.endsWith('/reselection'))).toHaveLength(2);
  privateDOM();
});

it('allows a confirmed busy-create rejection to retry only after another explicit click', async () => {
  let creates = 0;
  const { requests } = await fixture(async (url, init) => {
    if (url.pathname.endsWith('/reselection')) { creates++; return creates === 1 ? json({ code: 'reselectionBusy' }, 429) : json(original, 201); }
    if (init.method === 'PUT') return json(received);
    return json({ ...received, phase: 'verified' }, 202);
  });
  await selectFile(); await click('Verify original files'); await screen.findByText(/Verification capacity is busy/);
  expect(creates).toBe(1); expect(puts(requests)).toHaveLength(0);
  expect(screen.getByRole('button', { name: 'Verify original files' })).toBeEnabled();
  await click('Verify original files'); await screen.findByRole('button', { name: 'Resume original upload' });
  expect(creates).toBe(2); expect(puts(requests)).toHaveLength(1); privateDOM();
});

it('does not create another attempt after a genuinely lost creation reply', async () => {
  const { requests } = await fixture(async () => { throw new Error('PRIVATE-ERROR'); });
  await selectFile(); await click('Verify original files'); await screen.findByText(/temporary attempt identity was not received/);
  expect(writes(requests)).toHaveLength(1); expect(puts(requests)).toHaveLength(0);
  expect(screen.getByRole('button', { name: 'Verify original files' })).toBeDisabled();
  expect(screen.queryByLabelText('Original collection files')).not.toBeInTheDocument(); privateDOM();
});

it.each(['sign-out', 'unmount'] as const)('%s aborts an in-flight source and clears bytes before ignoring its late response', async event => {
  let release: ((response: Response) => void) | undefined;
  const { session, requests, unmount } = await fixture(async (_, init) => init.method === 'PUT' ? new Promise<Response>(resolve => { release = resolve; }) : json(original, 201));
  await selectFile(); await click('Verify original files');
  await waitFor(() => expect(release).toBeDefined());
  const source = puts(requests)[0]; expect(source.init.signal?.aborted).toBe(false);
  await act(async () => { if (event === 'sign-out') session.signOut(); else unmount(); });
  expect(source.init.signal?.aborted).toBe(true);
  await act(async () => { release!(json(received)); });
  await waitFor(() => expect((source.init.body as Uint8Array).every(byte => byte === 0)).toBe(true));
  expect(writes(requests)).toHaveLength(2); expect(requests.some(request => request.url.pathname.endsWith('/verify'))).toBe(false);
  expect(screen.queryByText(/files selected/)).not.toBeInTheDocument(); privateDOM();
});

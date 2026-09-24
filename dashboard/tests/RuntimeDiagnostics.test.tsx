import { afterEach, expect, it, vi } from 'vitest';
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { DashboardSession } from '../src/api/session';
import { runtimeView } from '../src/api/runtime';
import { SessionBoundary } from '../src/auth/SessionBoundary';
import { RuntimeDiagnostics } from '../src/components/RuntimeDiagnostics';

afterEach(() => { cleanup(); vi.restoreAllMocks(); });

function stateWire() {
  return { generatedAt: '2026-09-19T12:00:00Z', ready: true, live: true, controllerAvailable: true, controllerReady: true, controllerReason: '',
    projectionAvailable: true, projectionFresh: false, projectionAgeMs: 60_001, unknownActionsAvailable: false, unknownActions: 0,
    storage: { available: true, mode: 'raft', formatVersion: 1, commitIndex: 12, appliedIndex: 12, singleNode: true, bytesAvailable: false,
      commitLatency: { available: true, value: 0 }, snapshotDuration: { available: false, value: 0, reason: 'not_recorded' } } };
}

async function fixture(options: { permissions?: string[]; legacy?: boolean; readyStatus?: number; readyBody?: unknown; stateBody?: unknown; stateStatus?: number } = {}) {
  const wire = { stateStatus: options.stateStatus ?? 200, stateBody: options.stateBody ?? stateWire(), readyStatus: options.readyStatus ?? 200, readyBody: options.readyBody ?? { available: true, generatedAt: '2026-09-19T12:00:00Z' } };
  const requests: { path: string; method: string; authorization: string | null }[] = [];
  const response = (body: unknown, status = 200) => new Response(JSON.stringify(body), { status });
  const fetcher = vi.fn<typeof fetch>().mockImplementation(async (input, init) => {
    const path = new URL(String(input)).pathname;
    requests.push({ path, method: init?.method ?? 'GET', authorization: new Headers(init?.headers).get('Authorization') });
    if (path === '/api/v2/discovery') return options.legacy ? response({}, 404) : response({ apiVersions: ['cpra.io/v2'], resources: [], drivers: {}, patchTypes: [] });
    if (path === '/api/v2/self') return options.legacy ? response({}, 404) : response({ principalId: 'alice', role: 'reader', permissions: options.permissions ?? ['GetState', 'GetReady'] });
    if (path === '/api/v2/state') return response(wire.stateBody, wire.stateStatus);
    if (path === '/api/v2/readyz') return response(wire.readyBody, wire.readyStatus);
    if (path === '/api/v1/state') return response({ storage: { ready: true, mode: 'memory', committed_index: 0, commit_latency_ms: 0, snapshot_duration_ms: 0 }, actions: [] });
    return response({}, 404);
  });
  const session = new DashboardSession({ origin: 'https://cpra.example', fetch: fetcher });
  await session.discover();
  if (!options.legacy) await session.signIn('test-token');
  render(<SessionBoundary session={session}><RuntimeDiagnostics /></SessionBoundary>);
  return { wire, requests };
}

const valueFor = (label: string) => screen.getByText(label, { selector: 'dt' }).nextElementSibling;

it('decodes real state fields with independent availability and preserves measured zero', () => {
  const wire = { ...stateWire(), actions: [{ secret: 'private-action-payload' }], readinessReason: 'private unsupported reason', storage: { ...stateWire().storage, error: '/private/server/path: private-token' } };
  const observation = runtimeView(wire);
  expect(observation).toMatchObject({ ready: true, controllerReady: true, projectionFresh: false, projectionAgeMs: 60_001, storage: { available: true, appliedIndex: 12, commitLatencyMs: 0, error: true } });
  expect(observation.storage.snapshotDurationMs).toBeUndefined();
  expect(observation.readinessReason).toBe('The server reported an unrecognized reason.');
  expect(JSON.stringify(observation)).not.toMatch(/private-|\/private/);
});

it('keeps optional or invalid measurements unavailable instead of inventing successful observations', () => {
  const observation = runtimeView({ generatedAt: '2026-09-19T12:00:00Z', ready: false, live: true, controllerReady: true, projectionFresh: true, projectionAgeMs: 0,
    storage: { available: false, mode: 'future-format', appliedIndex: -1, commitLatency: { available: true, value: -1 }, snapshotDuration: { available: true, value: Infinity } }, controllerReason: 'future-condition' });
  expect(observation.controllerReady).toBeUndefined();
  expect(observation.projectionFresh).toBeUndefined();
  expect(observation.projectionAgeMs).toBeUndefined();
  expect(observation.storage).toMatchObject({ available: false, mode: 'Unrecognized storage mode' });
  expect(observation.storage.appliedIndex).toBeUndefined();
  expect(observation.storage.commitLatencyMs).toBeUndefined();
  expect(observation.storage.snapshotDurationMs).toBeUndefined();
  expect(observation.controllerReason).toBe('The server reported an unrecognized reason.');
  const invalidFlags = runtimeView({ ...stateWire(), controllerReady: 'yes', projectionFresh: 1, projectionAgeMs: -1 });
  expect(invalidFlags.controllerReady).toBeUndefined();
  expect(invalidFlags.projectionFresh).toBeUndefined();
  expect(invalidFlags.projectionAgeMs).toBeUndefined();
});

it.each([null, {}, { ...stateWire(), ready: 'yes' }, { ...stateWire(), live: null }, { ...stateWire(), generatedAt: 'not-a-time' }, { ...stateWire(), generatedAt: '0001-01-01T00:00:00Z' }, { ...stateWire(), storage: { available: 'true', mode: 'raft' } }])('rejects unsupported required state fields safely: %j', value => {
  expect(() => runtimeView(value)).toThrow('The runtime observation has an unsupported format.');
});

it('shows ready controller and storage separately from stale dashboard projection with GET-only reads', async () => {
  const { requests } = await fixture();
  await screen.findByText('Ready for new work');
  expect(valueFor('Controller initialization and progress')).toHaveTextContent(/^Ready$/);
  expect(valueFor('Dashboard projection')).toHaveTextContent(/^Stale$/);
  expect(valueFor('Projection age')).toHaveTextContent('60,001 ms');
  expect(valueFor('Storage availability')).toHaveTextContent(/^Available$/);
  expect(valueFor('Last recorded commit latency')).toHaveTextContent(/^0 ms$/);
  expect(valueFor('Last recorded snapshot duration')).toHaveTextContent(/^Unavailable$/);
  expect(await screen.findByText(/^Readiness endpoint:/)).toHaveTextContent('Ready');
  expect(screen.getByText(/intentionally empty configuration/)).toHaveTextContent('can be ready when the server permits it');
  expect(screen.getByText(/A stale projection/)).toHaveTextContent('does not prove a disk failure');
  expect(requests.every(request => request.method === 'GET')).toBe(true);
  expect(requests.filter(request => !request.path.endsWith('/self') && !request.path.endsWith('/discovery')).map(request => request.path).sort()).toEqual(['/api/v2/readyz', '/api/v2/state']);
});

it('renders the explicit notReady response and draining reason while liveness remains available', async () => {
  await fixture({ stateBody: { ...stateWire(), ready: false, controllerReady: false, readinessReason: 'Admission has stopped while CPRa drains.' }, readyStatus: 503, readyBody: { code: 'notReady', status: 503, detail: 'private diagnostics must not render' } });
  expect(await screen.findByText(/^Readiness endpoint:/)).toHaveTextContent('Not ready');
  expect(valueFor('Admission readiness (state)')).toHaveTextContent('Unavailable for new work');
  expect(valueFor('Process liveness (state)')).toHaveTextContent(/^Live$/);
  expect(screen.getByText('Admission has stopped while CPRa drains.')).toBeInTheDocument();
  expect(document.body.textContent).not.toContain('private diagnostics');
});

it.each([{ readyStatus: 503, readyBody: { code: 'upstreamUnavailable' } }, { readyBody: { available: 'true' } }, { readyBody: {} }])('does not mistake failed or malformed readiness for an unready application: %j', async options => {
  await fixture(options);
  await screen.findByText('Readiness endpoint is unavailable; readiness could not be determined.');
  expect(screen.queryByText(/^Readiness endpoint:/)).not.toBeInTheDocument();
});

it('does not present a Go zero readiness timestamp as an observation time', async () => {
  await fixture({ readyBody: { available: true, generatedAt: '0001-01-01T00:00:00Z' } });
  const endpoint = await screen.findByText(/^Readiness endpoint:/);
  expect(endpoint).toHaveTextContent('Ready');
  expect(endpoint).not.toHaveTextContent('Observed');
});

it('honors separate state/readiness permissions without trying a legacy route', async () => {
  const { requests } = await fixture({ permissions: ['GetReady'] });
  await screen.findByText(/^Readiness endpoint:/);
  expect(screen.getByText('Your identity cannot read runtime state.')).toBeInTheDocument();
  expect(requests.some(request => request.path.endsWith('/state'))).toBe(false);
  cleanup();
  const denied = await fixture({ permissions: [] });
  await screen.findByText('Your identity cannot read the readiness endpoint.');
  expect(denied.requests.filter(request => !request.path.endsWith('/self') && !request.path.endsWith('/discovery'))).toHaveLength(0);
});

it('uses the limited legacy storage view only for an explicitly discovered legacy server', async () => {
  const { requests } = await fixture({ legacy: true });
  await screen.findByText('Memory (not persistent)');
  expect(screen.getByText(/Legacy observation mode/)).toBeInTheDocument();
  expect(valueFor('Admission readiness (state)')).toHaveTextContent(/^Unavailable$/);
  expect(valueFor('Controller initialization and progress')).toHaveTextContent(/^Unavailable$/);
  expect(valueFor('Dashboard projection')).toHaveTextContent(/^Unavailable$/);
  expect(valueFor('Last recorded commit latency')).toHaveTextContent(/^Unavailable$/);
  expect(valueFor('FSM-applied log position')).toHaveTextContent(/^0$/);
  expect(requests.map(request => request.path)).toEqual(['/api/v2/self', '/api/v1/state']);
  expect(requests.every(request => request.authorization === null && request.method === 'GET')).toBe(true);
});

it('hides stale success after a denied refresh and does not downgrade to legacy', async () => {
  const { wire, requests } = await fixture();
  await screen.findByText('Ready for new work');
  wire.stateStatus = 403;
  wire.stateBody = { code: 'forbidden' };
  fireEvent.click(screen.getByRole('button', { name: 'Refresh runtime observations' }));
  await screen.findByText('Runtime state is unavailable. No previous successful observation is shown as current.');
  expect(screen.queryByText('Ready for new work')).not.toBeInTheDocument();
  await waitFor(() => expect(requests.filter(request => request.path === '/api/v2/state')).toHaveLength(2));
  expect(requests.some(request => request.path.startsWith('/api/v1/'))).toBe(false);
});

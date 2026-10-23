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

async function fixture(options: { permissions?: string[]; observation?: boolean; readyStatus?: number; readyBody?: unknown; stateBody?: unknown; stateStatus?: number } = {}) {
  const wire = { stateStatus: options.stateStatus ?? 200, stateBody: options.stateBody ?? stateWire(), readyStatus: options.readyStatus ?? 200, readyBody: options.readyBody ?? { available: true, generatedAt: '2026-09-19T12:00:00Z' } };
  const requests: { path: string; method: string; authorization: string | null }[] = [];
  const response = (body: unknown, status = 200) => new Response(JSON.stringify(body), { status });
  const fetcher = vi.fn<typeof fetch>().mockImplementation(async (input, init) => {
    const path = new URL(String(input)).pathname;
    requests.push({ path, method: init?.method ?? 'GET', authorization: new Headers(init?.headers).get('Authorization') });
    if (path === '/api/v2/discovery') return options.observation ? response({}, 404) : response({ apiVersions: ['cpra.io/v2'], resources: [], drivers: {}, patchTypes: [] });
    if (path === '/api/v2/access') return new Response(JSON.stringify({ mode: options.observation ? 'observation' : 'management', authenticationRequired: !options.observation }));
    if (path === '/api/v2/self') return response({ principalId: options.observation ? 'anonymous' : 'alice', role: 'reader', permissions: options.permissions ?? ['GetState', 'GetReady'] });
    if (path === '/api/v2/state') return response(wire.stateBody, wire.stateStatus);
    if (path === '/api/v2/readyz') return response(wire.readyBody, wire.readyStatus);
    if (path === '/api/v1/state') return response({ storage: { ready: true, mode: 'memory', committed_index: 0, commit_latency_ms: 0, snapshot_duration_ms: 0 }, actions: [] });
    return response({}, 404);
  });
  const session = new DashboardSession({ origin: 'https://cpra.example', fetch: fetcher });
  await session.discover();
  if (!options.observation) await session.signIn('test-token-01234567890123456789012345678901');
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

it('reports sampled cleanup accounting without exposing unknown diagnostics or changing admission readiness', async () => {
  const wire = stateWire();
  const stateBody = { ...wire, storage: { ...wire.storage, collectionGenerations: { available: true, active: 1, retained: 1, pinned: 0, retired: 4, unlinkedBytes: 4096, cleanupCondition: 'blocked', stepDurationMs: 12 } } };
  await fixture({ stateBody });
  await screen.findByText('Ready for new work');
  expect(valueFor('Retired collection generations')).toHaveTextContent(/^4$/);
  expect(valueFor('Pinned collection generations')).toHaveTextContent(/^0$/);
  expect(screen.getByText(/Cleanup is held/)).toBeInTheDocument();
  const unknown = runtimeView({ ...stateBody, storage: { ...stateBody.storage, collectionGenerations: { ...stateBody.storage.collectionGenerations, cleanupCondition: '/private/state/path' } } });
  expect(JSON.stringify(unknown)).not.toContain('/private/');
  expect(unknown.ready).toBe(true);
});

it.each([null, {}, { ...stateWire(), ready: 'yes' }, { ...stateWire(), live: null }, { ...stateWire(), generatedAt: 'not-a-time' }, { ...stateWire(), generatedAt: '0001-01-01T00:00:00Z' }, { ...stateWire(), storage: { available: 'true', mode: 'raft' } }])('rejects unsupported required state fields safely: %j', value => {
  expect(() => runtimeView(value)).toThrow('The runtime observation has an unsupported format.');
});

it('shows partial file allocation with explicit unavailable values and a bounded subtotal', async () => {
  const wire = stateWire();
  const generations = { available: true, active: 1, retained: 1, pinned: 0, retired: 4, unlinkedBytes: 4096, stepDurationMs: 1,
    usageCoverage: 'partial', usageGenerations: 1, usageExpected: 2, knownApparentBytes: 0, usageOldestAt: '2026-09-19T11:59:00Z' };
  await fixture({ stateBody: { ...wire, storage: { ...wire.storage, collectionGenerations: generations } } });
  await screen.findByText('Ready for new work');
  expect(valueFor('Measured collection generations')).toHaveTextContent(/^1 \/ 2$/);
  expect(valueFor('Known collection file lengths')).toHaveTextContent(/^0 bytes$/);
  expect(valueFor('Known collection file allocation')).toHaveTextContent(/^Unavailable$/);
  expect(screen.getByText(/Collection file usage covers part/)).toHaveTextContent('exclude incomplete collections');
  const unknown = runtimeView({ ...wire, storage: { ...wire.storage, collectionGenerations: { ...generations, usageCoverage: '/private/file/path', knownAllocatedBytes: -1 } } });
  expect(unknown.storage.generations?.knownAllocatedBytes).toBeUndefined();
  expect(JSON.stringify(unknown)).not.toContain('/private/');
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
  expect(requests.filter(request => !['/self', '/access', '/discovery'].some(suffix => request.path.endsWith(suffix))).map(request => request.path).sort()).toEqual(['/api/v2/readyz', '/api/v2/state']);
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
  expect(denied.requests.filter(request => !['/self', '/access', '/discovery'].some(suffix => request.path.endsWith(suffix)))).toHaveLength(0);
});

it('uses current state and readiness in observation mode without writable management access', async () => {
  const { requests } = await fixture({ observation: true, stateBody: { ...stateWire(), storage: { ...stateWire().storage, mode: 'memory' } } });
  await screen.findByText('Memory (not persistent)');
  expect(screen.getByText(/Observation mode/)).toBeInTheDocument();
  expect(valueFor('Admission readiness (state)')).toHaveTextContent(/^Ready for new work$/);
  expect(valueFor('Controller initialization and progress')).toHaveTextContent(/^Ready$/);
  expect(valueFor('Dashboard projection')).toHaveTextContent(/^Stale$/);
  expect(valueFor('Last recorded commit latency')).toHaveTextContent(/^0 ms$/);
  expect(valueFor('Committed controller log position')).toHaveTextContent(/^12$/);
  await screen.findByText(/^Readiness endpoint:/);
  expect(requests.map(request => request.path)).toEqual(['/api/v2/access', '/api/v2/self', '/api/v2/state', '/api/v2/readyz']);
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

it('shows bounded action storage accounting separately from physical allocation and admission', async () => {
  const wire = stateWire();
  const actions = { observedAt: '2026-10-23T00:00:00Z', active: 3, ordinaryRetained: 12, protected: 2, tombstones: 0,
    activeBytes: 1024, ordinaryBytes: 4096, protectedBytes: 512, tombstoneBytes: 0, retired: 7, retiredBytes: 2048,
    cleanupBlocked: 1, cleanupBlockedBytes: 256, cleanupCoverage: 'last_selection_page', compactionCondition: 'copying' };
  await fixture({ stateBody: { ...wire, storage: { ...wire.storage, actions } } });
  await screen.findByText('Ready for new work');
  expect(valueFor('Action storage accounting')).toHaveTextContent(/^Sampled /);
  expect(valueFor('Protected action records')).toHaveTextContent(/^2 records · 512 bytes$/);
  expect(valueFor('Action retirement tombstones')).toHaveTextContent(/^0 records · 0 bytes$/);
  expect(valueFor('Cleanup-blocked action records (last page)')).toHaveTextContent(/^1 record · 256 bytes$/);
  expect(screen.getByText(/Cleanup-blocked counts cover only the last retirement selection page/)).toBeInTheDocument();
  expect(screen.getByText(/Action byte counts measure encoded records/)).toHaveTextContent('not filesystem allocation or reclaimed disk space');
  expect(screen.getByText('Action storage compaction is copying retained records.')).toBeInTheDocument();
  const safe = runtimeView({ ...wire, storage: { ...wire.storage, actions: { ...actions, protectedBytes: -1,
    active: Number.MAX_SAFE_INTEGER + 1, compactionCondition: '/private/action/path', cleanupCoverage: 'private-token', privateRows: ['private-action'] } } });
  expect(safe.storage.actions?.protectedBytes).toBeUndefined();
  expect(safe.storage.actions?.active).toBeUndefined();
  expect(JSON.stringify(safe)).not.toContain('private');
  expect(safe.ready).toBe(true);
});

it('keeps absent action accounting unavailable instead of showing empty retained storage', async () => {
  await fixture();
  await screen.findByText('Ready for new work');
  expect(valueFor('Action storage accounting')).toHaveTextContent(/^Unavailable$/);
  expect(screen.queryByText('Protected action records')).not.toBeInTheDocument();
});

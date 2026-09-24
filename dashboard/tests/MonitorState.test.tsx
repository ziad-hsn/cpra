import { afterEach, expect, it, vi } from 'vitest';
import { cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react';
import { createMemoryRouter, RouterProvider } from 'react-router-dom';
import { AppRoutes } from '../src/App';
import { DashboardSession } from '../src/api/session';
import { SessionBoundary } from '../src/auth/SessionBoundary';
import { operationContracts } from '../src/api/generated';
import { resource } from './managementFixture';

afterEach(() => { cleanup(); vi.restoreAllMocks(); });

async function fixture(options: { reader?: boolean; permissions?: string[]; unsupported?: boolean; configuration?: boolean; mismatched?: boolean; missingUID?: boolean; routeID?: string } = {}) {
  const id = 'checkout:eu';
  const monitor = resource('monitors', id, { enabled: true, check: { driver: options.unsupported ? { type: 'external', config: { privatePayload: 'must-not-reach-the-dom' }, jobTypeRef: 'operator-job' } : { type: 'tcp', config: { host: 'checkout.example.test', port: 443 } }, interval: '60s', timeout: '5s' } });
  monitor.status = { controlRevision: 'control-1', executionRevision: 'execution-1', incidentID: 'incident-1', health: 'unhealthy', unknownActions: 1, lastCheckLatencyMs: { available: true, value: 0 } };
  if (options.mismatched) monitor.metadata.id = 'different-monitor';
  if (options.missingUID) monitor.metadata.uid = '';
  const requests: { path: string; method: string; search: URLSearchParams }[] = [];
  const writes: { path: string; method: string; match: string | null; body: unknown }[] = [];
  const state = { readStatus: 200 };
  const reply = (body: unknown, status = 200) => new Response(JSON.stringify(body), { status });
  const permissions = options.permissions ?? Object.keys(operationContracts).filter(name => !options.reader || name.startsWith('Get') || name.startsWith('List'));
  const fetcher = vi.fn<typeof fetch>().mockImplementation(async (input, init) => {
    const url = new URL(String(input));
    const path = url.pathname;
    const method = init?.method ?? 'GET';
    requests.push({ path, method, search: url.searchParams });
    if (path === '/api/v2/discovery') return reply({ apiVersions: ['cpra.io/v2'], resources: [], patchTypes: ['application/merge-patch+json'], drivers: { check: ['tcp'], recovery: [], notification: [] } });
    if (path === '/api/v2/self') return reply({ principalId: 'alice', role: options.reader ? 'reader' : 'operator', permissions });
    if (method !== 'GET') {
      const body = JSON.parse(String(init?.body)) as Record<string, unknown>;
      writes.push({ path, method, body, match: new Headers(init?.headers).get('If-Match') });
      if (method === 'PATCH') monitor.spec.enabled = (body.spec as { enabled: boolean }).enabled;
      return reply(monitor);
    }
    if (path === `/api/v2/monitors/${encodeURIComponent(id)}`) return reply(state.readStatus === 200 ? monitor : { code: 'notFound' }, state.readStatus);
    if (path === '/api/v2/incidents/incident-1') return reply({ id: 'incident-1', monitorID: id, revision: 'attention-1', state: 'open', acknowledgedBy: 'alice', acknowledgedAt: '2026-09-19T12:00:00Z' });
    const action = { id: 'action-1', monitorID: id, incarnationUID: monitor.metadata.uid, state: 'unknown', kind: 'intervention', held: true, executorFenced: true, reviewRevision: 'review-1' };
    if (path === '/api/v2/actions') return reply({ items: [action] });
    if (path === '/api/v2/actions/action-1') return reply(action);
    if (path === '/api/v2/history') return reply({ items: [{ id: 'event-1', monitorID: id, time: '2026-09-19T12:00:00Z', kind: 'incident_acknowledged', actor: 'alice', note: 'Checking the checkout deployment' }] });
    // No numeric v1 projection exists for this fixture.
    return reply({}, 404);
  });
  const session = new DashboardSession({ origin: 'https://cpra.example', fetch: fetcher });
  await session.discover(); await session.signIn('test-token');
  const router = createMemoryRouter([{ path: '*', element: <SessionBoundary session={session}><AppRoutes /></SessionBoundary> }], {
    initialEntries: [options.configuration ? `/monitor-configurations/${encodeURIComponent(id)}` : `/monitors/by-id/${encodeURIComponent(options.routeID ?? id)}`],
  });
  render(<RouterProvider router={router} />);
  return { id, monitor, requests, writes, state, router };
}

it('links an unsupported saved monitor to observations, history and generic controls without a numeric snapshot', async () => {
  const { id, requests, writes } = await fixture({ unsupported: true, configuration: true });
  const link = await screen.findByRole('link', { name: 'View monitoring and controls' });
  expect(link).toHaveAttribute('href', `/monitors/by-id/${encodeURIComponent(id)}`);
  expect(screen.queryByRole('button', { name: 'Edit monitor' })).not.toBeInTheDocument();
  fireEvent.click(link);
  await screen.findByRole('heading', { name: 'Monitor state' });
  expect(await screen.findByText(/Acknowledged by alice/)).toBeInTheDocument();
  expect(await screen.findByText('Checking the checkout deployment')).toBeInTheDocument();
  expect(await screen.findByText(/Provider outcome: unknown/)).toBeInTheDocument();
  expect(screen.getByText('Last-check latency: 0 ms')).toBeInTheDocument();
  expect(screen.getByText(/Supported observations and permitted generic controls remain available/)).toBeInTheDocument();
  expect(document.body.textContent).not.toContain('must-not-reach-the-dom');
  expect(requests.some(request => request.path.startsWith('/api/v1/monitors'))).toBe(false);
  for (const path of ['/api/v2/actions', '/api/v2/history']) {
    const read = requests.find(request => request.path === path);
    expect(read?.search.get('monitorID')).toBe(id);
    expect(read?.search.get('limit')).toBe('100');
  }
  expect(writes).toHaveLength(0);
  fireEvent.click(screen.getByRole('button', { name: 'Disable' }));
  const dialog = await screen.findByRole('dialog', { name: 'Disable' });
  fireEvent.click(within(dialog).getByRole('button', { name: 'Disable' }));
  await waitFor(() => expect(writes).toHaveLength(1));
  expect(writes[0]).toEqual({ path: `/api/v2/monitors/${encodeURIComponent(id)}`, method: 'PATCH', match: '"rv-1"', body: { spec: { enabled: false } } });
});

it('keeps readers on safe observations with no mutation controls', async () => {
  const { writes } = await fixture({ reader: true });
  await screen.findByText(/Read-only access/);
  expect(await screen.findByText(/Acknowledged by alice/)).toBeInTheDocument();
  expect(await screen.findByText('Checking the checkout deployment')).toBeInTheDocument();
  expect(await screen.findByText(/Provider outcome: unknown/)).toBeInTheDocument();
  expect(screen.queryByRole('button', { name: /acknowledge|dismiss|snooze|disable|enable|review action|request recovery/i })).not.toBeInTheDocument();
  expect(writes).toHaveLength(0);
});

it('does not request observations withheld from the current identity', async () => {
  const { requests } = await fixture({ permissions: ['GetMonitor'] });
  await screen.findByText(/Your identity cannot list action outcomes/);
  expect(screen.getByText(/Your identity cannot read event history/)).toBeInTheDocument();
  expect(requests.some(request => ['/api/v2/actions', '/api/v2/history', '/api/v2/incidents/incident-1'].includes(request.path))).toBe(false);
});

it.each(['Acknowledge', 'Review action action-1'])('discards an open %s draft when the stable ID is recreated', async label => {
  const { monitor, writes } = await fixture();
  fireEvent.click(await screen.findByRole('button', { name: label }));
  await screen.findByRole('dialog');
  monitor.metadata.uid = 'replacement-incarnation';
  monitor.metadata.resourceVersion = 'replacement-version';
  fireEvent.click(screen.getByRole('button', { name: 'Refresh monitor state' }));
  await screen.findByText('replacement-incarnation');
  await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument());
  expect(writes).toHaveLength(0);
});

it('removes controls and drafts when a previously readable monitor becomes unavailable', async () => {
  const { state, writes } = await fixture();
  fireEvent.click(await screen.findByRole('button', { name: 'Snooze' }));
  await screen.findByRole('dialog');
  state.readStatus = 404;
  fireEvent.click(screen.getByRole('button', { name: 'Refresh monitor state' }));
  await screen.findByText(/Monitor state is unavailable/);
  expect(screen.queryByRole('dialog')).not.toBeInTheDocument();
  expect(screen.queryByRole('button', { name: 'Snooze' })).not.toBeInTheDocument();
  expect(writes).toHaveLength(0);
});

it('does not expose controls or cross-monitor observations from a mismatched response', async () => {
  const { requests, writes } = await fixture({ mismatched: true });
  await screen.findByText(/Monitor state is unavailable/);
  expect(screen.queryByRole('button', { name: 'Snooze' })).not.toBeInTheDocument();
  expect(requests.some(request => ['/api/v2/actions', '/api/v2/history'].includes(request.path))).toBe(false);
  expect(writes).toHaveLength(0);
});

it('keeps observations readable while monitor controls await an incarnation', async () => {
  await fixture({ missingUID: true });
  await screen.findByText(/Monitor controls are unavailable until its incarnation is reported/);
  expect(await screen.findByText('Checking the checkout deployment')).toBeInTheDocument();
  expect(screen.queryByRole('button', { name: 'Snooze' })).not.toBeInTheDocument();
});

it('rejects an invalid stable route ID without requesting monitor data', async () => {
  const { requests } = await fixture({ routeID: 'invalid monitor' });
  await screen.findByText('This monitor ID is not valid.');
  expect(requests.some(request => request.path.startsWith('/api/v2/monitors/'))).toBe(false);
});

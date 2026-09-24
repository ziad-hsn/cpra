import { afterEach, expect, it, vi } from 'vitest';
import { act, cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react';
import { MemoryRouter } from 'react-router-dom';
import { DashboardSession } from '../src/api/session';
import { SessionBoundary } from '../src/auth/SessionBoundary';
import { MonitorControls } from '../src/components/MonitorControls';
import { operationContracts } from '../src/api/generated';
import { resource } from './managementFixture';

afterEach(() => { cleanup(); vi.restoreAllMocks(); });

async function controls(options: { reader?: boolean; conflict?: boolean; lost?: boolean; lostBody?: boolean; closed?: boolean; snoozed?: boolean; disabled?: boolean; recovery?: boolean; health?: string } = {}) {
  const monitor = resource('monitors', 'checkout', { enabled: !options.disabled, check: { driver: { type: 'tcp', config: { host: 'checkout.example.test', port: 443 } }, interval: '60s', timeout: '5s' } });
  if (options.recovery) monitor.spec.recovery = { driver: { type: 'webhook', config: { url: 'https://recovery.example.test' } } };
  monitor.status = { controlRevision: 'control-1', incidentID: 'incident-1', health: options.health ?? 'unhealthy', ...(options.snoozed ? { snoozedUntil: '2099-01-01T00:00:00Z' } : {}) };
  const incident = { id: 'incident-1', monitorID: 'checkout', revision: 'attention-1', state: options.closed ? 'closed' : 'open', closedAt: options.closed ? '2026-09-01T00:00:00Z' : '0001-01-01T00:00:00Z', dismissed: false, acknowledgedBy: '', acknowledgedAt: '' };
  const writes: { method: string; path: string; match: string | null; body: Record<string, unknown> }[] = [];
  const permissions = Object.keys(operationContracts).filter(name => !options.reader || name.startsWith('Get') || name.startsWith('List'));
  const reply = (body: unknown, status = 200, headers: HeadersInit = {}) => new Response(JSON.stringify(body), { status, headers });
  const fetcher = vi.fn<typeof fetch>().mockImplementation(async (input, init) => {
    const path = new URL(String(input)).pathname;
    const method = init?.method ?? 'GET';
    if (path === '/api/v2/discovery') return reply({ apiVersions: ['cpra.io/v2'], resources: [], patchTypes: ['application/merge-patch+json'], drivers: { check: ['tcp'], recovery: [], notification: [] } });
    if (path === '/api/v2/self') return reply({ principalId: 'alice', role: options.reader ? 'reader' : 'operator', permissions });
    if (method === 'GET') {
      if (path === '/api/v2/monitors/checkout') return reply(monitor);
      if (path === '/api/v2/incidents/incident-1') return reply(incident);
      if (path === '/api/v2/operations/control-operation') return reply({ id: 'control-operation', state: 'committed', contentDigest: '0'.repeat(64), committed: 1, applied: 0, validated: true });
      return reply({}, 404);
    }
    const body = JSON.parse(String(init?.body)) as Record<string, unknown>;
    writes.push({ method, path, match: new Headers(init?.headers).get('If-Match'), body });
    if (options.lost) throw new TypeError('private lost transport details');
    if (options.lostBody) return new Response(new ReadableStream({ start(controller) { controller.error(new Error('interrupted body')); } }), { status: 200, headers: { 'X-Operation-ID': 'control-operation' } });
    if (options.conflict) return reply({ code: 'conflict' }, 412);
    if (path.endsWith('/acknowledge')) { incident.acknowledgedBy = 'alice'; incident.acknowledgedAt = '2026-09-14T00:00:00Z'; }
    if (path.endsWith('/dismiss')) incident.dismissed = true;
    if (path.endsWith('/reopen')) incident.dismissed = false;
    if (method === 'PATCH') monitor.spec.enabled = (body.spec as { enabled: boolean }).enabled;
    return reply(path.includes('/incidents/') ? incident : path.endsWith('/checkout') ? monitor : { id: 'control-operation', state: 'committed', contentDigest: '0'.repeat(64), committed: 1, applied: 0, validated: true }, 200, { 'X-Operation-ID': 'control-operation' });
  });
  const session = new DashboardSession({ origin: 'https://cpra.example', fetch: fetcher });
  await session.discover(); await session.signIn('test-token');
  render(<MemoryRouter><SessionBoundary session={session}><MonitorControls monitorID="checkout" /></SessionBoundary></MemoryRouter>);
  await screen.findByText(options.disabled ? /^Disabled/ : /^Enabled/);
  return { session, monitor, incident, writes, fetcher };
}

async function openDialog(action: string) {
  fireEvent.click(await screen.findByRole('button', { name: action }));
  return screen.findByRole('dialog', { name: action });
}

it('acknowledges the exact frozen incident version and renders the server-attributed actor', async () => {
  const { writes, incident } = await controls();
  const dialog = await openDialog('Acknowledge');
  fireEvent.change(within(dialog).getByLabelText('Note (optional)'), { target: { value: 'Investigating the checkout deployment' } });
  incident.revision = 'attention-newer';
  fireEvent.click(within(dialog).getByRole('button', { name: 'Acknowledge' }));
  await waitFor(() => expect(writes).toHaveLength(1));
  expect(writes[0]).toEqual({ method: 'POST', path: '/api/v2/incidents/incident-1/acknowledge', match: '"attention-1"', body: { revision: 'attention-1', incidentID: 'incident-1', note: 'Investigating the checkout deployment' } });
  expect(writes[0].body).not.toHaveProperty('actor');
  expect(await screen.findByText(/Acknowledged by alice/)).toBeInTheDocument();
  expect(await screen.findByText(/Saved durably/)).toBeInTheDocument();
  expect(screen.queryByRole('button', { name: /check now/i })).not.toBeInTheDocument();
});

it('requires a reason for dismissal and reopens only the same active incident', async () => {
  const { writes } = await controls();
  const dialog = await openDialog('Dismiss');
  const submit = within(dialog).getByRole('button', { name: 'Dismiss' });
  expect(submit).toBeDisabled();
  fireEvent.change(within(dialog).getByLabelText('Reason'), { target: { value: 'Known development outage' } });
  fireEvent.click(submit);
  await waitFor(() => expect(writes).toHaveLength(1));
  expect(writes[0]).toMatchObject({ path: '/api/v2/incidents/incident-1/dismiss', match: '"attention-1"', body: { incidentID: 'incident-1', reason: 'Known development outage' } });
  const reopen = await openDialog('Reopen notifications');
  fireEvent.click(within(reopen).getByRole('button', { name: 'Reopen notifications' }));
  await waitFor(() => expect(writes).toHaveLength(2));
  expect(writes[1].path).toBe('/api/v2/incidents/incident-1/reopen');
  expect(writes[1].body.incidentID).toBe('incident-1');
});

it('uses the control version for a bounded snooze without editing enabled configuration', async () => {
  const { writes } = await controls();
  const dialog = await openDialog('Snooze');
  fireEvent.change(within(dialog).getByLabelText('Reason'), { target: { value: 'Scheduled service maintenance' } });
  const duration = within(dialog).getByRole('textbox', { name: /^Duration/ });
  const submit = within(dialog).getByRole('button', { name: 'Snooze' });
  for (const invalid of ['0s', '-1h', '721h', '1d', 'Infinityh', '']) {
    fireEvent.change(duration, { target: { value: invalid } });
    expect(submit).toBeDisabled();
  }
  fireEvent.change(duration, { target: { value: '1h30m' } });
  fireEvent.click(submit);
  await waitFor(() => expect(writes).toHaveLength(1));
  expect(writes[0]).toEqual({ method: 'POST', path: '/api/v2/monitors/checkout/snooze', match: '"control-1"', body: { revision: 'control-1', reason: 'Scheduled service maintenance', duration: '1h30m' } });
});

it('ends a snooze without enabling a disabled monitor', async () => {
  const { writes } = await controls({ snoozed: true, disabled: true });
  const dialog = await openDialog('End snooze');
  fireEvent.click(within(dialog).getByRole('button', { name: 'End snooze' }));
  await waitFor(() => expect(writes).toHaveLength(1));
  expect(writes[0]).toEqual({ method: 'POST', path: '/api/v2/monitors/checkout/unsnooze', match: '"control-1"', body: { revision: 'control-1' } });
  expect(screen.getByText(/^Disabled/)).toBeInTheDocument();
});

it.each([false, true])('conditionally patches enabled when disabled is %s', async disabled => {
  const { writes } = await controls({ disabled });
  const label = disabled ? 'Enable' : 'Disable';
  const dialog = await openDialog(label);
  fireEvent.click(within(dialog).getByRole('button', { name: label }));
  await waitFor(() => expect(writes).toHaveLength(1));
  expect(writes[0]).toEqual({ method: 'PATCH', path: '/api/v2/monitors/checkout', match: '"rv-1"', body: { spec: { enabled: disabled } } });
});

it('keeps a conflicting draft and does not adopt a newer version or retry it', async () => {
  const { writes } = await controls({ conflict: true });
  const dialog = await openDialog('Dismiss');
  fireEvent.change(within(dialog).getByLabelText('Reason'), { target: { value: 'Checked with the owner' } });
  fireEvent.click(within(dialog).getByRole('button', { name: 'Dismiss' }));
  await screen.findByRole('alert');
  expect(writes).toHaveLength(1);
  expect(writes[0].match).toBe('"attention-1"');
  expect(within(dialog).getByLabelText('Reason')).toHaveValue('Checked with the owner');
});

it('does not allow resubmission after an uncertain response', async () => {
  const { writes } = await controls({ lost: true });
  const dialog = await openDialog('Acknowledge');
  fireEvent.click(within(dialog).getByRole('button', { name: 'Acknowledge' }));
  expect(await screen.findByRole('alert')).toHaveTextContent(/Inspect the operation or current state/);
  expect(within(dialog).getByRole('button', { name: 'Acknowledge' })).toBeDisabled();
  fireEvent.click(within(dialog).getByRole('button', { name: 'Cancel' }));
  expect(screen.getByRole('button', { name: 'Acknowledge' })).toBeDisabled();
  expect(writes).toHaveLength(1);
});

it('hides mutations for readers while retaining incident context', async () => {
  const { writes } = await controls({ reader: true });
  expect(screen.queryByRole('button', { name: /acknowledge|dismiss|snooze|disable|enable|reopen/i })).not.toBeInTheDocument();
  expect(writes).toHaveLength(0);
});

it('links the original operation when its response body is lost', async () => {
  const { writes } = await controls({ lostBody: true });
  const dialog = await openDialog('Acknowledge');
  fireEvent.click(within(dialog).getByRole('button', { name: 'Acknowledge' }));
  expect(await screen.findByRole('link', { name: 'Inspect submitted operation' })).toHaveAttribute('href', '/operations/control-operation');
  expect(writes).toHaveLength(1);
});

it('bounds multilingual notes by encoded bytes before submitting', async () => {
  const { writes } = await controls();
  const dialog = await openDialog('Acknowledge');
  fireEvent.change(within(dialog).getByLabelText('Note (optional)'), { target: { value: 'م'.repeat(2049) } });
  expect(within(dialog).getByRole('alert')).toHaveTextContent('4,096 UTF-8 bytes');
  expect(within(dialog).getByRole('button', { name: 'Acknowledge' })).toBeDisabled();
  expect(writes).toHaveLength(0);
});

it('does not offer attention changes for a closed incident', async () => {
  await controls({ closed: true });
  expect(screen.queryByRole('button', { name: /acknowledge|dismiss|reopen/i })).not.toBeInTheDocument();
});

it('discards attention drafts on sign-out', async () => {
  const { session, writes } = await controls();
  const dialog = await openDialog('Acknowledge');
  fireEvent.change(within(dialog).getByLabelText('Note (optional)'), { target: { value: 'Only this operator can see this draft' } });
  act(() => session.signOut());
  await screen.findByRole('form', { name: 'Dashboard sign-in' });
  expect(screen.queryByRole('dialog')).not.toBeInTheDocument();
  expect(document.body.textContent).not.toContain('Only this operator can see this draft');
  expect(writes).toHaveLength(0);
});

it('requests guarded recovery with a reason and exact frozen control version', async () => {
  const { writes, monitor } = await controls({ recovery: true });
  const dialog = await openDialog('Request recovery');
  const submit = within(dialog).getByRole('button', { name: 'Request recovery' });
  expect(submit).toBeDisabled();
  expect(within(dialog).getByText(/can change the monitored service/)).toBeInTheDocument();
  fireEvent.change(within(dialog).getByLabelText('Reason'), { target: { value: 'Operator requested the configured recovery after inspection' } });
  monitor.status!.controlRevision = 'changed-control';
  fireEvent.click(submit);
  await waitFor(() => expect(writes).toHaveLength(1));
  expect(writes[0]).toEqual({ method: 'POST', path: '/api/v2/monitors/checkout/recover', match: '"control-1"', body: { revision: 'control-1', reason: 'Operator requested the configured recovery after inspection' } });
  expect(screen.queryByRole('button', { name: /force|check now/i })).not.toBeInTheDocument();
});

it.each([{ disabled: true }, { snoozed: true }, { health: 'healthy' }, { health: 'unknown' }])('does not offer an active recovery request for ineligible visible state %j', async option => {
  const { writes } = await controls({ recovery: true, ...option });
  expect(screen.getByRole('button', { name: 'Request recovery' })).toBeDisabled();
  expect(writes).toHaveLength(0);
});

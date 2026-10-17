import { afterEach, expect, it, vi } from 'vitest';
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { DashboardSession } from '../src/api/session';
import { SessionBoundary } from '../src/auth/SessionBoundary';
import { notificationSetupDocuments, NotificationSetup } from '../src/components/NotificationSetup';
import { operationContracts, type Monitor } from '../src/api/generated';

const monitor: Monitor = { apiVersion: 'cpra.io/v2', kind: 'Monitor', metadata: { id: 'api', uid: 'instance', resourceVersion: 'version-1', name: 'API' },
  spec: { enabled: false, check: { interval: '60s', timeout: '5s', driver: { type: 'http', config: { url: 'https://example.invalid' } } } } };
afterEach(() => { cleanup(); vi.restoreAllMocks(); });

it.each([{ url: 'https://hooks.example/private-marker' }, { credential: 'existing-secret' }, { endpoint: 'existing-endpoint' }])('binds a complete notification graph to the observed monitor version: %j', destination => {
  const before = JSON.stringify(monitor);
  const docs = notificationSetupDocuments(monitor, 'On-call', 'new', destination) as { kind: string; metadata: Monitor['metadata']; spec: Record<string, unknown> }[];
  const changed = docs.at(-1)!;
  expect(changed.metadata.resourceVersion).toBe('version-1');
  expect(changed.metadata.uid).toBe('instance');
  expect(changed.spec.enabled).toBe(false);
  expect(changed.spec.notifications).toEqual({ red: { notifyType: 'webhook', groupRef: 'new-group' }, green: { notifyType: 'webhook', groupRef: 'new-group' } });
  expect(docs.find(item => item.kind === 'NotificationGroup')?.spec).toEqual({ recipientRefs: ['new-recipient'] });
  expect(docs.find(item => item.kind === 'Recipient')?.spec).toEqual({ endpointRefs: ['endpoint' in destination ? destination.endpoint : 'new-endpoint'] });
  expect(docs.some(item => item.kind === 'Credential')).toBe('url' in destination);
  expect(docs.some(item => item.kind === 'NotificationEndpoint')).toBe(!('endpoint' in destination));
  expect(JSON.stringify(monitor)).toBe(before);
});

it('refuses an unversioned monitor and refuses to replace existing notification rules', () => {
  expect(() => notificationSetupDocuments({ ...monitor, metadata: { id: 'api' } }, 'name', 'id', { endpoint: 'endpoint' })).toThrow(/version/);
  expect(() => notificationSetupDocuments({ ...monitor, spec: { ...monitor.spec, notifications: { red: { notifyType: 'log' } } } }, 'name', 'id', { endpoint: 'endpoint' })).toThrow(/already has/);
});

const response = (value: unknown, status = 200) => new Response(JSON.stringify(value), { status });
async function setup() {
  const fetcher = vi.fn<typeof fetch>(async (input, init = {}) => {
    expect(init.method ?? 'GET').toBe('GET');
    const path = new URL(String(input)).pathname;
    if (path.endsWith('/access')) return response({ mode: 'management', authenticationRequired: true });
    if (path.endsWith('/self')) return response({ principalId: 'operator', role: 'operator', permissions: Object.keys(operationContracts) });
    if (path === '/api/v2/monitors/api') return response(monitor);
    if (path === '/api/v2/monitors') return response({ items: [monitor] });
    return response({ items: [] });
  });
  const session = new DashboardSession({ origin: 'https://cpra.example', fetch: fetcher });
  await session.discover(); await session.signIn('fixture-token-012345678901234567890123456789');
  const prepared = vi.fn(async (_files: File[]) => true), dirty = vi.fn();
  render(<SessionBoundary session={session}><NotificationSetup disabled={false} onPrepare={prepared} onDirty={dirty} onStart={() => true} onFinish={() => undefined} /></SessionBoundary>);
  fireEvent.click(screen.getByText('Set up webhook notifications'));
  await screen.findByRole('option', { name: 'API (api)' });
  fireEvent.change(screen.getByLabelText('Choose monitor'), { target: { value: 'api' } });
  fireEvent.change(screen.getByLabelText('Recipient name'), { target: { value: 'On-call' } });
  return { prepared, session, fetcher };
}

it('prepares private input without submitting a mutation or persisting the destination in browser storage', async () => {
  const storage = vi.spyOn(Storage.prototype, 'setItem');
  const { prepared } = await setup();
  fireEvent.change(screen.getByLabelText('Webhook URL'), { target: { value: 'https://hooks.example/private-marker' } });
  fireEvent.click(screen.getByRole('button', { name: 'Prepare notification setup' }));
  await waitFor(() => expect(prepared).toHaveBeenCalledOnce());
  const file = prepared.mock.calls[0][0][0];
  const raw = await new Promise<string>((resolve, reject) => { const reader = new FileReader(); reader.onload = () => resolve(String(reader.result)); reader.onerror = reject; reader.readAsText(file); });
  expect(JSON.parse(raw)).toHaveLength(5);
  expect(raw).toContain('private-marker');
  expect(screen.getByLabelText('Webhook URL')).toHaveValue('');
  expect(document.body.innerHTML).not.toContain('private-marker');
  expect(document.body.textContent).not.toContain('private-marker');
  expect(storage).not.toHaveBeenCalled();
});

it('clears a protected draft when the session ends', async () => {
  const { session, prepared } = await setup();
  fireEvent.change(screen.getByLabelText('Webhook URL'), { target: { value: 'https://hooks.example/private-marker' } });
  session.signOut();
  await waitFor(() => expect(document.body.innerHTML).not.toContain('private-marker'));
  expect(prepared).not.toHaveBeenCalled();
});

it('retains its draft when the shared preparation path cannot adopt it', async () => {
  const { prepared } = await setup();
  prepared.mockResolvedValueOnce(false);
  fireEvent.change(screen.getByLabelText('Webhook URL'), { target: { value: 'https://hooks.example/private-marker' } });
  fireEvent.click(screen.getByRole('button', { name: 'Prepare notification setup' }));
  expect(await screen.findByRole('alert')).toHaveTextContent('Your draft is retained');
  expect(screen.getByLabelText('Webhook URL')).toHaveValue('https://hooks.example/private-marker');
  expect(screen.getByLabelText('Recipient name')).toHaveValue('On-call');
});

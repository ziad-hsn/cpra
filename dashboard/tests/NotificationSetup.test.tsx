import { afterEach, expect, it, vi } from 'vitest';
import { act, cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
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

it('reuses an existing recipient without replacing its endpoints and preserves other colors', () => {
  const existing = {...monitor, spec: {...monitor.spec, notifications: {red: {notifyType: 'log', dispatch: false}, blue: {notifyType: 'webhook', groupRef: 'review-team'}}}} as Monitor;
  const docs = notificationSetupDocuments(existing, 'Existing on-call', 'new', {recipient: 'oncall'}, true) as {kind: string; spec: Record<string, unknown>}[];
  expect(docs.map(doc => doc.kind)).toEqual(['NotificationGroup', 'Monitor']);
  expect(docs[0].spec).toEqual({recipientRefs: ['oncall']});
  expect((docs[1].spec.notifications as Record<string, unknown>).blue).toEqual(existing.spec.notifications!.blue);
});

const response = (value: unknown, status = 200) => new Response(JSON.stringify(value), { status });
async function setup(existing = monitor, recipientType?: 'email' | 'webhook') {
  const fetcher = vi.fn<typeof fetch>(async (input, init = {}) => {
    expect(init.method ?? 'GET').toBe('GET');
    const path = new URL(String(input)).pathname;
    if (path.endsWith('/access')) return response({ mode: 'management', authenticationRequired: true });
    if (path.endsWith('/self')) return response({ principalId: 'operator', role: 'operator', permissions: Object.keys(operationContracts) });
    if (path === '/api/v2/monitors/api') return response(existing);
    if (path === '/api/v2/monitors') return response({ items: [existing] });
    if (path === '/api/v2/recipients/oncall') return response({apiVersion: 'cpra.io/v2', kind: 'Recipient', metadata: {id: 'oncall', uid: 'recipient', resourceVersion: 'r1'}, spec: {endpointRefs: ['destination']}});
    if (path === '/api/v2/notification-endpoints/destination') return response({apiVersion: 'cpra.io/v2', kind: 'NotificationEndpoint', metadata: {id: 'destination', uid: 'endpoint', resourceVersion: 'e1'}, spec: {type: recipientType, config: {}}});
    return response({ items: [] });
  });
  const session = new DashboardSession({ origin: 'https://cpra.example', fetch: fetcher });
  await session.discover(); await session.signIn('fixture-token-012345678901234567890123456789');
  const prepared = vi.fn(async (_files: File[]) => true), dirty = vi.fn();
  render(<SessionBoundary session={session}><NotificationSetup initialRecipient={recipientType ? 'oncall' : ''} disabled={false} onPrepare={prepared} onDirty={dirty} onStart={() => true} onFinish={() => undefined} /></SessionBoundary>);
  if (!recipientType) fireEvent.click(screen.getByText('Set up webhook notifications'));
  await screen.findByRole('option', { name: 'API (api)' });
  fireEvent.change(screen.getByLabelText('Choose monitor'), { target: { value: 'api' } });
  fireEvent.change(screen.getByLabelText(recipientType ? 'Notification group name' : 'Recipient name'), { target: { value: 'On-call' } });
  return { prepared, session, fetcher };
}

it.each(['email', 'webhook'] as const)('validates the channel of an existing %s recipient before preparation', async type => {
  const {prepared} = await setup(monitor, type);
  fireEvent.click(screen.getByRole('button', {name: 'Prepare notification setup'}));
  if (type === 'email') {
    expect(await screen.findByRole('alert')).toHaveTextContent('no webhook endpoint');
    expect(screen.getByRole('alert')).toHaveFocus();
    expect(prepared).not.toHaveBeenCalled();
  } else await waitFor(() => expect(prepared).toHaveBeenCalledOnce());
});

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

it('reviews existing rules and freezes the original version without fetching over a conflict', async () => {
  const existing = {...monitor, spec: {...monitor.spec, notifications: {red: {notifyType: 'webhook', groupRef: 'old-group', dispatch: false}, blue: {notifyType: 'webhook', groupRef: 'review-team'}}}} as Monitor;
  const { prepared, fetcher } = await setup(existing);
  fireEvent.change(screen.getByLabelText('Webhook URL'), {target: {value: 'https://hooks.example/private-review-marker'}});
  fireEvent.click(screen.getByRole('button', {name: 'Prepare notification setup'}));
  const review = await screen.findByLabelText('Review notification rule changes');
  expect(review).toHaveFocus();
  expect(review).toHaveTextContent('Disabled');
  expect(review).toHaveTextContent('old-group');
  expect(review).toHaveTextContent('version-1');
  expect(review).not.toHaveTextContent('private-review-marker');
  expect(prepared).not.toHaveBeenCalled();
  const readsBefore = fetcher.mock.calls.filter(([url]) => String(url).endsWith('/monitors/api')).length;
  fireEvent.click(screen.getByRole('button', {name: 'Prepare reviewed collection'}));
  await waitFor(() => expect(prepared).toHaveBeenCalledOnce());
  expect(fetcher.mock.calls.filter(([url]) => String(url).endsWith('/monitors/api'))).toHaveLength(readsBefore);
  const file = prepared.mock.calls[0][0][0];
  const raw = await new Promise<string>(resolve => { const reader = new FileReader(); reader.onload = () => resolve(String(reader.result)); reader.readAsText(file); });
  const result = JSON.parse(raw).at(-1);
  expect(result.metadata).toMatchObject({uid: 'instance', resourceVersion: 'version-1'});
  expect(result.spec.notifications.blue).toEqual(existing.spec.notifications!.blue);
  expect(result.spec.notifications.red.groupRef).not.toBe('old-group');
  expect(document.body.innerHTML).not.toContain('private-review-marker');
});

it('clears a protected draft when the session ends', async () => {
  const { session, prepared } = await setup();
  fireEvent.change(screen.getByLabelText('Webhook URL'), { target: { value: 'https://hooks.example/private-marker' } });
  act(() => session.signOut());
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

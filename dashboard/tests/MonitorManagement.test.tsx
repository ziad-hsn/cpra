import { afterEach, expect, it, vi } from 'vitest';
import { act, cleanup, fireEvent, screen, waitFor, within } from '@testing-library/react';
import { checkFields, recoveryFields, notificationFields, type Capabilities } from '../src/api/generated';
import { fieldsFor, resourceView, type DriverCategory } from '../src/api/resources';
import { validateMonitorDraft } from '../src/components/MonitorFields';
import { fixture, resource } from './managementFixture';

afterEach(() => { cleanup(); vi.restoreAllMocks(); });

const capabilities: Capabilities = { apiVersions: ['cpra.io/v2'], drivers: { check: Object.keys(checkFields), recovery: Object.keys(recoveryFields), notification: Object.keys(notificationFields) }, resources: [], patchTypes: [] };
const basic = () => ({ check: { driver: { type: 'tcp', config: { host: 'localhost', port: 443 } }, interval: '60s', timeout: '5s' } });
function driverSpec(driver: string, category: DriverCategory) {
  const config: Record<string, unknown> = {};
  const credentialRefs: Record<string, string> = {};
  for (const field of fieldsFor(driver, category) ?? []) {
    if (field.protected) credentialRefs[field.key] = `secret-${field.key}`;
    else config[field.key] = field.type === 'boolean' ? false : field.type === 'number' ? 0 : field.type.endsWith('-list') ? [] : field.key === 'url' ? 'https://service.example.test/health' : '';
  }
  return { type: driver, config, credentialRefs };
}

it('creates a monitor with guided timing, labels, enabled state and typed Code destinations', async () => {
  const { writes } = await fixture('monitors', 'new');
  const form = await screen.findByRole('form', { name: 'Create monitor' });
  fireEvent.change(screen.getByLabelText('Stable ID'), { target: { value: 'checkout-api' } });
  await screen.findByRole('option', { name: 'tcp' });
  fireEvent.change(screen.getByLabelText('Check driver'), { target: { value: 'tcp' } });
  fireEvent.change(screen.getByLabelText('Host'), { target: { value: 'checkout.default.svc' } });
  fireEvent.change(screen.getByLabelText('Port'), { target: { value: '443' } });
  fireEvent.change(screen.getByLabelText('Enabled'), { target: { value: 'false' } });
  fireEvent.change(screen.getByLabelText('Check retries'), { target: { value: '0' } });
  fireEvent.change(screen.getByLabelText('New label key'), { target: { value: 'team' } });
  fireEvent.change(screen.getByLabelText('New label value'), { target: { value: 'commerce' } });
  fireEvent.click(screen.getByRole('button', { name: 'Add label' }));
  fireEvent.change(screen.getByLabelText('Add Code'), { target: { value: 'red' } });
  fireEvent.change(screen.getByLabelText('Code red notification type'), { target: { value: 'telegram' } });
  fireEvent.change(screen.getByLabelText('Code red dispatch'), { target: { value: 'false' } });
  fireEvent.change(screen.getByLabelText('Specific code red recipients ID'), { target: { value: 'primary-oncall' } });
  fireEvent.click(within(screen.getByRole('group', { name: 'Code red recipients' })).getByRole('button', { name: 'Use ID' }));
  fireEvent.change(screen.getByLabelText('Specific code red group ID'), { target: { value: 'commerce-team' } });
  fireEvent.click(within(screen.getByRole('group', { name: 'Code red group' })).getByRole('button', { name: 'Use ID' }));
  fireEvent.submit(form);
  await waitFor(() => expect(writes).toHaveLength(1));
  expect(writes[0]).toMatchObject({ method: 'POST', path: '/api/v2/monitors', match: null, body: {
    apiVersion: 'cpra.io/v2', kind: 'Monitor', metadata: { id: 'checkout-api', labels: { team: 'commerce' } },
    spec: { enabled: false, check: { driver: { type: 'tcp', config: { host: 'checkout.default.svc', port: 443 } }, interval: '60s', timeout: '5s', retries: 0 },
      notifications: { red: { notifyType: 'telegram', dispatch: false, recipientRefs: ['primary-oncall'], groupRef: 'commerce-team' } } },
  } });
  const text = await screen.findByText(/Saved durably\./);
  expect(text).toHaveTextContent('Controller application has not yet been confirmed');
});

it.each(Object.keys(checkFields))('preserves the complete %s check document across a conditional edit', async driver => {
  const spec = { ...basic(), enabled: false, tags: [], check: { ...basic().check, driver: driverSpec(driver, 'check'), unhealthyThreshold: 0, healthyThreshold: 0, retries: 0, maxFailures: 0, groups: [] },
    notifications: { red: { notifyType: 'telegram', recipientRefs: ['oncall'], dispatch: false }, green: { notifyType: 'email', groupRef: 'email-team' } },
    notificationGroupRefs: [], maintenance: [{ start: '2026-10-01T02:00:00Z', end: '2026-10-01T03:00:00Z' }, { cron: '0 2 * * 1', duration: '30m', timezone: 'UTC' }] };
  const original = resource('monitors', `check-${driver}`, spec);
  Object.assign(original.metadata, { labels: { team: 'platform', empty: '' }, generation: 8 });
  original.status = { observedGeneration: 7, lastCheckLatencyMs: { available: true, value: 12.5 } };
  const { writes } = await fixture('monitors', original.metadata.id, { initial: [original] });
  fireEvent.click(await screen.findByRole('button', { name: 'Edit monitor' }));
  await waitFor(() => expect(screen.getByLabelText('Check driver')).not.toBeDisabled());
  fireEvent.change(screen.getByLabelText('Name'), { target: { value: 'New display name' } });
  fireEvent.click(screen.getByRole('button', { name: 'Save' }));
  await waitFor(() => expect(writes).toHaveLength(1));
  expect(writes[0]).toMatchObject({ method: 'PUT', match: '"rv-1"', body: { metadata: { id: original.metadata.id, uid: original.metadata.uid, labels: { team: 'platform', empty: '' } } } });
  expect(writes[0].body?.spec).toEqual(spec);
  expect(writes[0].body).not.toHaveProperty('status');
});

it.each(Object.keys(recoveryFields))('preserves the complete %s recovery document and legacy Code behavior', async driver => {
  const spec = { ...basic(), recovery: { driver: driverSpec(driver, 'recovery'), maxAttempts: 0, cooldown: '10m', retries: 0, maxFailures: 0 },
    notifications: { yellow: { groupRef: 'legacy-team', driver: { type: 'log', config: { file: '' } } }, cyan: { endpointRefs: ['ops-log'], dispatch: false } } };
  const original = resource('monitors', `recovery-${driver}`, spec);
  const { writes } = await fixture('monitors', original.metadata.id, { initial: [original] });
  fireEvent.click(await screen.findByRole('button', { name: 'Edit monitor' }));
  await waitFor(() => expect(screen.getByLabelText('Recovery driver')).not.toBeDisabled());
  fireEvent.change(screen.getByLabelText('Name'), { target: { value: 'Edited recovery monitor' } });
  fireEvent.click(screen.getByRole('button', { name: 'Save' }));
  await waitFor(() => expect(writes).toHaveLength(1));
  expect(writes[0].body?.spec).toEqual(spec);
  expect((writes[0].body?.spec as typeof spec).notifications.yellow).not.toHaveProperty('notifyType');
  expect((writes[0].body?.spec as typeof spec).notifications.yellow).not.toHaveProperty('dispatch');
});

it('loads a full monitor detail before editing rather than replacing from a list summary', async () => {
  const original = resource('monitors', 'full-detail', { ...basic(), enabled: false, tags: ['preserve'], recovery: { driver: { type: 'systemd', config: { unit: 'dedicated-test.service' } } } });
  const { writes, fetcher } = await fixture('monitors', undefined, { initial: [original], summaries: true });
  fireEvent.click(await screen.findByRole('link', { name: 'full-detail' }));
  fireEvent.click(await screen.findByRole('button', { name: 'Edit monitor' }));
  expect(fetcher.mock.calls.some(([url]) => String(url).endsWith('/api/v2/monitors/full-detail'))).toBe(true);
  await waitFor(() => expect(screen.getByLabelText('Check driver')).not.toBeDisabled());
  fireEvent.click(screen.getByRole('button', { name: 'Save' }));
  await waitFor(() => expect(writes).toHaveLength(1));
  expect(writes[0].body?.spec).toEqual(original.spec);
});

it('keeps drafts frozen and shows delete/recreate conflicts without retrying or adopting a new incarnation', async () => {
  const original = resource('monitors', 'same-name', basic());
  const { writes, records } = await fixture('monitors', original.metadata.id, { initial: [original] });
  fireEvent.click(await screen.findByRole('button', { name: 'Edit monitor' }));
  await waitFor(() => expect(screen.getByLabelText('Check driver')).not.toBeDisabled());
  fireEvent.change(screen.getByLabelText('Check interval'), { target: { value: '90s' } });
  records.set('Monitor:same-name', { ...original, metadata: { ...original.metadata, name: 'Replacement', uid: 'new-incarnation', resourceVersion: 'rv-2' } });
  fireEvent.click(screen.getByRole('button', { name: 'Save' }));
  fireEvent.click(await screen.findByRole('button', { name: 'Compare latest resource' }));
  expect(await screen.findByText(/This is a different incarnation/)).toBeInTheDocument();
  expect(screen.getByLabelText('Check interval')).toHaveValue('90s');
  expect(writes).toHaveLength(1);
  expect(writes[0]).toMatchObject({ match: '"rv-1"', body: { metadata: { uid: original.metadata.uid } } });
});

it('shows measurement unavailability and hides every mutation control from a reader', async () => {
  const original = resource('monitors', 'observe-only', basic());
  original.status = { lastCheckLatencyMs: { available: false, value: 0 }, observedGeneration: 1 };
  const { writes } = await fixture('monitors', original.metadata.id, { reader: true, initial: [original] });
  expect(await screen.findByText('Last-check latency: Unavailable')).toBeInTheDocument();
  expect(screen.queryByRole('button', { name: /^Edit|^Delete|^Save|^Add|^Remove|^Use/ })).not.toBeInTheDocument();
  expect(screen.queryByRole('button', { name: /check now|recovery|snooze|dismiss|acknowledge/i })).not.toBeInTheDocument();
  expect(writes).toHaveLength(0);
});

it('does not grant create or edit from an operator role without the exact operation permission', async () => {
  const { writes } = await fixture('monitors', 'restricted', { initial: [resource('monitors', 'restricted', basic())], permissions: ['GetMonitor', 'ListMonitors'] });
  await screen.findByRole('heading', { name: 'restricted' });
  expect(screen.queryByRole('button', { name: /^Edit|^Delete/ })).not.toBeInTheDocument();
  expect(writes).toHaveLength(0);
});

it('refuses a compiled capability mismatch and unsupported fields without rewriting configuration', async () => {
  const original = resource('monitors', 'unavailable-driver', basic());
  const { writes } = await fixture('monitors', original.metadata.id, { initial: [original], drivers: { check: ['http'], notification: [], recovery: [] } });
  fireEvent.click(await screen.findByRole('button', { name: 'Edit monitor' }));
  await waitFor(() => expect(screen.getByLabelText('Check driver')).not.toBeDisabled());
  fireEvent.click(screen.getByRole('button', { name: 'Save' }));
  expect(await screen.findByText('Select a check driver compiled into this server.')).toBeInTheDocument();
  expect(writes).toHaveLength(0);
  const unsupported = resourceView(resource('monitors', 'future', { ...basic(), futureBehavior: { secret: 'must-not-cache' } }), 'monitors');
  expect(unsupported.unsupported).toContain('cannot edit safely');
  expect(JSON.stringify(unsupported)).not.toContain('must-not-cache');
});

it('removes protected inline values before query caching and preserves optional secret references', () => {
  const source = resource('monitors', 'old-credentials', { ...basic(), check: { ...basic().check, driver: { type: 'http', config: { url: 'https://example.test/health?token=private-url-token', headers: { Authorization: 'private-header' }, body: 'private-body', expectedStatus: [200] } } }, recovery: { driver: { type: 'webhook', config: { url: 'private-recovery-url', body: 'private-recovery-body' } } } });
  const view = resourceView(source, 'monitors');
  expect(view.unsupported).toContain('inline credentials');
  expect(JSON.stringify(view)).not.toMatch(/private-/);
  const referenced = resource('monitors', 'refs', { ...basic(), check: { ...basic().check, driver: { type: 'http', config: { expectedStatus: [200] }, credentialRefs: { url: 'http-url', headers: 'http-headers', method: 'method-override' } } } });
  const safe = resourceView(referenced, 'monitors');
  expect(safe.unsupported).toBeUndefined();
  expect(safe.resource.spec).toEqual(referenced.spec);
});

it('edits expected status codes and safe URL references without a raw configuration editor', async () => {
  const original = resource('monitors', 'http-status', { ...basic(), check: { ...basic().check, driver: { type: 'http', config: { expectedStatus: [200], method: 'GET' }, credentialRefs: { url: 'http-url' } } } });
  const { writes } = await fixture('monitors', original.metadata.id, { initial: [original] });
  fireEvent.click(await screen.findByRole('button', { name: 'Edit monitor' }));
  await waitFor(() => expect(screen.getByLabelText('Check driver')).not.toBeDisabled());
  expect(screen.getByRole('group', { name: 'Url secret' })).toHaveTextContent('http-url');
  fireEvent.change(screen.getByLabelText('Expected status'), { target: { value: '200\n204' } });
  fireEvent.click(screen.getByRole('button', { name: 'Save' }));
  await waitFor(() => expect(writes).toHaveLength(1));
  expect(writes[0].body?.spec).toMatchObject({ check: { driver: { config: { expectedStatus: [200, 204], method: 'GET' }, credentialRefs: { url: 'http-url' } } } });
});

it('supports a stable monitor ID literally named new without treating it as a create route', async () => {
  const { router } = await fixture('monitors', undefined, { initial: [resource('monitors', 'new', basic())] });
  fireEvent.click(await screen.findByRole('link', { name: 'new' }));
  expect(await screen.findByRole('button', { name: 'Edit monitor' })).toBeInTheDocument();
  expect(router.state.location.pathname).toBe('/monitor-configurations/new');
  expect(screen.queryByRole('form', { name: 'Create monitor' })).not.toBeInTheDocument();
});

it('clears monitor drafts on sign-out and keeps an uncertain mutation from being repeated', async () => {
  const { writes, session } = await fixture('monitors', 'lost', { initial: [resource('monitors', 'lost', basic())], failWrite: true });
  fireEvent.click(await screen.findByRole('button', { name: 'Edit monitor' }));
  await waitFor(() => expect(screen.getByLabelText('Check driver')).not.toBeDisabled());
  fireEvent.change(screen.getByLabelText('Name'), { target: { value: 'Private draft' } });
  fireEvent.click(screen.getByRole('button', { name: 'Save' }));
  expect(await screen.findByRole('alert')).toHaveTextContent('outcome is unconfirmed');
  expect(screen.getByRole('button', { name: 'Save' })).toBeDisabled();
  expect(writes).toHaveLength(1);
  await act(async () => { session.signOut(); });
  expect(screen.queryByDisplayValue('Private draft')).not.toBeInTheDocument();
});

it('validates duration, typed destinations and maintenance before issuing a mutation', () => {
  expect(validateMonitorDraft(basic(), capabilities)).toBeUndefined();
  expect(validateMonitorDraft({ ...basic(), check: { ...basic().check, interval: '+60s' }, maintenance: [{ cron: '0 2 * * *', duration: '30m' }] }, capabilities)).toBeUndefined();
  expect(validateMonitorDraft({ ...basic(), check: { ...basic().check, interval: '0s' } }, capabilities)).toMatch(/positive Go duration/);
  expect(validateMonitorDraft({ ...basic(), notifications: { red: { notifyType: 'telegram', recipientRefs: [] } } }, capabilities)).toMatch(/recipient or a group/);
  expect(validateMonitorDraft({ ...basic(), notifications: { red: { notifyType: 'telegram', recipientRefs: ['oncall'], endpointRefs: ['legacy'] } } }, capabilities)).toMatch(/cannot combine/);
  expect(validateMonitorDraft({ ...basic(), maintenance: [{ start: '2026-10-01T02:00:00Z', end: '2026-10-01T01:00:00Z' }] }, capabilities)).toMatch(/end after/);
});

it('rejects protected HTTP target input without placing it in a draft, request, or browser storage', async () => {
  const storage = vi.spyOn(Storage.prototype, 'setItem');
  const { writes } = await fixture('monitors', 'plain-http', { initial: [resource('monitors', 'plain-http', { ...basic(), check: { ...basic().check, driver: { type: 'http', config: { url: 'https://example.test/health' } } } })] });
  fireEvent.click(await screen.findByRole('button', { name: 'Edit monitor' }));
  await waitFor(() => expect(screen.getByLabelText('Check driver')).not.toBeDisabled());
  fireEvent.change(screen.getByLabelText('Url'), { target: { value: 'https://example.test/health?token=do-not-retain' } });
  expect(screen.getByLabelText('Url')).toHaveValue('https://example.test/health');
  expect(screen.getByLabelText('Url')).toBeInvalid();
  fireEvent.submit(screen.getByRole('form', { name: 'Edit monitor' }));
  expect(writes).toHaveLength(0);
  expect(document.body.innerHTML).not.toContain('do-not-retain');
  expect(storage).not.toHaveBeenCalled();
  fireEvent.click(screen.getByLabelText('Use a secret for url'));
  fireEvent.change(screen.getByLabelText('Specific url secret ID'), { target: { value: 'private-health-url' } });
  fireEvent.click(within(screen.getByRole('group', { name: 'Url secret' })).getByRole('button', { name: 'Use ID' }));
  fireEvent.click(screen.getByRole('button', { name: 'Save' }));
  await waitFor(() => expect(writes).toHaveLength(1));
  expect(writes[0].body?.spec).toMatchObject({ check: { driver: { config: {}, credentialRefs: { url: 'private-health-url' } } } });
  expect(JSON.stringify(writes)).not.toContain('do-not-retain');
});

it('rejects an invalid monitor identity before submitting its configuration', async () => {
  const { writes } = await fixture('monitors', 'new');
  await screen.findByRole('form', { name: 'Create monitor' });
  fireEvent.change(screen.getByLabelText('Stable ID'), { target: { value: 'team/service' } });
  await screen.findByRole('option', { name: 'tcp' });
  fireEvent.change(screen.getByLabelText('Check driver'), { target: { value: 'tcp' } });
  fireEvent.click(screen.getByRole('button', { name: 'Save' }));
  expect(await screen.findByRole('alert')).toHaveTextContent('must start with a letter or digit');
  expect(writes).toHaveLength(0);
});

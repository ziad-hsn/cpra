import { afterEach, expect, it, vi } from 'vitest';
import { act, cleanup, fireEvent, screen, waitFor, within } from '@testing-library/react';
import { fieldsFor, resourceView } from '../src/api/resources';
import { notificationFields } from '../src/api/generated';
import { fixture, resource } from './managementFixture';


afterEach(() => { cleanup(); vi.restoreAllMocks(); });

it('creates a write-only secret with the canonical envelope and clears the input without displaying the echoed value', async () => {
  const storage = vi.spyOn(Storage.prototype, 'setItem');
  const { writes, session } = await fixture('credentials', 'new');
  const form = await screen.findByRole('form', { name: 'Create secret' });
  fireEvent.change(within(form).getByLabelText('Stable ID'), { target: { value: 'notification-token' } });
  fireEvent.change(within(form).getByLabelText('Name'), { target: { value: 'Notification token' } });
  fireEvent.change(within(form).getByLabelText('New secret value'), { target: { value: 'never-render-this-value' } });
  fireEvent.submit(form);
  expect(within(form).getByLabelText('New secret value')).toHaveValue('');
  await waitFor(() => expect(writes).toHaveLength(1));
  expect(writes[0]).toMatchObject({ method: 'POST', path: '/api/v2/credentials', body: { apiVersion: 'cpra.io/v2', kind: 'Credential', metadata: { id: 'notification-token', name: 'Notification token' }, spec: { value: 'never-render-this-value' } } });
  expect(await screen.findByText(/Saved durably/)).toBeInTheDocument();
  expect(document.body.textContent).not.toContain('never-render-this-value');
  expect(storage).not.toHaveBeenCalled();
  await act(async () => { session.signOut(); });
  expect(document.body.innerHTML).not.toContain('never-render-this-value');
});

it('patches secret metadata using the frozen version without sending a replacement value', async () => {
  const { writes } = await fixture('credentials', 'existing', { initial: [resource('credentials', 'existing', { description: 'Before', value: 'server-only-value' })] });
  fireEvent.click(await screen.findByRole('button', { name: 'Edit secret' }));
  expect(screen.queryByLabelText('New secret value')).not.toBeInTheDocument();
  fireEvent.change(screen.getByLabelText('Description'), { target: { value: 'After' } });
  fireEvent.click(screen.getByRole('button', { name: 'Save' }));
  await waitFor(() => expect(writes).toHaveLength(1));
  expect(writes[0]).toMatchObject({ method: 'PATCH', match: '"rv-1"', body: { spec: { description: 'After' } } });
  expect(writes[0].body).not.toHaveProperty('metadata');
  expect(JSON.stringify(writes[0])).not.toContain('server-only-value');
  expect((writes[0].body?.spec as Record<string, unknown>).value).toBeUndefined();
});

it('represents deleted secret labels as merge-patch nulls and keeps immutable identities out of the body', async () => {
  const original = resource('credentials', 'labels', { description: 'Keep' });
  Object.assign(original.metadata, { labels: { keep: 'one', remove: 'two' } });
  const { writes } = await fixture('credentials', 'labels', { initial: [original] });
  fireEvent.click(await screen.findByRole('button', { name: 'Edit secret' }));
  fireEvent.click(screen.getByRole('button', { name: 'Remove label remove' }));
  fireEvent.change(screen.getByLabelText('Name'), { target: { value: 'New name' } });
  fireEvent.click(screen.getByRole('button', { name: 'Save' }));
  await waitFor(() => expect(writes).toHaveLength(1));
  expect(writes[0].body).toEqual({ metadata: { name: 'New name', labels: { remove: null } }, spec: { description: 'Keep' } });
  expect(writes[0].match).toBe('"rv-1"');
});

it('creates a recipient with ordered endpoint references, not login or channel fields', async () => {
  const { writes, router } = await fixture('recipients', 'new', { initial: [resource('endpoints', 'ops-mail', { type: 'email', config: { to: 'ops@example.test' }, credentialRefs: { server: 'smtp-host' } })] });
  const form = await screen.findByRole('form', { name: 'Create recipient' });
  fireEvent.change(screen.getByLabelText('Stable ID'), { target: { value: 'on-call' } });
  await screen.findByRole('option', { name: /ops-mail/ });
  fireEvent.change(screen.getByLabelText('Choose endpoint references'), { target: { value: 'ops-mail' } });
  fireEvent.submit(form);
  await waitFor(() => expect(writes).toHaveLength(1));
  expect(writes[0].body).toMatchObject({ kind: 'Recipient', spec: { endpointRefs: ['ops-mail'] } });
  expect(writes[0].body?.spec).not.toHaveProperty('role');
  expect(writes[0].body?.spec).not.toHaveProperty('notifyType');
  await waitFor(() => expect(router.state.location.pathname).toBe('/recipients/on-call'));
  expect(screen.queryByRole('alertdialog')).not.toBeInTheDocument();
});

it('creates a group with recipients and direct endpoints without nested groups', async () => {
  const { writes } = await fixture('groups', 'new');
  const form = await screen.findByRole('form', { name: 'Create notification group' });
  fireEvent.change(screen.getByLabelText('Stable ID'), { target: { value: 'platform' } });
  fireEvent.change(screen.getByLabelText('Specific endpoint references ID'), { target: { value: 'platform-log' } });
  fireEvent.click(within(screen.getByRole('group', { name: 'Endpoint references' })).getByRole('button', { name: 'Use ID' }));
  fireEvent.change(screen.getByLabelText('Specific recipient references ID'), { target: { value: 'on-call' } });
  fireEvent.click(within(screen.getByRole('group', { name: 'Recipient references' })).getByRole('button', { name: 'Use ID' }));
  fireEvent.submit(form);
  await waitFor(() => expect(writes).toHaveLength(1));
  expect(writes[0].body?.spec).toEqual({ endpointRefs: ['platform-log'], recipientRefs: ['on-call'] });
  expect(screen.queryByLabelText(/nested group/i)).not.toBeInTheDocument();
});

it('uses generated notification fields and secret references while preserving explicit false and zero', async () => {
  const { writes } = await fixture('endpoints', 'new');
  const form = await screen.findByRole('form', { name: 'Create notification endpoint' });
  fireEvent.change(screen.getByLabelText('Stable ID'), { target: { value: 'pager' } });
  await screen.findByRole('option', { name: 'pushover' });
  fireEvent.change(screen.getByLabelText('Notification driver'), { target: { value: 'pushover' } });
  fireEvent.change(screen.getByLabelText('Priority'), { target: { value: '0' } });
  fireEvent.change(screen.getByLabelText('Specific app token secret ID'), { target: { value: 'pushover-app' } });
  fireEvent.click(within(screen.getByRole('group', { name: 'App token secret' })).getByRole('button', { name: 'Use ID' }));
  fireEvent.submit(form);
  await waitFor(() => expect(writes).toHaveLength(1));
  expect(writes[0].body?.spec).toEqual({ type: 'pushover', config: { priority: 0 }, credentialRefs: { appToken: 'pushover-app' } });
  expect(Object.keys(notificationFields)).toHaveLength(14);
  expect(fieldsFor('telegram')?.find(field => field.key === 'botToken')).toMatchObject({ protected: true });
  expect(fieldsFor('email')?.find(field => field.key === 'server')).toMatchObject({ protected: true });
});

it('does not overwrite an edit draft when the server version changes, and never retries a conflict', async () => {
  const original = resource('recipients', 'person', { endpointRefs: ['ops-mail'] });
  const { writes, records } = await fixture('recipients', 'person', { initial: [original] });
  fireEvent.click(await screen.findByRole('button', { name: 'Edit recipient' }));
  fireEvent.change(screen.getByLabelText('Name'), { target: { value: 'My draft' } });
  records.set('Recipient:person', { ...original, metadata: { ...original.metadata, name: 'Other operator', resourceVersion: 'rv-2' } });
  fireEvent.click(screen.getByRole('button', { name: 'Save' }));
  expect(await screen.findByRole('button', { name: 'Compare latest resource' })).toBeInTheDocument();
  expect(screen.getByLabelText('Name')).toHaveValue('My draft');
  expect(writes).toHaveLength(1);
  expect(writes[0].match).toBe('"rv-1"');
  fireEvent.click(screen.getByRole('button', { name: 'Compare latest resource' }));
  expect(await screen.findByRole('region', { name: 'Latest resource comparison' })).toHaveTextContent('Other operator');
  expect(screen.getByLabelText('Name')).toHaveValue('My draft');
  expect(writes).toHaveLength(1);
});

it('shows a referenced deletion failure without removing the resource', async () => {
  const { writes, records } = await fixture('credentials', 'referenced', { initial: [resource('credentials', 'referenced', {})] });
  fireEvent.click(await screen.findByRole('button', { name: 'Delete secret' }));
  fireEvent.click(screen.getByRole('button', { name: 'Confirm deletion' }));
  expect(await screen.findByRole('alert')).toHaveTextContent('reference prevents');
  expect(writes[0]).toMatchObject({ method: 'DELETE', match: '"rv-1"' });
  expect(records.has('Credential:referenced')).toBe(true);
});

it('keeps reader pages observable without mutation controls', async () => {
  const { writes } = await fixture('recipients', 'person', { reader: true, initial: [resource('recipients', 'person', { endpointRefs: ['ops-mail'] })] });
  expect(await screen.findByRole('heading', { name: 'person' })).toBeInTheDocument();
  expect(screen.queryByRole('button', { name: /^Edit|^Delete|^Save/ })).not.toBeInTheDocument();
  expect(writes).toHaveLength(0);
});

it('clears submitted secrets and disables repeat submission after a lost reply', async () => {
  const { writes } = await fixture('credentials', 'new', { failWrite: true });
  const form = await screen.findByRole('form', { name: 'Create secret' });
  fireEvent.change(screen.getByLabelText('Stable ID'), { target: { value: 'new-key' } });
  fireEvent.change(screen.getByLabelText('New secret value'), { target: { value: 'ephemeral-only' } });
  fireEvent.submit(form);
  expect(await screen.findByRole('alert')).toHaveTextContent('outcome is unconfirmed');
  expect(screen.getByLabelText('New secret value')).toHaveValue('');
  expect(screen.getByRole('button', { name: 'Save' })).toBeDisabled();
  expect(writes).toHaveLength(1);
});

it('blocks navigation away from a dirty draft until the operator chooses to discard it', async () => {
  const { router } = await fixture('recipients', 'new');
  await screen.findByRole('form', { name: 'Create recipient' });
  fireEvent.change(screen.getByLabelText('Name'), { target: { value: 'Unsaved draft' } });
  await act(async () => { await router.navigate('/elsewhere'); });
  expect(screen.getByRole('alertdialog')).toHaveTextContent('Leave this editor?');
  fireEvent.click(screen.getByRole('button', { name: 'Keep editing' }));
  expect(screen.getByLabelText('Name')).toHaveValue('Unsaved draft');
  await act(async () => { await router.navigate('/elsewhere'); });
  fireEvent.click(screen.getByRole('button', { name: 'Discard draft and leave' }));
  expect(await screen.findByText('Elsewhere')).toBeInTheDocument();
});

it('removes protected legacy endpoint values before query caching and refuses destructive editing', () => {
  const view = resourceView(resource('endpoints', 'old', { type: 'telegram', config: { botToken: 'legacy-inline-token', chatId: '123', testMode: false }, credentialRefs: {} }), 'endpoints');
  expect(JSON.stringify(view)).not.toContain('legacy-inline-token');
  expect(view.unsupported).toContain('inline credentials');
  expect(view.resource.spec).toMatchObject({ config: { chatId: '123', testMode: false } });
});

it.each(Object.keys(notificationFields))('round-trips the generated %s endpoint form without erasing false, zero, empty or absent values', async driver => {
  const config: Record<string, unknown> = {};
  const credentialRefs: Record<string, string> = {};
  for (const field of fieldsFor(driver) ?? []) {
    if (field.protected) credentialRefs[field.key] = `secret-${field.key}`;
    else config[field.key] = field.type === 'boolean' ? false : field.type === 'number' ? 0 : field.type === 'string-list' ? [] : '';
  }
  const original = resource('endpoints', `endpoint-${driver}`, { type: driver, config, credentialRefs });
  const { writes } = await fixture('endpoints', original.metadata.id, { initial: [original] });
  fireEvent.click(await screen.findByRole('button', { name: 'Edit notification endpoint' }));
  await waitFor(() => expect(screen.getByLabelText('Notification driver')).not.toBeDisabled());
  fireEvent.change(screen.getByLabelText('Name'), { target: { value: 'New display name' } });
  fireEvent.click(screen.getByRole('button', { name: 'Save' }));
  await waitFor(() => expect(writes).toHaveLength(1));
  expect(writes[0]).toMatchObject({ method: 'PUT', match: '"rv-1"' });
  expect(writes[0].body?.spec).toEqual(original.spec);
});

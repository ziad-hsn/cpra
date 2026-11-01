import { afterEach, expect, it, vi } from 'vitest';
import { act, cleanup, render, fireEvent, screen, waitFor } from '@testing-library/react';
import { fixture, resource } from './managementFixture';
import { RecoveryRuleChanges } from '../src/components/RecoveryRules';
import { resourceView } from '../src/api/resources';

afterEach(() => { cleanup(); vi.restoreAllMocks(); });

const spec = () => ({ check: { driver: { type: 'http', config: { url: 'https://service.example/health' } }, interval: '60s', timeout: '5s' },
  recovery: { taxonomyVersion: '1', maxAttempts: 1, rules: [
    { id: 'certificate', match: { codes: ['tls.cert.*'] }, none: true },
    { id: 'refused', match: { codes: ['network.connection_refused'] }, driver: { type: 'webhook', config: {}, credentialRefs: { url: 'recovery-endpoint' } } },
  ] } });

it('edits and reorders typed predicates without losing line breaks or secret references', async () => {
  const { writes } = await fixture('monitors', 'rules', { initial: [resource('monitors', 'rules', spec())] });
  fireEvent.click(await screen.findByRole('button', { name: 'Edit monitor' }));
  const codes = screen.getByLabelText('Rule 2 failure codes');
  fireEvent.focus(codes);
  fireEvent.change(codes, { target: { value: 'network.connection_refused\n' } });
  expect(codes).toHaveValue('network.connection_refused\n');
  fireEvent.change(codes, { target: { value: 'network.connection_refused\nnetwork.connection_reset' } });
  fireEvent.blur(codes);
  fireEvent.click(screen.getByRole('button', { name: 'Move rule 2 up' }));
  expect(screen.getByLabelText('Rule 1 ID')).toHaveValue('refused');
  expect(screen.getByLabelText('Rule 1 ID')).toHaveFocus();
  expect(screen.getByLabelText('Rule 1 failure codes')).toHaveValue('network.connection_refused\nnetwork.connection_reset');
  await waitFor(() => expect(screen.getByLabelText('Recovery driver')).not.toBeDisabled());
  fireEvent.click(screen.getByRole('button', { name: 'Save' }));
  await waitFor(() => expect(writes).toHaveLength(1));
  expect(writes[0].match).toBe('"rv-1"');
  expect(writes[0].body?.spec).toMatchObject({ recovery: { taxonomyVersion: '1', rules: [
    { id: 'refused', match: { codes: ['network.connection_refused', 'network.connection_reset'] }, driver: { type: 'webhook', credentialRefs: { url: 'recovery-endpoint' } } },
    { id: 'certificate', none: true },
  ] } });
});

it('maps rule field failures to the editable predicate without replacing the conditional draft', async () => {
  const { writes } = await fixture('monitors', 'rules', { initial: [resource('monitors', 'rules', spec())], writeProblem: {
    code: 'invalidResource', status: 422, errors: [{ field: 'spec.recovery.rules[0].match.codes', reason: 'failureCode', message: 'untrusted diagnostic' }],
  } });
  fireEvent.click(await screen.findByRole('button', { name: 'Edit monitor' }));
  await waitFor(() => expect(screen.getByLabelText('Recovery driver')).not.toBeDisabled());
  fireEvent.click(screen.getByRole('button', { name: 'Save' }));
  await waitFor(() => expect(writes).toHaveLength(1));
  fireEvent.click(await screen.findByRole('button', { name: 'Go to spec.recovery.rules[0].match.codes' }));
  expect(screen.getByLabelText('Rule 1 failure codes')).toHaveFocus();
  expect(document.activeElement).toHaveAttribute('aria-invalid', 'true');
  expect(document.body.textContent).not.toContain('untrusted diagnostic');
});

it('edits rules on an opaque check with a recovery-only conditional patch', async () => {
  const external = { ...spec(), check: { driver: { type: 'external', config: { jobTypeID: 'private-check', version: 'v1', parameters: { secret: 'must-not-render-or-send' } } }, interval: '60s', timeout: '5s' } };
  const { writes } = await fixture('monitors', 'rules', { initial: [resource('monitors', 'rules', external)] });
  fireEvent.click(await screen.findByRole('button', { name: 'Edit intervention rules only' }));
  expect(screen.queryByRole('button', { name: 'Edit monitor' })).not.toBeInTheDocument();
  fireEvent.change(screen.getAllByLabelText('Rule 1 failure codes').at(-1)!, { target: { value: 'custom.cache.full' } });
  fireEvent.click(screen.getByRole('button', { name: 'Save intervention rules' }));
  await waitFor(() => expect(writes).toHaveLength(1));
  expect(writes[0].method).toBe('PATCH');
  expect(writes[0].match).toBe('"rv-1"');
  expect(Object.keys(writes[0].body!)).toEqual(['spec']);
  expect(Object.keys(writes[0].body!.spec as object)).toEqual(['recovery']);
  expect(JSON.stringify(writes)).not.toContain('must-not-render-or-send');
  expect(document.body.textContent).not.toContain('must-not-render-or-send');
});

it('uses recovery driver allowlists for recovery-only editing and refuses concealed branches', () => {
  const value = spec();
  const rules: Record<string, unknown>[] = value.recovery.rules;
  rules[1].driver = { type: 'docker', config: { container: 'target' }, credentialRefs: { } };
  const result = resourceView(resource('monitors', 'rules', value), 'monitors');
  expect(result.recoveryEditable).toBe(true);
  rules[1].driver = { type: 'webhook', config: { url: 'https://secret.invalid/path' }, credentialRefs: { } };
  expect(resourceView(resource('monitors', 'rules', value), 'monitors').recoveryEditable).toBe(false);
});

it('preserves unfinished secret-reference input when a rule moves', async () => {
  await fixture('monitors', 'rules', { initial: [resource('monitors', 'rules', spec())] });
  fireEvent.click(await screen.findByRole('button', { name: 'Edit monitor' }));
  const headers = screen.getAllByLabelText('Specific headers secret ID').at(-1)!;
  fireEvent.change(headers, { target: { value: 'unfinished-header-reference' } });
  const body = screen.getAllByLabelText('Specific body secret ID').at(-1)!;
  fireEvent.change(body, { target: { value: 'unfinished-body-reference' } });
  fireEvent.click(screen.getByRole('button', { name: 'Move rule 2 up' }));
  expect(screen.getAllByLabelText('Specific headers secret ID').at(-1)).toHaveValue('unfinished-header-reference');
  expect(screen.getAllByLabelText('Specific body secret ID').at(-1)).toHaveValue('unfinished-body-reference');
  expect(screen.getByLabelText('Rule 1 ID')).toHaveFocus();
});

it('replaces rule identities only when explicitly discarding a draft', async () => {
  vi.spyOn(window, 'confirm').mockReturnValue(true);
  const { records } = await fixture('monitors', 'rules', { initial: [resource('monitors', 'rules', spec())] });
  fireEvent.click(await screen.findByRole('button', { name: 'Edit monitor' }));
  fireEvent.click(screen.getByRole('button', { name: 'Remove rule 1' }));
  records.set('Monitor:rules', { ...resource('monitors', 'rules', spec()), metadata: { id: 'rules', name: 'rules', uid: 'uid-rules', resourceVersion: 'rv-2' } });
  fireEvent.click(screen.getByRole('button', { name: 'Save' }));
  fireEvent.click(await screen.findByRole('button', { name: 'Compare latest resource' }));
  fireEvent.click(await screen.findByRole('button', { name: 'Discard draft and use latest' }));
  fireEvent.click(screen.getByRole('button', { name: 'Move rule 2 up' }));
  expect(screen.getByLabelText('Rule 1 ID')).toHaveValue('refused');
  expect(screen.getByLabelText('Rule 1 ID')).toHaveFocus();
  expect(screen.getByLabelText('Rule 2 ID')).toHaveValue('certificate');
  fireEvent.click(screen.getByRole('button', { name: 'Add rule (2/32)' }));
  fireEvent.change(screen.getByLabelText('Rule 3 ID'), { target: { value: 'third' } });
  expect(screen.getByLabelText('Rule 1 ID')).toHaveValue('refused');
  expect(screen.getByLabelText('Rule 3 ID')).toHaveValue('third');
});

it('offers protocol status predicates for structured external checks', async () => {
  await fixture('monitors', 'rules', { initial: [opaque()] });
  fireEvent.click(await screen.findByRole('button', { name: 'Edit intervention rules only' }));
  expect(screen.getByLabelText('Rule 1 http statuses')).toBeInTheDocument();
  expect(screen.getByLabelText('Rule 1 grpc transport statuses')).toBeInTheDocument();
  expect(screen.getByLabelText('Rule 1 grpc health statuses')).toBeInTheDocument();
});

const opaque = () => resource('monitors', 'rules', { ...spec(), check: { driver: { type: 'external', config: {} }, interval: '60s', timeout: '5s' } });

it('keeps the recovery-only draft conditional after a competing edit or deletion', async () => {
  for (const remove of [false, true]) {
    const { writes, records } = await fixture('monitors', 'rules', { initial: [opaque()] });
    fireEvent.click(await screen.findByRole('button', { name: 'Edit intervention rules only' }));
    fireEvent.change(screen.getByLabelText('Rule 1 ID'), { target: { value: 'changed-rule' } });
    if (remove) records.delete('Monitor:rules');
    else records.get('Monitor:rules')!.metadata.resourceVersion = 'newer-version';
    fireEvent.click(screen.getByRole('button', { name: 'Save intervention rules' }));
    await waitFor(() => expect(writes).toHaveLength(1));
    await waitFor(() => expect(screen.getByRole('button', { name: 'Save intervention rules' })).not.toBeDisabled());
    expect(writes[0].match).toBe('"rv-1"');
    expect(screen.getByLabelText('Rule 1 ID')).toHaveValue('changed-rule');
    expect(writes).toHaveLength(1);
    cleanup();
  }
});

it('does not retry an unconfirmed recovery-only write', async () => {
  const { writes } = await fixture('monitors', 'rules', { initial: [opaque()], failWrite: true });
  fireEvent.click(await screen.findByRole('button', { name: 'Edit intervention rules only' }));
  fireEvent.click(screen.getByRole('button', { name: 'Save intervention rules' }));
  await waitFor(() => expect(screen.getByRole('alert')).toHaveTextContent('outcome is unconfirmed'));
  expect(screen.getByRole('button', { name: 'Save intervention rules' })).toBeDisabled();
  expect(writes).toHaveLength(1);
});

it('protects recovery-only drafts when leaving the route', async () => {
  const { router, writes } = await fixture('monitors', 'rules', { initial: [opaque()] });
  fireEvent.click(await screen.findByRole('button', { name: 'Edit intervention rules only' }));
  fireEvent.change(screen.getByLabelText('Rule 1 ID'), { target: { value: 'unsaved-rule' } });
  await act(async () => { await router.navigate('/elsewhere'); });
  expect(screen.getByRole('alertdialog')).toBeInTheDocument();
  fireEvent.click(screen.getByRole('button', { name: 'Keep editing' }));
  expect(screen.getByLabelText('Rule 1 ID')).toHaveValue('unsaved-rule');
  await act(async () => { await router.navigate('/elsewhere'); });
  fireEvent.click(screen.getByRole('button', { name: 'Discard draft and leave' }));
  expect(await screen.findByText('Elsewhere')).toBeInTheDocument();
  expect(writes).toHaveLength(0);
});

it('freezes recovery-only inputs while the original write is pending', async () => {
  const { fetcher, writes } = await fixture('monitors', 'rules', { initial: [opaque()] });
  const original = fetcher.getMockImplementation()!;
  let release!: () => void;
  const pending = new Promise<void>(resolve => { release = resolve; });
  fetcher.mockImplementation(async (input, options) => {
    if (options?.method === 'PATCH') await pending;
    return original(input, options);
  });
  fireEvent.click(await screen.findByRole('button', { name: 'Edit intervention rules only' }));
  fireEvent.change(screen.getByLabelText('Rule 1 ID'), { target: { value: 'submitted-rule' } });
  fireEvent.click(screen.getByRole('button', { name: 'Save intervention rules' }));
  await waitFor(() => expect(screen.getByLabelText('Rule 1 ID')).toHaveAttribute('readonly'));
  expect(screen.queryByRole('button', { name: 'Move rule 2 up' })).not.toBeInTheDocument();
  expect(screen.getByRole('button', { name: 'Close rule editor' })).toBeDisabled();
  await act(async () => { release(); });
  await waitFor(() => expect(writes).toHaveLength(1));
  expect(writes[0].body?.spec).toMatchObject({ recovery: { rules: [{ id: 'submitted-rule' }, { id: 'refused' }] } });
  expect(screen.queryByRole('button', { name: 'Save intervention rules' })).not.toBeInTheDocument();
});

it('summarizes rule changes and conceals protected driver values even in details', () => {
 const before = { taxonomyVersion: '1', rules: [
  { id: 'keep', match: { codes: ['network.connection_refused'] }, driver: { type: 'webhook', config: { url: 'PRIVATE_BEFORE_URL' } } },
  { id: 'removed', match: { codes: ['tls.cert.*'] }, none: true },
 ] };
 const after = { taxonomyVersion: '1', rules: [
  { id: 'added', match: { codes: ['tls.cert.*'] }, none: true },
  { id: 'keep', match: { codes: ['network.connection_reset'] }, none: true, privatePayload: 'PRIVATE_AFTER_VALUE' },
 ] };
 render(<RecoveryRuleChanges before={before} after={after} />);
 expect(screen.getByText('Removed rule removed.')).toBeInTheDocument();
 expect(screen.getByText(/Added rule added at position 1/)).toBeInTheDocument();
 expect(screen.getByText('Moved rule keep: position 1 → 2.')).toBeInTheDocument();
 expect(screen.getByText(/Changed match criteria for rule keep/)).toBeInTheDocument();
 expect(screen.getByText('Changed decision for rule keep: no intervention.')).toBeInTheDocument();
 fireEvent.click(screen.getByText('Show concealed configuration details'));
 expect(document.body.textContent).not.toMatch(/PRIVATE_BEFORE_URL|PRIVATE_AFTER_VALUE/);
});

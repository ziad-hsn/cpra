import { afterEach, expect, it, vi } from 'vitest';
import { cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react';
import { CodeBadge } from '../src/components/CodeBadge';
import { fixture, resource } from './managementFixture';

afterEach(() => { cleanup(); vi.restoreAllMocks(); });

it('describes review and verification without relying on color or repeated live announcements', () => {
  render(<><CodeBadge code="blue" /><CodeBadge code="cyan" /></>);
  expect(screen.getByText('RECOVERY REVIEW REQUIRED')).toHaveTextContent('RECOVERY REVIEW REQUIRED');
  expect(screen.getByText('HEALTH VERIFICATION PENDING')).toHaveTextContent('HEALTH VERIFICATION PENDING');
  expect(screen.queryByRole('status')).toBeNull();
});

it('requires an explicit blue destination and preserves the red rule on conditional edits', async () => {
  const original = resource('monitors', 'recovery-review', {
    check: { driver: { type: 'tcp', config: { host: 'localhost', port: 443 } }, interval: '60s', timeout: '5s' },
    recovery: { driver: { type: 'docker', config: { container: 'designated-test' } } },
    notifications: { red: { notifyType: 'email', recipientRefs: ['oncall'] } },
  });
  const { writes } = await fixture('monitors', original.metadata.id, { initial: [original] });
  fireEvent.click(await screen.findByRole('button', { name: 'Edit monitor' }));
  await waitFor(() => expect(screen.getByLabelText('Check driver')).not.toBeDisabled());
  expect(screen.getByText(/Uncertain recovery has no enabled blue notification destination/)).toBeInTheDocument();
  fireEvent.change(screen.getByLabelText('Add notification'), { target: { value: 'blue' } });
  expect(screen.getByLabelText('Notification blue driver')).toHaveValue('');
  fireEvent.change(screen.getByLabelText('Notification blue driver'), { target: { value: 'email' } });
  fireEvent.change(screen.getByLabelText('Specific notification blue recipients ID'), { target: { value: 'review-team' } });
  fireEvent.click(within(screen.getByRole('group', { name: 'Notification blue recipients' })).getByRole('button', { name: 'Use ID' }));
  fireEvent.click(screen.getByRole('button', { name: 'Save' }));
  await waitFor(() => expect(writes).toHaveLength(1));
  expect(writes[0]).toMatchObject({ method: 'PUT', match: '"rv-1"', body: { spec: {
    notifications: { red: { notifyType: 'email', recipientRefs: ['oncall'] }, blue: { notifyType: 'email', recipientRefs: ['review-team'] } },
  } } });
});

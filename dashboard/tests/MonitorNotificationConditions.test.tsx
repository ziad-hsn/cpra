import { afterEach, expect, it } from 'vitest';
import { cleanup, render, screen } from '@testing-library/react';
import { MonitorNotificationConditions } from '../src/components/MonitorNotificationConditions';

afterEach(cleanup);

it('shows a pending earlier closure without inventing a provider outcome or action', () => {
  render(<MonitorNotificationConditions conditions={[{conditionID: 'condition-1', kind: 'resolution', color: 'green', state: 'pending', reason: 'maintenance', pendingAt: '2026-10-22T12:00:00Z', recoveredAt: '2026-10-22T12:00:00Z'}]} />);
  expect(screen.getByRole('region', {name: 'Notification conditions'})).toHaveTextContent('Earlier outage closure');
  expect(screen.getByText(/does not describe the current outage or prove provider resolution/)).toBeInTheDocument();
  expect(screen.getByText('Reason: Maintenance active.')).toBeInTheDocument();
  expect(screen.queryByText(/Original action:/)).not.toBeInTheDocument();
});

it('renders held migration reasons and truncation without exposing extra response fields', () => {
  const condition = {conditionID: 'migration/old-action', kind: 'migration', color: 'red', state: 'held', migrationReason: 'binding_ambiguous', originalActionID: 'old-action', providerKey: 'private-provider-marker'};
  render(<MonitorNotificationConditions conditions={[condition]} truncated />);
  expect(screen.getByText(/binding ambiguous/)).toBeInTheDocument();
  expect(screen.getByText('old-action')).toBeInTheDocument();
  expect(screen.getByText(/Additional retained conditions/)).toBeInTheDocument();
  expect(screen.queryByText(/private-provider-marker/)).not.toBeInTheDocument();
});

it('bounds rendering to the supported projection limit', () => {
  render(<MonitorNotificationConditions conditions={Array.from({length: 101}, (_, i) => ({conditionID: `condition-${i}`, kind: 'opening', color: 'red', state: 'pending'}))} />);
  expect(screen.getAllByRole('article')).toHaveLength(100);
  expect(screen.queryByText('condition-100')).not.toBeInTheDocument();
  expect(screen.getByText(/Additional retained conditions/)).toBeInTheDocument();
});

it('shows retained blue closure suppression without claiming provider resolution or releasing an action', () => {
  render(<MonitorNotificationConditions conditions={[{conditionID: 'review-1/close', kind: 'review_closure', color: 'blue', state: 'suppressed', reason: 'suppressed', originalActionID: 'uncertain-recovery', pendingAt: '2026-10-23T12:00:00Z', recoveredAt: '2026-10-23T12:00:00Z'}]} observedAt="2026-10-23T12:00:01Z" sequence="18446744073709551615" />);
  expect(screen.getByRole('region', {name: 'Notification conditions'})).toHaveTextContent('Recovery review closure');
  expect(screen.getByText(/Storage sequence 18446744073709551615/)).toBeInTheDocument();
  expect(screen.queryByText(/18446744073709552000/)).not.toBeInTheDocument();
  expect(screen.getByText(/original recovery outcome and its hold remain subject to action review/)).toBeInTheDocument();
  expect(screen.getByText('Reason: Suppressed by current control policy.')).toBeInTheDocument();
  expect(screen.getByText('uncertain-recovery')).toBeInTheDocument();
});

it('separates unavailable condition coverage from a complete empty detail read', () => {
  const {rerender} = render(<MonitorNotificationConditions conditions={[]} truncated />);
  expect(screen.getByText(/Retained condition sample unavailable/)).toBeInTheDocument();
  expect(screen.queryByText(/No current notification conditions/)).not.toBeInTheDocument();
  rerender(<MonitorNotificationConditions conditions={[]} observedAt="2026-10-23T12:00:00Z" sequence="0" />);
  expect(screen.getByText(/No current notification conditions were reported/)).toBeInTheDocument();
  expect(screen.getByText(/Storage sequence 0/)).toBeInTheDocument();
  expect(screen.queryByText(/Additional retained conditions/)).not.toBeInTheDocument();
});

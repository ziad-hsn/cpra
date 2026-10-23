import { afterEach, expect, it } from 'vitest';
import { cleanup, render, screen } from '@testing-library/react';
import type { Monitor, MonitorStatus } from '../src/api/generated';
import { MonitorStatusDetails } from '../src/components/MonitorFields';
import { resourceView } from '../src/api/resources';

afterEach(cleanup);

function monitor(status?: MonitorStatus): Monitor {
  return { apiVersion: 'cpra.io/v2', kind: 'Monitor', metadata: { id: 'service', generation: 3 },
    spec: { check: { interval: '60s', timeout: '5s', driver: { type: 'http', config: {} } } }, status };
}

it('requires the owner observation of the exact desired generation', () => {
  const { rerender } = render(<MonitorStatusDetails monitor={monitor()} />);
  expect(screen.getByText(/application of this saved configuration has not been confirmed/)).toBeVisible();
  rerender(<MonitorStatusDetails monitor={monitor({ observedGeneration: 2 })} />);
  expect(screen.getByText(/has not been confirmed/)).toBeVisible();
  rerender(<MonitorStatusDetails monitor={monitor({ observedGeneration: 3 })} />);
  expect(screen.getByText('The controller reports this configuration generation applied.')).toBeVisible();
});

it.each(['', '0001-01-01T00:00:00Z', 'invalid'])('shows unavailable timestamps for %j', value => {
  render(<MonitorStatusDetails monitor={monitor({ lastCheckedAt: value, snoozedUntil: value })} />);
  expect(screen.getByText(/Last check:/)).toHaveTextContent('Last check: Unavailable');
  expect(screen.getByText(/Snoozed until:/)).toHaveTextContent('Snoozed until: Not reported');
});

it.each([undefined, NaN, Infinity, -1])('rejects unavailable or invalid latency %j', value => {
  render(<MonitorStatusDetails monitor={monitor({ lastCheckLatencyMs: { available: true, value } })} />);
  expect(screen.getByText(/Last-check latency:/)).toHaveTextContent('Last-check latency: Unavailable');
});

it('preserves measured zero latency without inventing unavailable measurements', () => {
  const { rerender } = render(<MonitorStatusDetails monitor={monitor({ lastCheckLatencyMs: { available: true, value: 0 } })} />);
  expect(screen.getByText('Last-check latency: 0 ms')).toBeVisible();
  rerender(<MonitorStatusDetails monitor={monitor({ lastCheckLatencyMs: { available: false, value: 0 } })} />);
  expect(screen.getByText('Last-check latency: Unavailable')).toBeVisible();
});

it('renders a safe failure and removes it after successful observation', () => {
  const { rerender } = render(<MonitorStatusDetails monitor={monitor({ lastFailure: { code: 'dns_failed', message: 'The target name could not be resolved.' } })} />);
  expect(screen.getByText('dns_failed')).toBeVisible();
  expect(screen.getByText(/The target name could not be resolved/)).toBeVisible();
  rerender(<MonitorStatusDetails monitor={monitor({ health: 'healthy' })} />);
  expect(screen.queryByText('dns_failed')).toBeNull();
});

it('preserves the server diagnostic through the production resource projection', () => {
  const response = monitor({ health: 'unhealthy', lastFailure: { code: 'dns_failed', message: 'The target name could not be resolved.' } });
  response.spec.check.driver.config = { url: 'https://example.test/health' };
  const projected = resourceView(response, 'monitors').resource as Monitor;
  expect(projected.status?.lastFailure).toEqual(response.status?.lastFailure);
  const { rerender } = render(<MonitorStatusDetails monitor={projected} />);
  expect(screen.getByText('dns_failed')).toBeVisible();
  const healthy = resourceView({ ...response, status: { health: 'healthy' } }, 'monitors').resource as Monitor;
  rerender(<MonitorStatusDetails monitor={healthy} />);
  expect(screen.queryByText('dns_failed')).toBeNull();
});

it('preserves current unavailability, real observation, maintenance and independent closure conditions', () => {
  const checked = '2026-10-22T12:00:00Z';
  const response = monitor({health: 'unknown', lastCheckedAt: checked, lastObservedHealth: 'healthy', lastObservedAt: checked,
    availability: {state: 'unavailable', reason: 'configurationProblem', observedAt: '2026-10-22T12:01:00Z'}, maintenance: true, notificationsSuppressed: true,
    configurationProblem: {code: 'ca_file_missing', message: 'Configured CA file is unavailable.'}, warnings: [{field: 'spec.notifications.blue', reason: 'unknownRecoveryDestination', message: 'Uncertain recovery has no enabled blue notification destination.'}],
    notificationConditions: [{conditionID: 'closure-1', kind: 'resolution', color: 'green', state: 'pending', reason: 'maintenance', pendingAt: checked, recoveredAt: checked}], notificationConditionsTruncated: true, notificationConditionsObservedAt: checked, notificationConditionsSequence: '18446744073709551615'});
  response.spec.check.driver.config = {url: 'https://example.test/health'};
  const projected = resourceView(response, 'monitors').resource as Monitor;
  expect(projected.status?.notificationConditions).toEqual(response.status?.notificationConditions);
  expect(projected.status?.notificationConditionsSequence).toBe('18446744073709551615');
  expect(projected.status?.availability).toEqual(response.status?.availability);
  render(<MonitorStatusDetails monitor={projected} />);
  expect(screen.getByText(/Current health:/)).toHaveTextContent('Current health: unknown');
  expect(screen.getByText(/Last target observation:/)).toHaveTextContent('healthy');
  expect(screen.getByText(/Maintenance:/)).toHaveTextContent('Active; checks continue');
  expect(screen.getByText(/Uncertain recovery has no enabled blue/)).toBeInTheDocument();
  expect(screen.getByRole('region', {name: 'Notification conditions'})).toHaveTextContent('Earlier outage closure');
  expect(screen.getByText(/Storage sequence 18446744073709551615/)).toBeInTheDocument();
  expect(screen.getByText(/Additional retained conditions/)).toBeInTheDocument();
});

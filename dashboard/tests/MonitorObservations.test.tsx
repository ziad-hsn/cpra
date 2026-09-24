import { afterEach, expect, it } from 'vitest';
import { cleanup, render, screen } from '@testing-library/react';
import type { Monitor, MonitorStatus } from '../src/api/generated';
import { MonitorStatusDetails } from '../src/components/MonitorFields';

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

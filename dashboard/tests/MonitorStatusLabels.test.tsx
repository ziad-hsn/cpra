import { afterEach, expect, it } from 'vitest';
import { cleanup, render, screen } from '@testing-library/react';
import { MonitorStatusLabels } from '../src/components/MonitorStatusLabels';
import type { MonitorSummary } from '../src/api/types';

afterEach(cleanup);

it('keeps actual health visible during Ignore and disabled checks', () => {
  const monitor = { health: 'unhealthy', status: 'disabled', enabled: false, ignored: true, phase: 'incident' } as MonitorSummary;
  render(<MonitorStatusLabels monitor={monitor} />);
  const state = screen.getByLabelText('Observed health and controls');
  expect(state).toHaveTextContent('Unhealthy');
  expect(state).toHaveTextContent('Disabled · Ignored · Incident open');
});

it('does not call the first failed observation an open incident', () => {
  render(<MonitorStatusLabels monitor={{ health: 'unhealthy', phase: 'idle', enabled: true } as MonitorSummary} />);
  expect(screen.getByLabelText('Observed health and controls')).toHaveTextContent('Unhealthy');
  expect(screen.queryByText(/Incident open/)).toBeNull();
});

it('labels preparation failure unknown and preserves the last real observation separately', () => {
  render(<MonitorStatusLabels monitor={{ health: 'unknown', last_observed_health: 'healthy', last_observed_at: '2026-10-22T10:00:00Z', availability: { state: 'unavailable', reason: 'configurationProblem' }, enabled: true, phase: 'idle' } as MonitorSummary} />);
  const state = screen.getByLabelText('Observed health and controls');
  expect(state).toHaveTextContent('Unknown');
  expect(state).toHaveTextContent('Check configuration unavailable');
  expect(state).toHaveTextContent('Last observed healthy');
});

it('uses controller maintenance and snooze observations without consulting the browser clock', () => {
  render(<MonitorStatusLabels monitor={{ health: 'unhealthy', phase: 'incident', maintenance: true, notifications_suppressed: true, snoozed_until: '2000-01-01T00:00:00Z', enabled: true } as MonitorSummary} />);
  const state = screen.getByLabelText('Observed health and controls');
  expect(state).toHaveTextContent('Maintenance');
  expect(state).toHaveTextContent('Snoozed until');
  expect(state).toHaveTextContent('Unhealthy');
});

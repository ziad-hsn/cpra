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

import { afterEach, beforeEach, expect, it, vi } from 'vitest';
import { cleanup, render, screen, waitFor } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { DurableStatus } from '../src/components/DurableStatus';
import { MonitorTimeline } from '../src/components/MonitorTimeline';
import { api } from '../src/api/client';
import { dashboardSession } from '../src/api/session';

afterEach(() => { cleanup(); vi.restoreAllMocks(); });
beforeEach(() => { vi.spyOn(dashboardSession, 'getSnapshot').mockReturnValue({ phase: 'legacy', epoch: 1 }); });
const wrap = (child: React.ReactNode) => <QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>{child}</QueryClientProvider>;

it('shows unknown action outcomes without a replay control', async () => {
  vi.spyOn(api, 'getState').mockResolvedValue({ storage: { mode: 'raft', ready: true, committed_index: 5, commit_latency_ms: 2, snapshot_duration_ms: 1 }, actions: [{ id: 'operation', revision: 'rev', kind: 'code', color: 'red', endpoint: 1, attempt: 1, state: 'unknown' }] });
  vi.spyOn(api, 'getHistory').mockResolvedValue({ events: [{ id: 'event', monitor_id: 'monitor', revision: 'rev', at: '2026-09-10T00:00:00Z', type: 'action_unknown', kind: 'code', endpoint: 1 }], retention_days: 30 });
  render(wrap(<MonitorTimeline monitorID='monitor' />));
  await waitFor(() => expect(screen.getByText(/1 action outcome is unknown/)).toBeInTheDocument());
  expect(screen.queryByRole('button', { name: /replay|retry action/i })).not.toBeInTheDocument();
  expect(screen.getByRole('button', { name: 'Next' })).toBeDisabled();
});

it('reports missing coverage instead of claiming the target was met', async () => {
  vi.spyOn(api, 'getState').mockResolvedValue({ storage: { mode: 'memory', ready: true, committed_index: 5, commit_latency_ms: 2, snapshot_duration_ms: 0 }, actions: [] });
  vi.spyOn(api, 'getSLO').mockResolvedValue({ generated: '2026-09-10T00:00:00Z', window_seconds: 300, queue_target_ms: 250, result_target_ms: 5000, coverage_complete: false, reports: [] });
  render(wrap(<DurableStatus />));
  await waitFor(() => expect(screen.getByText(/incomplete coverage/)).toBeInTheDocument());
  expect(screen.getByText(/No health-check observations/)).toBeInTheDocument();
  expect(screen.getByText(/State is discarded/)).toBeInTheDocument();
});

it('shows the audited operator and attention note as text in the retained timeline', async () => {
  vi.spyOn(api, 'getState').mockResolvedValue({ storage: { mode: 'memory', ready: true, committed_index: 5, commit_latency_ms: 2, snapshot_duration_ms: 0 }, actions: [] });
  vi.spyOn(api, 'getHistory').mockResolvedValue({ events: [{ id: 'attention', monitor_id: 'monitor', revision: 'rev', at: '2026-09-14T00:00:00Z', type: 'control_acknowledge', actor: 'alice', note: '<script>investigating</script>', incident_id: 'exact-incident' }], retention_days: 30 });
  render(wrap(<MonitorTimeline monitorID='monitor' />));
  expect(await screen.findByRole('cell', { name: 'alice' })).toBeInTheDocument();
  expect(screen.getByRole('cell', { name: '<script>investigating</script>' })).toBeInTheDocument();
  expect(document.querySelector('script')).toBeNull();
});

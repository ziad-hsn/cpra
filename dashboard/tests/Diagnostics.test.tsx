import { afterEach, expect, it, vi } from 'vitest';
import { cleanup, render, screen, within } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { QueueDiagnostics, PoolDiagnostics } from '../src/components/QueueDiagnostics';
import { DurableStatus } from '../src/components/DurableStatus';
import { api } from '../src/api/client';
import type { QueueStats, SLOResponse, WorkerPoolStats } from '../src/api/types';

afterEach(() => { cleanup(); vi.restoreAllMocks(); });
const queue: QueueStats = { queue_depth: 120, capacity: 100, last_enqueue: '', last_dequeue: '', avg_queue_time: 25_000_000, max_queue_time: 300_000_000, dequeued: 20, enqueued: 140, dropped: 5,
  max_job_latency: 0, avg_job_latency: 0, enqueue_rate: 12.5, dequeue_rate: 10, sample_window: 30_000_000_000, arrival_cv: 0, arrival_samples: 100 };
const pool: WorkerPoolStats = { current_capacity: 32, target_workers: 64, min_workers: 2, max_workers: 500, running_workers: 16, waiting_tasks: 120, pending_results: 15,
  tasks_submitted: 200, tasks_completed: 60, scaling_events: 3, last_scale_time: '', slo_condition: 'controller_limited', sizing_model: 'erlang-c+allen-cunneen', service_cv: 0, service_samples: 100, service_time: 10_000_000 };

it('shows actual queue saturation and distinguishes observed waits from oldest queued work', () => {
  render(<QueueDiagnostics name="pulse" queue={queue} />);
  const region = screen.getByRole('region', { name: 'pulse queue diagnostics' });
  expect(region).toHaveTextContent('120.0% — full');
  expect(region).toHaveTextContent('120 / 100');
  expect(region).toHaveTextContent('12.5/s');
  expect(within(region).getByText('Dequeue rate').parentElement).toHaveTextContent('10.0/s');
  expect(within(region).queryByText('Service rate')).not.toBeInTheDocument();
  expect(region).toHaveTextContent('0.000');
  expect(within(region).getByText('Oldest queued work').parentElement).toHaveTextContent('Not reported');
  expect(region).toHaveTextContent('not the age of work still waiting');
});

it('does not fabricate saturation or timing evidence for an unconfigured queue', () => {
  render(<QueueDiagnostics name="code" queue={{ ...queue, capacity: 0, queue_depth: 0, dequeued: 0, arrival_samples: 0 }} />);
  const region = screen.getByRole('region', { name: 'code queue diagnostics' });
  expect(region).toHaveTextContent('Unavailable (no capacity reported)');
  expect(region).toHaveTextContent('Unavailable (no dequeued samples)');
  expect(region.textContent).not.toMatch(/NaN|Infinity/);
});

it.each([
  [{ oldest_pending_available: true, oldest_pending_age: 25_000_000 }, '25.0ms'],
  [{ oldest_pending_available: true, oldest_pending_age: 0 }, '0ms'],
  [{ oldest_pending_available: false, oldest_pending_age: 999_000_000, oldest_pending_reason: 'queue_empty' }, 'No pending work'],
  [{ oldest_pending_available: false, oldest_pending_reason: 'admission_in_progress' }, 'Unavailable (oldest admission is in progress)'],
  [{ oldest_pending_available: false, oldest_pending_reason: 'queue_closed' }, 'Unavailable (queue is closed)'],
  [{ oldest_pending_available: true, oldest_pending_age: -1 }, 'Unavailable'],
  [{ oldest_pending_available: true }, 'Unavailable'],
  [{ oldest_pending_reason: 'future_reason' }, 'Not reported'],
] as const)('renders pending age with explicit availability: %j', (observation, expected) => {
  render(<QueueDiagnostics name="pulse" queue={{ ...queue, ...observation }} />);
  expect(screen.getByText('Oldest queued work').parentElement).toHaveTextContent(expected);
  expect(screen.getByText(/work already removed for execution/)).toBeInTheDocument();
});

it('exposes worker bounds, pending results, active capacity model and an actionable scaling condition', () => {
  render(<PoolDiagnostics name="pulse" pool={pool} />);
  const region = screen.getByRole('region', { name: 'pulse worker diagnostics' });
  expect(region).toHaveTextContent('2 – 500');
  expect(within(region).getByText('Pending results').parentElement).toHaveTextContent('15');
  expect(region).toHaveTextContent('erlang-c+allen-cunneen');
  expect(region).toHaveTextContent('0.000');
  expect(within(region).getByRole('status')).toHaveTextContent('Controller progress limits latency; more workers may not help');
});

it('shows all three latency distributions, exact attainment counts and recovery coverage gaps', async () => {
  const data: SLOResponse = { generated: '2026-09-14T00:00:00Z', window_seconds: 300, queue_target_ms: 250, result_target_ms: 5000, coverage_complete: false,
    gap_start: '2026-09-13T23:55:00Z', gap_end: '2026-09-13T23:56:00Z', reports: [{ driver: 'http', samples: 1000, expected: 1020, missed: 10, overdue: 10, pending: 20, timeouts: 5,
      scheduling_queue: { p50_ms: 25, p95_ms: 100, p99_ms: 251 }, execution: { p50_ms: 100, p95_ms: 2000, p99_ms: 5100 }, scheduled_result: { p50_ms: 125, p95_ms: 2100, p99_ms: 5351 },
      queue_met: 990, result_met: 980, queue_attainment: 990 / 1020, result_attainment: 980 / 1020, condition: 'downstream_limited' }] };
  vi.spyOn(api, 'getSLO').mockResolvedValue(data);
  vi.spyOn(api, 'getState').mockResolvedValue({ storage: { mode: 'raft', ready: true, committed_index: 42, commit_latency_ms: 2.5, snapshot_duration_ms: 0 }, actions: [] });
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(<QueryClientProvider client={client}><DurableStatus /></QueryClientProvider>);
  expect(await screen.findByText(/incomplete coverage following startup/)).toBeInTheDocument();
  const table = screen.getByRole('region', { name: 'Latency distributions and attainment' });
  expect(within(table).getByRole('columnheader', { name: 'Execution p50 / p95 / p99' })).toBeInTheDocument();
  expect(table).toHaveTextContent('1,000 / 1,020');
  expect(table).toHaveTextContent('(990 met)');
  expect(table).toHaveTextContent('(980 met)');
  expect(table).toHaveTextContent('10 / 5 / 10 / 20');
  expect(table).toHaveTextContent('Target execution is too slow');
  expect(screen.getByText(/Reported coverage gap/)).toHaveTextContent('2026-09-13T23:56:00Z');
  expect(screen.getByText(/histogram estimates/)).toHaveTextContent('do not constitute an SLA guarantee');
  const pauses = screen.getByRole('list', { name: 'Intentional monitoring pauses' });
  expect(pauses).toHaveTextContent('Not reported currently paused');
  expect(pauses).toHaveTextContent('Not reported of pause time');
  client.clear();
});

it('shows intentional pause exposure separately without turning paused monitors into successful samples', async () => {
  const empty = { p50_ms: null, p95_ms: null, p99_ms: null };
  vi.spyOn(api, 'getSLO').mockResolvedValue({ generated: '2026-09-14T00:00:00Z', window_seconds: 300, queue_target_ms: 250, result_target_ms: 5000, coverage_complete: false,
    reports: [{ driver: 'http', samples: 0, expected: 3, missed: 3, overdue: 0, pending: 0, timeouts: 0, scheduling_queue: empty, execution: empty, scheduled_result: empty,
      queue_met: 0, result_met: 0, queue_attainment: 0, result_attainment: 0, condition: 'insufficient_samples', paused_monitors: 2, paused_monitor_seconds: 15.5 },
    { driver: 'tcp', samples: 0, expected: 0, missed: 0, timeouts: 0, scheduling_queue: empty, execution: empty, scheduled_result: empty,
      queue_met: 0, result_met: 0, queue_attainment: null, result_attainment: null, condition: 'insufficient_samples', paused_monitors: 0, paused_monitor_seconds: 0 }] });
  vi.spyOn(api, 'getState').mockResolvedValue({ storage: { mode: 'raft', ready: true, committed_index: 42, commit_latency_ms: 2.5, snapshot_duration_ms: 0 }, actions: [] });
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(<QueryClientProvider client={client}><DurableStatus /></QueryClientProvider>);
  const pauses = await screen.findByRole('list', { name: 'Intentional monitoring pauses' });
  expect(pauses).toHaveTextContent('http: 2 currently paused; 15.5 monitor-seconds');
  expect(pauses).toHaveTextContent('tcp: 0 currently paused; 0 monitor-seconds');
  expect(screen.getByRole('region', { name: 'Latency distributions and attainment' })).toHaveTextContent('0 / 3');
  expect(screen.getByText(/Two monitors paused/)).toHaveTextContent('Overlapping disable and snooze count once');
  expect(screen.getByText(/Two monitors paused/)).toHaveTextContent('preserve obligations and misses');
  client.clear();
});

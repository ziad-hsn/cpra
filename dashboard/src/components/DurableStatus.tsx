import { useQuery } from '@tanstack/react-query';
import { api } from '../api/client';
import { conditionLabel } from '../lib/diagnostics';
import type { Percentiles } from '../api/types';
import { formatMs, formatNumber } from '../lib/format';

const latency = (value: number | null | undefined) => typeof value === 'number' && Number.isFinite(value) ? formatMs(value) : 'Unavailable';
const distribution = (value: Percentiles) => `${latency(value.p50_ms)} / ${latency(value.p95_ms)} / ${latency(value.p99_ms)}`;
const percent = (value: number | null) => typeof value === 'number' && Number.isFinite(value) ? `${(value * 100).toFixed(3)}%` : 'Unavailable';
const pauseCount = (value?: number) => typeof value === 'number' && Number.isSafeInteger(value) && value >= 0 ? formatNumber(value) : 'Not reported';
const pauseDuration = (value?: number) => typeof value === 'number' && Number.isFinite(value) && value >= 0 ? `${formatNumber(Math.round(value * 10) / 10)} monitor-seconds` : 'Not reported';

export function DurableStatus() {
  const state = useQuery({ queryKey: ['durable-state'], queryFn: () => api.getState(), refetchInterval: 5000 });
  const slo = useQuery({ queryKey: ['slo'], queryFn: api.getSLO, refetchInterval: 5000 });
  return <section className='card' aria-label='Persistence and latency targets'>
    <div className='card-head'><h2 className='card-title'>Persistence and latency targets</h2></div>
    {state.isError ? <p role='alert'>Persistence status unavailable.</p> : !state.data ? <p>Loading persistence status…</p> : <p>
      Storage: <strong>{state.data.storage.mode}</strong>. {state.data.storage.ready ? 'Ready' : 'Unavailable — new work is stopped'}.
      {' '}Committed position: {state.data.storage.committed_index}. Commit latency: {formatMs(state.data.storage.commit_latency_ms)}.
      {state.data.storage.mode === 'raft' && ' Single node; the data directory must be retained across restarts.'}
      {state.data.storage.mode === 'memory' && ' State is discarded when this process exits.'}
    </p>}
    {slo.isError ? <p role='alert'>Latency measurements unavailable.</p> : !slo.data ? <p>Loading measurements…</p> : <>
      <p className='muted'>Measured {formatNumber(slo.data.window_seconds)}-second health-check window: p99 scheduling and queue delay ≤{formatMs(slo.data.queue_target_ms)}; scheduled to committed result ≤{formatMs(slo.data.result_target_ms)}.</p>
      {!slo.data.coverage_complete && <p role='status'>This window has incomplete coverage following startup or recovery. It cannot establish target attainment for the full five minutes.</p>}
      {(slo.data.gap_start || slo.data.gap_end) && <p className='muted'>Reported coverage gap: {slo.data.gap_start || 'Start not reported'} to {slo.data.gap_end || 'End not reported'}.</p>}
      <div style={{ overflowX: 'auto' }} role='region' aria-label='Latency distributions and attainment' tabIndex={0}><table className='data-table'>
        <thead><tr><th>Driver</th><th>Samples / expected</th><th>Scheduling + queue p50 / p95 / p99</th><th>Execution p50 / p95 / p99</th><th>Scheduled to result p50 / p95 / p99</th><th>Queue attainment</th><th>Result attainment</th><th>Missed / timeouts / overdue / pending</th><th>Condition</th></tr></thead>
        <tbody>{slo.data.reports.map(r => <tr key={r.driver}><td>{r.driver}</td><td>{formatNumber(r.samples)} / {formatNumber(r.expected)}</td><td>{distribution(r.scheduling_queue)}</td><td>{distribution(r.execution)}</td><td>{distribution(r.scheduled_result)}</td><td>{percent(r.queue_attainment)} ({formatNumber(r.queue_met)} met)</td><td>{percent(r.result_attainment)} ({formatNumber(r.result_met)} met)</td><td>{r.missed} / {r.timeouts} / {r.overdue ?? 'Not reported'} / {r.pending ?? 'Not reported'}</td><td>{conditionLabel(r.condition)}</td></tr>)}</tbody>
      </table></div>
      <ul aria-label='Intentional monitoring pauses'>{slo.data.reports.map(r => <li key={r.driver}>{r.driver}: {pauseCount(r.paused_monitors)} currently paused; {pauseDuration(r.paused_monitor_seconds)} of pause time in this window.</li>)}</ul>
      {slo.data.reports.length > 0 && <p className='muted'>Pause time records disabled or snoozed monitors as observed by the controller. Two monitors paused for one second contribute two monitor-seconds. Overlapping disable and snooze count once. Pauses add no successful checks and preserve obligations and misses from before the pause. Startup and recovery gaps remain unavailable.</p>}
      {slo.data.reports.length === 0 && <p>No health-check observations yet.</p>}
      <p className='muted'>Percentiles are histogram estimates. Threshold counts include missed work, timeouts and overdue checks. New unfinished checks enter the denominator after their five-second bucket and result deadline have elapsed. These measurements describe the observed window and do not constitute an SLA guarantee.</p>
    </>}
  </section>;
}

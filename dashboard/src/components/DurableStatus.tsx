import { useQuery } from '@tanstack/react-query';
import { api } from '../api/client';
import { formatMs, formatNumber } from '../lib/format';

const latency = (value: number | null) => value === null ? 'Unavailable' : formatMs(value);
const percent = (value: number | null) => value === null ? 'Unavailable' : `${(value * 100).toFixed(3)}%`;

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
      <p className='muted'>Five-minute health-check targets: p99 scheduling and queue delay ≤{formatMs(slo.data.queue_target_ms)}; scheduled to committed result ≤{formatMs(slo.data.result_target_ms)}.</p>
      {!slo.data.coverage_complete && <p role='status'>This window has incomplete coverage following startup or recovery. It cannot establish target attainment for the full five minutes.</p>}
      <div style={{ overflowX: 'auto' }}><table className='data-table'>
        <thead><tr><th>Driver</th><th>Samples</th><th>Queue p99</th><th>Result p99</th><th>Queue attainment</th><th>Result attainment</th><th>Missed / timeouts / overdue</th><th>Condition</th></tr></thead>
        <tbody>{slo.data.reports.map(r => <tr key={r.driver}><td>{r.driver}</td><td>{formatNumber(r.samples)}</td><td>{latency(r.scheduling_queue.p99_ms)}</td><td>{latency(r.scheduled_result.p99_ms)}</td><td>{percent(r.queue_attainment)}</td><td>{percent(r.result_attainment)}</td><td>{r.missed} / {r.timeouts} / {r.overdue ?? 0}</td><td>{r.condition.replaceAll('_', ' ')}</td></tr>)}</tbody>
      </table></div>
      {slo.data.reports.length === 0 && <p>No health-check observations yet.</p>}
      <p className='muted'>Percentiles are histogram estimates. Threshold counts include missed work, timeouts and overdue checks. New unfinished checks enter the denominator after their five-second bucket and result deadline have elapsed. These measurements describe the observed window and do not constitute an SLA guarantee.</p>
    </>}
  </section>;
}

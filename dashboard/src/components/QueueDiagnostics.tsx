import type { QueueStats, WorkerPoolStats } from '../api/types';
import { formatDurationNs, formatNumber } from '../lib/format';
import { conditionLabel } from '../lib/diagnostics';

const valid = (value: number | undefined): value is number => typeof value === 'number' && Number.isFinite(value) && value >= 0;
const number = (value?: number) => valid(value) ? formatNumber(value) : 'Unavailable';
const duration = (value?: number) => valid(value) ? formatDurationNs(value) : 'Unavailable';
const rate = (value?: number) => valid(value) ? `${value.toFixed(1)}/s` : 'Unavailable';
const pendingReasons: Record<string, string> = {
  queue_empty: 'No pending work',
  admission_in_progress: 'Unavailable (oldest admission is in progress)',
  queue_closed: 'Unavailable (queue is closed)',
  invalid_clock_observation: 'Unavailable (invalid clock observation)',
};
function oldestPending(q: QueueStats): string {
  if (q.oldest_pending_available === true) return duration(q.oldest_pending_age);
  return q.oldest_pending_reason && Object.hasOwn(pendingReasons, q.oldest_pending_reason) ? pendingReasons[q.oldest_pending_reason] : 'Not reported';
}

function StatRow({ label, value }: { label: string; value: string }) {
  return <div className="row" style={{ justifyContent: 'space-between', padding: '3px 0', gap: 12, fontSize: 12 }}><span className="muted">{label}</span><span className="mono" style={{ fontWeight: 500, textAlign: 'right' }}>{value}</span></div>;
}

export function QueueDiagnostics({ name, queue: q }: { name: string; queue: QueueStats }) {
  const saturation = valid(q.capacity) && q.capacity > 0 && valid(q.queue_depth) ? q.queue_depth / q.capacity : undefined;
  return <section className="card" aria-label={`${name} queue diagnostics`} style={{ background: 'var(--surface-2)' }}>
    <h3 className="card-title" style={{ textTransform: 'capitalize' }}>{name}</h3>
    <StatRow label="Saturation" value={saturation === undefined ? 'Unavailable (no capacity reported)' : `${(saturation * 100).toFixed(1)}%${saturation >= 1 ? ' — full' : ''}`} />
    <StatRow label="Depth / capacity" value={`${number(q.queue_depth)} / ${number(q.capacity)}`} />
    <StatRow label="Arrival rate" value={rate(q.enqueue_rate)} /><StatRow label="Dequeue rate" value={rate(q.dequeue_rate)} />
    <StatRow label="Enqueued / dequeued" value={`${number(q.enqueued)} / ${number(q.dequeued)}`} /><StatRow label="Dropped" value={number(q.dropped)} />
    <StatRow label="Observed average wait" value={q.dequeued > 0 ? duration(q.avg_queue_time) : 'Unavailable (no dequeued samples)'} />
    <StatRow label="Observed maximum wait" value={q.dequeued > 0 ? duration(q.max_queue_time) : 'Unavailable (no dequeued samples)'} />
    <StatRow label="Oldest queued work" value={oldestPending(q)} />
    <StatRow label="Arrival variability (CV)" value={(q.arrival_samples ?? 0) >= 2 && valid(q.arrival_cv) ? q.arrival_cv.toFixed(3) : 'Unavailable'} />
    <StatRow label="Variability samples" value={number(q.arrival_samples)} /><StatRow label="Rate sample window" value={q.sample_window > 0 ? duration(q.sample_window) : 'Unavailable'} />
    <p className="muted">Saturation is the current depth divided by capacity. Observed waits describe dequeued work, not the age of work still waiting. Oldest queued work measures an admitted copy still waiting for dequeue; work already removed for execution is excluded. Depth and age are separate observations during concurrent activity.</p>
  </section>;
}

export function PoolDiagnostics({ name, pool: p }: { name: string; pool: WorkerPoolStats }) {
  return <section className="card" aria-label={`${name} worker diagnostics`} style={{ background: 'var(--surface-2)' }}>
    <h3 className="card-title" style={{ textTransform: 'capitalize' }}>{name}</h3>
    <StatRow label="Running / capacity" value={`${number(p.running_workers)} / ${number(p.current_capacity)}`} />
    <StatRow label="Target workers" value={number(p.target_workers)} /><StatRow label="Worker bounds" value={`${number(p.min_workers)} – ${number(p.max_workers)}`} />
    <StatRow label="Waiting tasks" value={number(p.waiting_tasks)} /><StatRow label="Pending results" value={number(p.pending_results)} />
    <StatRow label="Submitted / completed" value={`${number(p.tasks_submitted)} / ${number(p.tasks_completed)}`} /><StatRow label="Scaling events" value={number(p.scaling_events)} />
    <StatRow label="Observed service time" value={(p.service_samples ?? 0) > 0 ? duration(p.service_time) : 'Unavailable'} />
    <StatRow label="Service variability (CV)" value={(p.service_samples ?? 0) >= 2 && valid(p.service_cv) ? p.service_cv.toFixed(3) : 'Unavailable'} /><StatRow label="Service samples" value={number(p.service_samples)} />
    <StatRow label="Capacity model" value={p.sizing_model || 'Not reported'} />
    <p role="status">Latency feedback: {conditionLabel(p.slo_condition)}.</p>
  </section>;
}

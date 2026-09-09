import { useSystems, useQueues, usePools } from '../hooks/queries';
import { KpiTile } from '../components/KpiTile';
import { ErrorState } from '../components/ErrorState';
import { LoadingSkeleton } from '../components/LoadingSkeleton';
import { formatNumber, formatDurationNs } from '../lib/format';
import { Collapse } from '../components/Collapse';
import type { QueueStats, WorkerPoolStats } from '../api/types';

export default function SystemHealth() {
  const { data: systemsData, isLoading: sysLoading, isError: sysError, refetch: sysRefetch } = useSystems();
  const { data: queues } = useQueues();
  const { data: pools } = usePools();

  const aggregate = systemsData?.aggregate;
  const systems = systemsData?.systems ?? {};

  return (
    <div className="page">
      <div className="page-head">
        <div>
          <h1>System Health</h1>
          <div className="lead">ECS system throughput, queue backpressure, and worker pool utilization</div>
        </div>
      </div>

      {/* Engine KPIs */}
      <div className="grid-kpis">
        <KpiTile
          label="Entities / sec"
          value={formatNumber(Math.round(aggregate?.entities_per_second ?? 0))}
          code="cyan"
          icon="activity"
          sub="throughput"
        />
        <KpiTile
          label="Updates / sec"
          value={formatNumber(Math.round(aggregate?.updates_per_second ?? 0))}
          code="cyan"
          icon="pulse"
          sub="tick rate"
        />
        <KpiTile
          label="Active Systems"
          value={formatNumber(aggregate?.system_count ?? 0)}
          code="gray"
          icon="layers"
          sub="ECS systems loaded"
        />
        <KpiTile
          label="Avg Update Duration"
          value={formatDurationNs(aggregate?.avg_update_duration ?? 0)}
          code="yellow"
          icon="clock"
          sub="per system tick"
        />
      </div>

      {/* System metrics table */}
      <div className="card">
        <div className="card-head">
          <span className="card-title">ECS System Metrics</span>
          <span className="card-sub">per-system throughput and timing</span>
        </div>
        {sysError ? (
          <ErrorState message="Failed to load system metrics" onRetry={() => sysRefetch()} />
        ) : sysLoading ? (
          <LoadingSkeleton lines={5} />
        ) : Object.keys(systems).length === 0 ? (
          <div className="empty-state">No system metrics available yet.</div>
        ) : (
          <table className="data-table">
            <thead>
              <tr>
                <th>System</th>
                <th style={{ textAlign: 'right' }}>Updates</th>
                <th style={{ textAlign: 'right' }}>Entities</th>
                <th style={{ textAlign: 'right' }}>Avg</th>
                <th style={{ textAlign: 'right' }}>Max</th>
                <th style={{ textAlign: 'right' }}>Min</th>
              </tr>
            </thead>
            <tbody>
              {Object.entries(systems).map(([name, m]) => {
                const avg = m.total_updates > 0 ? m.total_duration / m.total_updates : 0;
                return (
                  <tr key={name}>
                    <td className="mono">{name}</td>
                    <td style={{ textAlign: 'right' }}>{formatNumber(m.total_updates)}</td>
                    <td style={{ textAlign: 'right' }}>{formatNumber(m.total_entities_processed)}</td>
                    <td style={{ textAlign: 'right' }}>{formatDurationNs(avg)}</td>
                    <td style={{ textAlign: 'right', background: m.max_update_duration > 1e8 ? 'var(--status-bg-degraded)' : undefined }}>{formatDurationNs(m.max_update_duration)}</td>
                    <td style={{ textAlign: 'right' }}>{formatDurationNs(m.min_update_duration)}</td>
                  </tr>
                );
              })}
            </tbody>
          </table>
        )}
      </div>

      {/* Queue stats — progressive disclosure */}
      {queues && (
        <Collapse title="Queue Stats" subtitle="SEDA queue depth, throughput, and wait times" defaultOpen={false}>
          <div className="grid-3">
            {(['pulse', 'intervention', 'code'] as const).map((name) => {
              const q = queues[name] as QueueStats;
              return (
                <div key={name} className="card" style={{ background: 'var(--surface-2)' }}>
                  <div className="card-head">
                    <span className="card-title" style={{ textTransform: 'capitalize' }}>{name}</span>
                  </div>
                  <StatRow label="Depth" value={formatNumber(q.queue_depth)} />
                  <StatRow label="Capacity" value={formatNumber(q.capacity)} />
                  <StatRow label="Enqueued" value={formatNumber(q.enqueued)} />
                  <StatRow label="Dequeued" value={formatNumber(q.dequeued)} />
                  <StatRow label="Dropped" value={formatNumber(q.dropped)} />
                  <StatRow label="Arrival rate" value={`${q.enqueue_rate.toFixed(1)}/s`} />
                  <StatRow label="Service rate" value={`${q.dequeue_rate.toFixed(1)}/s`} />
                  <StatRow label="Avg wait" value={formatDurationNs(q.avg_queue_time)} />
                </div>
              );
            })}
          </div>
        </Collapse>
      )}

      {/* Worker pool stats — progressive disclosure */}
      {pools && (
        <Collapse title="Worker Pool Stats" subtitle="M/M/c worker sizing & autoscaling" defaultOpen={false}>
          <div className="grid-3">
            {(['pulse', 'intervention', 'code'] as const).map((name) => {
              const p = pools[name] as WorkerPoolStats;
              return (
                <div key={name} className="card" style={{ background: 'var(--surface-2)' }}>
                  <div className="card-head">
                    <span className="card-title" style={{ textTransform: 'capitalize' }}>{name}</span>
                  </div>
                  <StatRow label="Running" value={formatNumber(p.running_workers)} />
                  <StatRow label="Capacity" value={formatNumber(p.current_capacity)} />
                  <StatRow label="Target" value={formatNumber(p.target_workers)} />
                  <StatRow label="Waiting" value={formatNumber(p.waiting_tasks)} />
                  <StatRow label="Submitted" value={formatNumber(p.tasks_submitted)} />
                  <StatRow label="Completed" value={formatNumber(p.tasks_completed)} />
                  <StatRow label="Scaling events" value={formatNumber(p.scaling_events)} />
                </div>
              );
            })}
          </div>
        </Collapse>
      )}
    </div>
  );
}

function StatRow({ label, value }: { label: string; value: string }) {
  return (
    <div className="row" style={{ justifyContent: 'space-between', padding: '3px 0', fontSize: 12 }}>
      <span className="muted">{label}</span>
      <span className="mono" style={{ color: 'var(--text-primary)', fontWeight: 500 }}>{value}</span>
    </div>
  );
}

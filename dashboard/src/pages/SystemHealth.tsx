import { DurableStatus } from '../components/DurableStatus';
import { RuntimeDiagnostics } from '../components/RuntimeDiagnostics';
import { useSystems, useQueues, usePools } from '../hooks/queries';
import { KpiTile } from '../components/KpiTile';
import { ErrorState } from '../components/ErrorState';
import { LoadingSkeleton } from '../components/LoadingSkeleton';
import { formatNumber, formatDurationNs } from '../lib/format';
import { Collapse } from '../components/Collapse';
import { PoolDiagnostics, QueueDiagnostics } from '../components/QueueDiagnostics';

export default function SystemHealth() {
  const { data: systemsData, isLoading: sysLoading, isError: sysError, refetch: sysRefetch } = useSystems();
  const { data: queues, isError: queuesError, isLoading: queuesLoading } = useQueues();
  const { data: pools, isError: poolsError, isLoading: poolsLoading } = usePools();

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

      <RuntimeDiagnostics />
      <DurableStatus />

      {/* Engine KPIs */}
      <div className="grid-kpis">
        <KpiTile
          label="Entities / sec"
          value={aggregate && !sysError ? formatNumber(Math.round(aggregate.entities_per_second)) : 'Unavailable'}
          code="cyan"
          icon="activity"
          sub="throughput"
        />
        <KpiTile
          label="Updates / sec"
          value={aggregate && !sysError ? formatNumber(Math.round(aggregate.updates_per_second)) : 'Unavailable'}
          code="cyan"
          icon="pulse"
          sub="tick rate"
        />
        <KpiTile
          label="Active Systems"
          value={aggregate && !sysError ? formatNumber(aggregate.system_count) : 'Unavailable'}
          code="gray"
          icon="layers"
          sub="ECS systems loaded"
        />
        <KpiTile
          label="Avg Update Duration"
          value={aggregate && !sysError ? formatDurationNs(aggregate.avg_update_duration) : 'Unavailable'}
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
          <div className="table-scroll" role="region" aria-label="System metrics" tabIndex={0}>
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
          </div>
        )}
      </div>

      {queuesError && <p role="alert">Queue measurements unavailable. Any last received values below may be out of date.</p>}
      {queuesLoading && <p>Loading queue measurements…</p>}
      {queues && <Collapse title="Queue saturation" subtitle="Depth, throughput, drops and observed waits" defaultOpen>
        <div className="grid-3">{(['pulse', 'intervention', 'code'] as const).map(name => queues[name] && <QueueDiagnostics key={name} name={name} queue={queues[name]} />)}</div>
      </Collapse>}
      {poolsError && <p role="alert">Worker measurements unavailable. Any last received values below may be out of date.</p>}
      {poolsLoading && <p>Loading worker measurements…</p>}
      {pools && <Collapse title="Worker Pool Stats" subtitle="Worker bounds, pending results and latency feedback" defaultOpen>
        <div className="grid-3">{(['pulse', 'intervention', 'code'] as const).map(name => pools[name] && <PoolDiagnostics key={name} name={name} pool={pools[name]} />)}</div>
      </Collapse>}
    </div>
  );
}

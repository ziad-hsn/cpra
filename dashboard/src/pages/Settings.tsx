import { useConfig, useHealth } from '../hooks/queries';
import { ErrorState } from '../components/ErrorState';
import { LoadingSkeleton } from '../components/LoadingSkeleton';
import { Icon } from '../components/Icon';
import { formatDurationNs } from '../lib/format';

export default function Settings() {
  const { data: config, isLoading, isError, refetch } = useConfig();
  const { data: health, isError: healthError } = useHealth();

  return (
    <div className="page">
      <div className="page-head">
        <div>
          <h1>Settings</h1>
          <div className="lead">Engine configuration &amp; diagnostics</div>
        </div>
      </div>

      {/* Config */}
      <div className="card">
        <div className="card-head">
          <span className="card-title">Engine Configuration</span>
          <span className="card-sub">current runtime values</span>
        </div>
        {isError ? (
          <ErrorState message="Failed to load configuration" onRetry={() => refetch()} />
        ) : isLoading || !config ? (
          <LoadingSkeleton lines={6} />
        ) : (
          <table className="data-table">
            <tbody>
              <ConfigRow label="Queue Capacity" value={String(config.queue_capacity)} />
              <ConfigRow label="Batch Size" value={String(config.batch_size)} />
              <ConfigRow label="Alert Cooldown" value={formatDurationNs(config.alert_cooldown)} />
              <ConfigRow label="Recovery Bypass" value={config.recovery_bypass ? 'Enabled' : 'Disabled'} />
              <ConfigRow label="Adaptive Queue" value={config.use_adaptive_queue ? 'Enabled' : 'Disabled'} />
              <ConfigRow label="Queue Type" value={config.queue_type} />
            </tbody>
          </table>
        )}
      </div>

      {/* Health */}
      <div className="card">
        <div className="card-head">
          <span className="card-title">Health Check</span>
          <span className="card-sub">liveness probe</span>
        </div>
        <div className="row" style={{ gap: 8 }}>
          <span
            className="status-dot"
            style={{ background: !healthError && health?.status === 'ok' ? 'var(--status-operational)' : 'var(--status-critical)' }}
          />
          <span style={{ fontSize: 14, fontWeight: 600 }}>
            {healthError ? 'Server health unavailable' : health?.status === 'ok' ? 'Server is healthy' : health ? 'Server unhealthy' : 'Checking…'}
          </span>
        </div>
      </div>

      {/* Metrics link */}
      <div className="card">
        <div className="card-head">
          <span className="card-title">Prometheus Metrics</span>
          <span className="card-sub">raw text exposition</span>
        </div>
        <p className="muted" style={{ fontSize: 12, marginBottom: 8 }}>
          Raw Prometheus text exposition is available at the /metrics endpoint.
        </p>
        <a href="/metrics" target="_blank" rel="noopener noreferrer" className="btn">
          Open /metrics <Icon name="external" size={14} />
        </a>
      </div>

      {/* About */}
      <div className="card">
        <div className="card-head">
          <span className="card-title">About</span>
        </div>
        <p style={{ fontSize: 12, color: 'var(--text-secondary)' }}>
          <strong>CPRa</strong> checks services, sends alerts, and runs configured recovery actions.
        </p>
      </div>
    </div>
  );
}

function ConfigRow({ label, value }: { label: string; value: string }) {
  return (
    <tr>
      <td className="muted" style={{ width: '40%' }}>{label}</td>
      <td className="mono">{value}</td>
    </tr>
  );
}

import type { MonitorSummary, MonitorStatus } from '../api/types';
import { formatDateTime } from '../lib/format';
import { StatusChip } from './StatusChip';

/** Health is observed independently of control suppression and incident phase. */
export function MonitorStatusLabels({ monitor, compact = false }: { monitor: MonitorSummary; compact?: boolean }) {
  const health: Record<string, MonitorStatus> = { healthy: 'up', unhealthy: 'down', warning: 'degraded', unknown: 'unknown' };
  const status = monitor.health ? health[monitor.health] ?? 'unknown' : monitor.status;
  const controls = [monitor.enabled === false ? 'Disabled' : '', monitor.ignored ? 'Ignored' : '',
    monitor.snoozed_until ? (compact ? 'Snoozed' : `Snoozed until ${formatDateTime(monitor.snoozed_until)}`) : '',
    monitor.maintenance ? 'Maintenance' : '',
    monitor.availability?.state === 'unavailable' ? (monitor.availability.reason === 'configurationProblem' ? 'Check configuration unavailable' : 'Checks unavailable') : '',
    monitor.availability?.state === 'pending' ? 'Awaiting first observation' : '',
    monitor.phase === 'incident' ? 'Incident open' : monitor.phase === 'verifying' ? 'Verifying recovery' : ''].filter(Boolean);
  return <div aria-label="Observed health and controls" style={{ display: 'flex', flexDirection: 'column', gap: 3, minWidth: 0 }}>
    <StatusChip status={status} label={monitor.health ? monitor.health[0].toUpperCase() + monitor.health.slice(1) : undefined} />
    {!!controls.length && <span className="muted" style={{ fontSize: compact ? 10 : 12, overflowWrap: 'anywhere' }}>{controls.join(' · ')}</span>}
    {monitor.availability?.state === 'unavailable' && monitor.last_observed_at && <span className="muted" style={{ fontSize: compact ? 10 : 12, overflowWrap: 'anywhere' }}>Last observed {monitor.last_observed_health ?? 'unknown'} · {formatDateTime(monitor.last_observed_at)}</span>}
  </div>;
}

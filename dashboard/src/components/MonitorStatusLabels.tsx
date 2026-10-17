import type { MonitorSummary, MonitorStatus } from '../api/types';
import { formatDateTime } from '../lib/format';
import { StatusChip } from './StatusChip';

/** Health is observed independently of control suppression and incident phase. */
export function MonitorStatusLabels({ monitor, compact = false }: { monitor: MonitorSummary; compact?: boolean }) {
  const health: Record<string, MonitorStatus> = { healthy: 'up', unhealthy: 'down', warning: 'degraded', unknown: 'unknown' };
  const status = monitor.health ? health[monitor.health] ?? 'unknown' : monitor.status;
  const controls = [monitor.enabled === false ? 'Disabled' : '', monitor.ignored ? 'Ignored' : '',
    monitor.snoozed_until && Date.parse(monitor.snoozed_until) > Date.now() ? (compact ? 'Snoozed' : `Snoozed until ${formatDateTime(monitor.snoozed_until)}`) : '',
    monitor.phase === 'incident' ? 'Incident open' : monitor.phase === 'verifying' ? 'Verifying recovery' : ''].filter(Boolean);
  return <div aria-label="Observed health and controls" style={{ display: 'flex', flexDirection: 'column', gap: 3, minWidth: 0 }}>
    <StatusChip status={status} label={monitor.health ? monitor.health[0].toUpperCase() + monitor.health.slice(1) : undefined} />
    {!!controls.length && <span className="muted" style={{ fontSize: compact ? 10 : 12, overflowWrap: 'anywhere' }}>{controls.join(' · ')}</span>}
  </div>;
}

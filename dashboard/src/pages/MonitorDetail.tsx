import { MonitorTimeline } from '../components/MonitorTimeline';
import { MonitorControls } from '../components/MonitorControls';
import { MonitorActions } from '../components/MonitorActions';
import { Link, useParams, useNavigate } from 'react-router-dom';
import { useDashboardSession } from '../auth/SessionBoundary';
import { useMonitor } from '../hooks/queries';
import { MonitorStatusLabels } from '../components/MonitorStatusLabels';
import { FailureDetails } from '../components/FailureDetails';
import { CodeBadge } from '../components/CodeBadge';
import { ErrorState } from '../components/ErrorState';
import { LoadingSkeleton } from '../components/LoadingSkeleton';
import { Icon } from '../components/Icon';
import { formatDateTime, formatNumber, formatMs, formatMeasuredLatency, formatUptime, uptimeColor } from '../lib/format';
import type { CpraCode } from '../theme/tokens';
import type { ReactNode } from 'react';
import { ApiError } from '../api/client';
import { useOnlineStatus } from '../hooks/connection';

export default function MonitorDetail() {
  const { id } = useParams<{ id: string }>();
  const navigate = useNavigate();
  const session = useDashboardSession();
  const { data: monitor, isLoading, isError, error, refetch } = useMonitor(id);
  const online = useOnlineStatus();

  const backBtn = (
    <button className='btn ghost' onClick={() => navigate('/monitors')}>
      <Icon name='chevron-left' size={16} /> Back to Monitors
    </button>
  );

  const absent = error instanceof ApiError && error.status === 404;
  const denied = error instanceof ApiError && (error.status === 401 || error.status === 403);
  if ((isError || !online) && (!monitor || absent || denied)) {
    const message = !online ? 'You are offline. Reconnect to read this monitor.'
      : absent ? 'This monitor was not found in the current controller projection.'
      : denied ? 'Your current identity cannot read this monitor. Check your connection and permissions.'
      : 'Monitor observations are unavailable. This does not mean the monitor was deleted or recovered.';
    return <div className='page'><h1>Monitor details</h1><div>{backBtn}</div><ErrorState message={message} onRetry={online && !denied ? () => refetch() : undefined} /></div>;
  }
  if (isLoading || !monitor) {
    return <div className='page'><h1>Monitor details</h1><div>{backBtn}</div><div className='card'><LoadingSkeleton lines={6} /></div></div>;
  }

  const m = monitor;
  const activeCodes = m.active_codes ?? [];

  const sampledHealth = m.status === 'unknown' ? undefined : m.uptime;

  return (
    <div className='page'>
      <div className='row' style={{ gap: 8, flexWrap: 'wrap' }}>{backBtn}{m.monitor_id && session.can('GetMonitor') && <Link className='btn' to={`/monitor-configurations/${encodeURIComponent(m.monitor_id)}`}>View saved configuration</Link>}</div>
      {m.monitor_id && session.can('GetMonitor') && session.can('CreateOperation') && <Link className="btn" to={`/import?monitor=${encodeURIComponent(m.monitor_id)}`}>Set up notifications</Link>}

      {(isError || !online) && <p role="alert">Saved monitor observations are shown below. Current health is unavailable{!online ? ' while offline' : ' because the latest request failed'}.</p>}

      <div className='card' style={{ display: 'flex', flexDirection: 'column', gap: 18 }}>
        <div className='spread' style={{ alignItems: 'flex-start', gap: 16, flexWrap: 'wrap' }}>
          <div className='row' style={{ gap: 14, minWidth: 0 }}>
            <span style={{ width: 44, height: 44, borderRadius: 12, display: 'grid', placeItems: 'center', fontSize: 18, color: 'var(--accent)', background: 'var(--accent-soft)', border: '1px solid var(--accent-soft)', textTransform: 'lowercase' }}>{m.pulse_type.slice(0,2)}</span>
            <div style={{ minWidth: 0 }}>
              <h1 style={{ fontSize: 20, fontWeight: 700, letterSpacing: '-0.3px', overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>{m.name}</h1>
              <div className='muted mono' style={{ fontSize: 12.5, marginTop: 4, overflow: 'hidden', textOverflow: 'ellipsis' }}>{m.target || m.pulse_type.toUpperCase()}</div>
            </div>
          </div>
          <div className='row' style={{ gap: 10 }}>
            <MonitorStatusLabels monitor={m} />
          </div>
        </div>

        <div className='grid-4'>
          <KpiStat label='Healthy samples' value={formatUptime(sampledHealth)} accent={uptimeColor(sampledHealth)} />
          <KpiStat label='Latency' value={formatMeasuredLatency(m.latency_available, m.latency_ms)} accent='var(--status-info)' />
          <KpiStat label='Check Interval' value={m.interval_ms ? formatMs(m.interval_ms) : '—'} accent='var(--accent)' />
          <KpiStat label='Consecutive Failures' value={formatNumber(m.consecutive_failures ?? 0)} accent={m.consecutive_failures ? 'var(--status-degraded)' : 'var(--text-muted)'} />
        </div>

        <p className='muted' style={{ fontSize: 12 }}>
          Healthy samples shows the share of recorded health observations that succeeded.
          The event timeline retains incidents and actions for 30 days; raw check history is not retained. This percentage is not an uptime guarantee.
        </p>
	    {m.warning && <p role='status' style={{ color: 'var(--status-degraded)' }}>{m.warning}</p>}
        <FailureDetails diagnostic={m.last_failure} />
		<FailureDetails diagnostic={m.configuration_problem} label="Check configuration problem" />
		{m.configuration_problem && <p>Health and last-check time show the last target observation. Preparation will be retried at the normal check interval; recovery actions are blocked meanwhile.</p>}
        {m.certificate_verification_disabled && <p role='status'>TLS certificate verification disabled for this monitor.</p>}
        <p>Pending notifications: {m.pending_notifications ?? 'Unavailable'} · Accepted (retained): {m.completed_notifications ?? 'Unavailable'} · Failed (retained): {m.failed_notifications ?? 'Unavailable'} · Actions needing review: {m.actions_needing_review ?? 'Unavailable'}</p>
        <p className='muted'>Acceptance records the provider response; it does not prove a recipient received the notification.</p>
      </div>

      {m.monitor_id && <MonitorControls key={`controls/${m.monitor_id}`} monitorID={m.monitor_id} />}
      {m.monitor_id && <MonitorActions key={`actions/${m.monitor_id}`} monitorID={m.monitor_id} />}
      {m.monitor_id && <MonitorTimeline key={m.monitor_id} monitorID={m.monitor_id} />}

      <div className='grid-2'>
        <div className='card'>
          <div className='card-head'><h2 className='card-title'>Timing (local time)</h2></div>
          <div className='stack-12'>
            <Row k='Last check' v={formatDateTime(m.last_check)} />
            <Row k='Last success' v={formatDateTime(m.last_success)} />
            <Row k='Next check' v={formatDateTime(m.next_check)} />
            <Row k='Check type' v={m.pulse_type.toUpperCase()} />
          </div>
        </div>
        <div className='card'>
          <div className='card-head'><h2 className='card-title'>Configured notifications</h2><span className='card-sub'>{activeCodes.length}</span></div>
          {activeCodes.length === 0 ? (
            <div className='empty-state'><Icon name='check' size={20} style={{ color: 'var(--status-operational)' }} /><div>No notifications configured</div></div>
          ) : (
            <div className='stack-12'>
              {activeCodes.map((c) => (
                <div key={c} className='spread'>
                  <CodeBadge code={c as CpraCode} />
                  {m.pending_code === c ? (
                    <span className='muted' style={{ fontSize: 12 }}>pending delivery</span>
                  ) : (
                    <span className='muted' style={{ fontSize: 12 }}>configured</span>
                  )}
                </div>
              ))}
            </div>
          )}
        </div>
      </div>
    </div>
  );
}

function KpiStat({ label, value, accent }: { label: string; value: ReactNode; accent: string }) {
  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: 4 }}>
      <span className='card-sub' style={{ textTransform: 'uppercase', letterSpacing: 0.6 }}>{label}</span>
      <span style={{ fontSize: 22, fontWeight: 700, color: accent, fontVariantNumeric: 'tabular-nums' }}>{value}</span>
    </div>
  );
}

function Row({ k, v }: { k: string; v: string }) {
  return (
    <div className='spread'>
      <span className='muted' style={{ fontSize: 12.5 }}>{k}</span>
      <span className='mono' style={{ fontSize: 13 }}>{v}</span>
    </div>
  );
}

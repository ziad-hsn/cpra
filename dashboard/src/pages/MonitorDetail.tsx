import { MonitorTimeline } from '../components/MonitorTimeline';
import { useParams, useNavigate } from 'react-router-dom';
import { useMonitor } from '../hooks/queries';
import { StatusChip } from '../components/StatusChip';
import { CodeBadge } from '../components/CodeBadge';
import { ErrorState } from '../components/ErrorState';
import { LoadingSkeleton } from '../components/LoadingSkeleton';
import { Icon } from '../components/Icon';
import { formatDateTime, formatNumber, formatMs, formatUptime, uptimeColor } from '../lib/format';
import type { CpraCode } from '../theme/tokens';
import type { ReactNode } from 'react';

export default function MonitorDetail() {
  const { id } = useParams<{ id: string }>();
  const navigate = useNavigate();
  const { data: monitor, isLoading, isError, refetch } = useMonitor(id);

  const backBtn = (
    <button className='btn ghost' onClick={() => navigate('/monitors')}>
      <Icon name='chevron-left' size={16} /> Back to Monitors
    </button>
  );

  if (isError) {
    return <div className='page'><div>{backBtn}</div><ErrorState message={'Monitor ' + id + ' not found'} onRetry={() => refetch()} /></div>;
  }
  if (isLoading || !monitor) {
    return <div className='page'><div>{backBtn}</div><div className='card'><LoadingSkeleton lines={6} /></div></div>;
  }

  const m = monitor;
  const activeCodes = m.active_codes ?? [];

  const sampledHealth = m.status === 'unknown' ? undefined : m.uptime;

  return (
    <div className='page'>
      <div>{backBtn}</div>

      <div className='card' style={{ display: 'flex', flexDirection: 'column', gap: 18 }}>
        <div className='spread' style={{ alignItems: 'flex-start', gap: 16, flexWrap: 'wrap' }}>
          <div className='row' style={{ gap: 14, minWidth: 0 }}>
            <span style={{ width: 44, height: 44, borderRadius: 12, display: 'grid', placeItems: 'center', fontSize: 18, color: 'var(--accent)', background: 'var(--accent-soft)', border: '1px solid var(--accent-soft)', textTransform: 'lowercase' }}>{m.pulse_type.slice(0,2)}</span>
            <div style={{ minWidth: 0 }}>
              <div style={{ fontSize: 20, fontWeight: 700, letterSpacing: '-0.3px', overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>{m.name}</div>
              <div className='muted mono' style={{ fontSize: 12.5, marginTop: 4, overflow: 'hidden', textOverflow: 'ellipsis' }}>{m.target || m.pulse_type.toUpperCase()}</div>
            </div>
          </div>
          <div className='row' style={{ gap: 10 }}>
            <StatusChip status={m.status} />
            {m.incident && <CodeBadge code='red' label='INCIDENT' />}
          </div>
        </div>

        <div className='grid-4'>
          <KpiStat label='Healthy samples' value={formatUptime(sampledHealth)} accent={uptimeColor(sampledHealth)} />
          <KpiStat label='Latency' value={m.latency_available ? formatMs(m.latency_ms ?? 0) : 'Unavailable'} accent='var(--status-info)' />
          <KpiStat label='Check Interval' value={m.interval_ms ? formatMs(m.interval_ms) : '—'} accent='var(--accent)' />
          <KpiStat label='Consecutive Failures' value={formatNumber(m.consecutive_failures ?? 0)} accent={m.consecutive_failures ? 'var(--status-degraded)' : 'var(--text-muted)'} />
        </div>

        <p className='muted' style={{ fontSize: 12 }}>
          Healthy samples shows the share of recorded health observations that succeeded.
          The event timeline retains incidents and actions for 30 days; raw check history is not retained. This percentage is not an uptime guarantee.
        </p>
	    {m.warning && <p role='status' style={{ color: 'var(--status-degraded)' }}>{m.warning}</p>}
      </div>

      {m.monitor_id && <MonitorTimeline key={m.monitor_id} monitorID={m.monitor_id} />}

      <div className='grid-2'>
        <div className='card'>
          <div className='card-head'><span className='card-title'>Timing</span></div>
          <div className='stack-12'>
            <Row k='Last check' v={formatDateTime(m.last_check)} />
            <Row k='Last success' v={formatDateTime(m.last_success)} />
            <Row k='Next check' v={formatDateTime(m.next_check)} />
            <Row k='Pulse type' v={m.pulse_type.toUpperCase()} />
          </div>
        </div>
        <div className='card'>
          <div className='card-head'><span className='card-title'>Alert codes</span><span className='card-sub'>{activeCodes.length}</span></div>
          {activeCodes.length === 0 ? (
            <div className='empty-state'><Icon name='check' size={20} style={{ color: 'var(--status-operational)' }} /><div>No alert codes configured</div></div>
          ) : (
            <div className='stack-12'>
              {activeCodes.map((c) => (
                <div key={c} className='spread'>
                  <CodeBadge code={c as CpraCode} />
                  {m.pending_code === c ? (
                    <span className='muted' style={{ fontSize: 12 }}>pending delivery</span>
                  ) : (
                    <span className='muted' style={{ fontSize: 12 }}>armed</span>
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

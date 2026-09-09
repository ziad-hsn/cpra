import { useEffect, useRef, useState } from 'react';
import { useNavigate } from 'react-router-dom';
import { useOverview, useSystems, useIncidents, useQueues } from '../hooks/queries';
import { KpiTile } from '../components/KpiTile';
import { DonutChart } from '../components/DonutChart';
import { Sparkline } from '../components/Sparkline';
import { ProgressRing } from '../components/ProgressRing';
import { ErrorState } from '../components/ErrorState';
import { LoadingSkeleton } from '../components/LoadingSkeleton';
import { StatusChip } from '../components/StatusChip';
import { Icon } from '../components/Icon';
import { formatNumber, formatPercent, formatDurationNs, timeAgo } from '../lib/format';
import type { MonitorSummary } from '../api/types';

const CODE_COLORS: Record<string, string> = {
  red: 'var(--status-critical)',
  yellow: 'var(--status-degraded)',
  green: 'var(--status-operational)',
  cyan: 'var(--status-info)',
  gray: 'var(--status-disabled)',
};

export default function Overview() {
  const navigate = useNavigate();
  const { data: overview, isLoading, isError, refetch } = useOverview();
  const { data: systems, dataUpdatedAt: systemsUpdatedAt } = useSystems();
  const [throughputHistory, setThroughputHistory] = useState<number[]>([]);
  const lastSampleAt = useRef(0);
  const { data: incidentsData, isLoading: incidentsLoading, isError: incidentsError, refetch: refetchIncidents } = useIncidents();
  const { data: queues } = useQueues();

  const byStatus = overview?.by_status ?? {};
  const incidents = incidentsData?.incidents ?? [];
  const throughput = systems?.aggregate?.entities_per_second ?? 0;
  const epsProcessed = systems?.systems ? Object.values(systems.systems).reduce((a, s) => a + (s.total_entities_processed ?? 0), 0) : 0;

  const donutSegments = [
    { label: 'Unknown', value: byStatus.unknown ?? 0, color: 'var(--status-disabled)' },
    { label: 'Operational', value: byStatus.up ?? 0, color: 'var(--status-operational)' },
    { label: 'Degraded', value: byStatus.degraded ?? 0, color: 'var(--status-degraded)' },
    { label: 'Down', value: byStatus.down ?? 0, color: 'var(--status-critical)' },
    { label: 'Verifying', value: byStatus.verifying ?? 0, color: 'var(--status-info)' },
    { label: 'Incident', value: byStatus.incident ?? 0, color: 'var(--status-critical)' },
    { label: 'Disabled', value: byStatus.disabled ?? 0, color: 'var(--status-disabled)' },
  ];

  const codeEntries = (['red', 'yellow', 'green', 'cyan', 'gray'] as const).map((c) => ({
    code: c, value: overview?.by_code?.[c] ?? 0, color: CODE_COLORS[c],
  }));
  const maxCode = Math.max(...codeEntries.map((s) => s.value), 1);

  useEffect(() => {
    if (!systems || !systemsUpdatedAt || lastSampleAt.current === systemsUpdatedAt) return;
    lastSampleAt.current = systemsUpdatedAt;
    setThroughputHistory((samples) => [...samples.slice(-59), throughput]);
  }, [systems, systemsUpdatedAt, throughput]);

  if (isError) {
    return (
      <div className='page'>
        <ErrorState message='Failed to load fleet overview' onRetry={() => refetch()} />
      </div>
    );
  }

  const upPct = overview?.up_percent ?? 0;
  const total = overview?.total ?? 0;
  const critical = (byStatus.incident ?? 0) + (byStatus.down ?? 0);
  const degraded = byStatus.degraded ?? 0;
  const availFrac = total > 0 ? upPct / 100 : 0;
  const pending = (byStatus.unknown ?? 0) + (byStatus.verifying ?? 0);
  const fleetNote = total === 0 || total === (overview?.disabled ?? 0) ? 'no active monitors'
    : critical > 0 ? formatNumber(critical) + ' need attention'
    : degraded > 0 ? formatNumber(degraded) + ' have health warnings'
    : pending > 0 ? formatNumber(pending) + ' awaiting health confirmation'
    : 'all active monitors operational';

  return (
    <div className='page'>
      <div className='page-head'>
        <div>
          <h1>Fleet Overview</h1>
          <div className='lead'>Latest health snapshot for {formatNumber(total)} monitors</div>
        </div>
      </div>

      {/* KPI strip */}
      <div className='grid-kpis'>
        <KpiTile label='Total Monitors' value={formatNumber(total)} loading={isLoading} sub='registered endpoints' code='gray' icon='layers' />
        <KpiTile label='Operational' value={total > 0 ? formatPercent(upPct) : '—'} loading={isLoading} code='green' icon='check' sub='latest controller snapshot' />
        <KpiTile label='Open Incidents' value={formatNumber(critical)} loading={isLoading} code='red' icon='warning' sub='incident + down' />
        <KpiTile label='Verifying' value={formatNumber(byStatus.verifying ?? 0)} loading={isLoading} code='yellow' icon='clock' sub='awaiting retry' />
        <KpiTile label='Throughput' value={formatNumber(Math.round(throughput))} loading={isLoading} code='cyan' icon='activity' sub='entities / sec' />
      </div>

      {/* Bento row 1: availability ring + status donut + throughput */}
      <div className='bento'>
        <div className='card span-4' style={{ display: 'flex', flexDirection: 'column', alignItems: 'center', justifyContent: 'center', gap: 10 }}>
          <span className='card-title' style={{ alignSelf: 'flex-start' }}>Current Fleet Health</span>
          <ProgressRing value={availFrac} size={128} thickness={12} color='var(--status-operational)' ariaLabel='Currently operational monitors'>
            <div style={{ textAlign: 'center' }}>
              <div style={{ fontSize: 26, fontWeight: 700, lineHeight: 1 }}>{formatPercent(upPct, 1)}</div>
              <div className='muted' style={{ fontSize: 11 }}>operational</div>
            </div>
          </ProgressRing>
          <div className='row muted' style={{ fontSize: 12, gap: 6 }}>
            <span className='status-dot' style={{ background: critical > 0 ? 'var(--status-critical)' : degraded > 0 ? 'var(--status-degraded)' : pending > 0 ? 'var(--status-info)' : total === 0 || total === (overview?.disabled ?? 0) ? 'var(--status-disabled)' : 'var(--status-operational)' }} />
            {fleetNote}
          </div>
        </div>

        <div className='card span-4'>
          <div className='card-head'>
            <span className='card-title'>Status Distribution</span>
            <span className='card-sub'>{formatNumber(total)} total</span>
          </div>
          {isLoading ? <LoadingSkeleton width='70%' lines={2} /> : (
            <DonutChart segments={donutSegments} size={150} thickness={18} centerValue={formatNumber(total)} centerLabel='monitors' ariaLabel='Fleet status by health' />
          )}
        </div>

        <div className='card span-4'>
          <div className='card-head'>
            <span className='card-title'>Throughput</span>
            <span className='card-sub'>last {throughputHistory.length} dashboard refreshes</span>
          </div>
          <div className='col' style={{ gap: 8 }}>
            <div style={{ fontSize: 30, fontWeight: 700, lineHeight: 1 }}>{formatNumber(Math.round(throughput))}<span className='muted' style={{ fontSize: 13, fontWeight: 500 }}> eps</span></div>
            <Sparkline data={throughputHistory} width={320} height={70} color='var(--accent)' fill />
            <div className='muted' style={{ fontSize: 11 }}>{formatNumber(epsProcessed)} entities processed since server start</div>
          </div>
        </div>
      </div>

      {/* Bento row 2: alert codes + top incidents */}
      <div className='bento'>
        <div className='card span-5'>
          <div className='card-head'>
            <span className='card-title'>Active Alert Codes</span>
            <span className='card-sub'>by severity</span>
          </div>
          {isLoading ? <LoadingSkeleton width='80%' lines={5} /> : (
            <div className='col'>
              {codeEntries.map((c) => (
                <div className='bar-row' key={c.code}>
                  <span className='bar-label'>{c.code}</span>
                  <span className='bar-track'><span className='bar-fill' style={{ width: (c.value / maxCode) * 100 + '%', background: c.color, boxShadow: '0 0 12px ' + c.color }} /></span>
                  <span className='bar-value'>{formatNumber(c.value)}</span>
                </div>
              ))}
            </div>
          )}
        </div>

        <div className='card span-7'>
          <div className='card-head'>
            <span className='card-title'>Top Incidents</span>
            <span className='card-sub'>{overview?.index_capped || incidentsError ? 'unavailable' : incidentsLoading ? 'loading…' : `${incidents.length} active`}</span>
          </div>
          {overview?.index_capped ? (
            <div className='empty-state'>Incident details are unavailable because this fleet exceeds the snapshot index limit. Aggregate counts remain available above.</div>
          ) : incidentsError ? (
            <ErrorState message='Failed to load incident details' onRetry={() => refetchIncidents()} />
          ) : incidentsLoading || isLoading ? <LoadingSkeleton width='90%' lines={4} /> : incidents.length === 0 ? (
            <div className='empty-state'><Icon name='check' size={22} style={{ color: 'var(--status-operational)' }} /><div>No active incidents</div></div>
          ) : (
            <table className='data-table'>
              <thead><tr><th>Monitor</th><th>Status</th><th>Last check</th></tr></thead>
              <tbody>
                {incidents.slice(0, 6).map((m: MonitorSummary) => (
                  <tr key={m.id} className='clickable' onClick={() => navigate('/monitors/' + m.id)}>
                    <td><div className='row'><span className='status-dot' style={{ background: 'var(--status-critical)' }} />{m.name}</div></td>
                    <td><StatusChip status={m.status} /></td>
                    <td className='muted'>{m.last_check ? timeAgo(m.last_check) : '—'}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          )}
        </div>
      </div>

      {/* Pipeline backpressure */}
      {queues && (
        <div className='card span-12'>
          <div className='card-head'>
            <span className='card-title'>Pipeline Backpressure</span>
            <span className='card-sub'>queue depth vs capacity</span>
          </div>
          <div className='grid-3'>
            {(['pulse', 'intervention', 'code'] as const).map((name) => {
              const q = queues[name];
              if (!q) return null;
              const pct = q.capacity > 0 ? Math.min(100, (q.queue_depth / q.capacity) * 100) : 0;
              const hot = pct > 60;
              return (
                <div key={name} className='col' style={{ gap: 6 }}>
                  <span className='card-title' style={{ textTransform: 'capitalize' }}>{name} queue</span>
                  <div className='row' style={{ alignItems: 'baseline', gap: 8 }}>
                    <span style={{ fontSize: 26, fontWeight: 700 }}>{formatNumber(q.queue_depth)}</span>
                    <span className='muted' style={{ fontSize: 12 }}>/ {formatNumber(q.capacity)}</span>
                  </div>
                  <span className='bar-track'><span className='bar-fill' style={{ width: pct + '%', background: hot ? 'var(--status-degraded)' : 'var(--accent)' }} /></span>
                  <span className='muted' style={{ fontSize: 11 }}>avg wait {formatDurationNs(q.avg_queue_time)} · {formatNumber(q.dropped)} dropped</span>
                </div>
              );
            })}
          </div>
        </div>
      )}
    </div>
  );
}

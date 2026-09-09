import { useNavigate } from 'react-router-dom';
import { useIncidents } from '../hooks/queries';
import { KpiTile } from '../components/KpiTile';
import { StatusChip } from '../components/StatusChip';
import { CodeBadge } from '../components/CodeBadge';
import { ErrorState } from '../components/ErrorState';
import { EmptyState } from '../components/EmptyState';
import { LoadingSkeleton } from '../components/LoadingSkeleton';
import { formatNumber, timeAgo } from '../lib/format';
import type { MonitorSummary, MonitorStatus } from '../api/types';
import type { CpraCode } from '../theme/tokens';

export default function Alerts() {
  const navigate = useNavigate();
  const { data, isLoading, isError, refetch } = useIncidents();

  const incidents = data?.incidents ?? [];
  const count = data?.count ?? 0;

  const redCount = incidents.filter((m) => m.pending_code === 'red' || m.status === 'down' || m.status === 'incident').length;
  const yellowCount = incidents.filter((m) => m.pending_code === 'yellow').length;
  const greenCount = incidents.filter((m) => m.pending_code === 'green').length;

  const validCodes: CpraCode[] = ['red', 'yellow', 'green', 'cyan', 'gray'];
  const isValidCode = (c: string | null | undefined): c is CpraCode =>
    !!c && (validCodes as string[]).includes(c);

  return (
    <div className="page">
      <div className="page-head">
        <div>
          <h1>Alerts &amp; Incidents</h1>
          <div className="lead">Open incidents and their pending notifications</div>
        </div>
      </div>

      {/* Summary bar */}
      <div className="grid-kpis">
        <KpiTile
          label="Open Incidents"
          value={isError || isLoading ? '—' : formatNumber(count)}
          code={isError || isLoading ? 'gray' : count > 0 ? 'red' : 'green'}
          icon="warning"
          sub={isError ? 'unavailable' : isLoading ? 'loading' : count > 0 ? 'requires attention' : 'no open incidents'}
        />
        <KpiTile
          label="Critical (Red)"
          value={isError || isLoading ? '—' : formatNumber(redCount)}
          code="red"
          icon="warning"
          sub="pending red code"
        />
        <KpiTile
          label="Degraded (Yellow)"
          value={isError || isLoading ? '—' : formatNumber(yellowCount)}
          code="yellow"
          icon="clock"
          sub="pending yellow code"
        />
        <KpiTile
          label="Recovering (Green)"
          value={isError || isLoading ? '—' : formatNumber(greenCount)}
          code="green"
          icon="check"
          sub="pending green code"
        />
      </div>

      <div className="card" style={{ padding: 0, overflow: 'hidden' }}>
        {isError ? (
          <div style={{ padding: 16 }}><ErrorState message="Failed to load incidents" onRetry={() => refetch()} /></div>
        ) : isLoading ? (
          <div style={{ padding: 16 }}><LoadingSkeleton lines={6} /></div>
        ) : incidents.length === 0 ? (
          <div style={{ padding: 16 }}>
            <EmptyState title="No active incidents" message="No incidents are open. Check the monitor fleet for current health and checks awaiting results." />
          </div>
        ) : (
          <table className="data-table">
            <thead>
              <tr>
                <th>Severity</th>
                <th>Monitor</th>
                <th>Status</th>
                <th style={{ textAlign: 'right' }}>Failures</th>
                <th>Pending Code</th>
                <th>Last Check</th>
              </tr>
            </thead>
            <tbody>
              {incidents.map((m: MonitorSummary) => (
                <tr
                  key={m.id}
                  className="clickable"
                  onClick={() => navigate(`/monitors/${m.id}`)}
                >
                  <td>
                    {isValidCode(m.pending_code) ? (
                      <CodeBadge code={m.pending_code} />
                    ) : (
                      <span className="muted">—</span>
                    )}
                  </td>
                  <td className="mono">{m.name}</td>
                  <td><StatusChip status={m.status as MonitorStatus} /></td>
                  <td style={{ textAlign: 'right', color: m.consecutive_failures > 0 ? 'var(--status-degraded)' : undefined, fontWeight: m.consecutive_failures > 0 ? 600 : 400 }}>{m.consecutive_failures}</td>
                  <td>{m.pending_code ? m.pending_code.toUpperCase() : '—'}</td>
                  <td className="muted" style={{ fontSize: 12 }}>{timeAgo(m.last_check)}</td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </div>
    </div>
  );
}

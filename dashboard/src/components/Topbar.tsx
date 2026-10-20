import { useEffect, useState } from 'react';
import { useLocation } from 'react-router-dom';
import { useOverview } from '../hooks/queries';
import { useTheme } from '../theme/useTheme';
import { Icon } from './Icon';
import { useOnlineStatus } from '../hooks/connection';
import { formatDateTime } from '../lib/format';

const TITLES: Record<string, { title: string; crumb: string }> = {
  '/': { title: 'Fleet Overview', crumb: 'Workspace / Overview' },
  '/monitors': { title: 'Monitors', crumb: 'Workspace / Monitors' },
  '/alerts': { title: 'Alerts & Incidents', crumb: 'Workspace / Alerts' },
  '/system': { title: 'System Health', crumb: 'Workspace / System' },
  '/settings': { title: 'Settings', crumb: 'Workspace / Settings' },
  '/monitor-configurations': { title: 'Monitor configurations', crumb: 'Workspace / Configuration' },
  '/operations': { title: 'Operation progress', crumb: 'Workspace / Configuration' },
  '/recipients': { title: 'Recipients', crumb: 'Workspace / Notifications' },
  '/notification-endpoints': { title: 'Notification endpoints', crumb: 'Workspace / Notifications' },
  '/notification-groups': { title: 'Notification groups', crumb: 'Workspace / Notifications' },
  '/secrets': { title: 'Secrets', crumb: 'Workspace / Configuration' },
};

export function Topbar() {
  const { data: overview, isLoading, isError, dataUpdatedAt, fetchStatus } = useOverview();
  const online = useOnlineStatus();
  const [now, setNow] = useState(() => Date.now());
  useEffect(() => {
    const timer = window.setInterval(() => setNow(Date.now()), 5000);
    return () => window.clearInterval(timer);
  }, []);
  const { theme, toggleTheme } = useTheme();
  const { pathname } = useLocation();

  const meta = TITLES[pathname] ?? TITLES[`/${pathname.split('/')[1]}`] ?? { title: 'CPRa', crumb: 'Workspace' };
  useEffect(() => { document.title = `${meta.title} · CPRa`; }, [meta.title]);

  const incidentCount = overview?.by_status?.incident ?? 0;
  const downCount = overview?.by_status?.down ?? 0;
  const critical = incidentCount + downCount;
  const unknown = overview?.by_status?.unknown ?? 0;
  const verifying = overview?.by_status?.verifying ?? 0;
  const degraded = overview?.by_status?.degraded ?? 0;
  const active = (overview?.total ?? 0) - (overview?.disabled ?? 0);
  const offline = !online || fetchStatus === 'paused';
  const projectedAt = overview?.generated ? Date.parse(overview.generated) : NaN;
  const projectionUncertain = Boolean(overview && (!Number.isFinite(projectedAt) || projectedAt > now + 30000));
  const stale = Boolean(overview && (dataUpdatedAt && now - dataUpdatedAt > 30000 || Number.isFinite(projectedAt) && now - projectedAt > 30000));
  const unavailable = offline || isError || stale || projectionUncertain;
  const ok = !isLoading && !unavailable && active > 0 && critical === 0 && degraded === 0 && unknown === 0 && verifying === 0;
  const statusText = offline ? 'Offline — current health unavailable'
    : stale ? 'Observations are stale — current health unavailable'
    : projectionUncertain ? 'Projection freshness unavailable — current health unavailable'
    : isError ? 'Status unavailable'
    : isLoading ? 'Loading fleet status…'
    : active === 0 ? 'No active monitors'
    : critical > 0 ? `${critical} monitor${critical !== 1 ? 's' : ''} critical — investigate`
    : degraded > 0 ? `${degraded} monitor${degraded !== 1 ? 's' : ''} degraded — review warnings`
    : unknown > 0 ? 'Awaiting first check results'
    : verifying > 0 ? `${verifying} monitor${verifying !== 1 ? 's' : ''} verifying`
    : 'All systems operational';

  return (
    <header className="topbar">
      <div className="topbar-title">
        <strong>{meta.title}</strong>
        <span className="crumb">{meta.crumb}</span>
      </div>

      <div
        className={`status-banner ${ok ? 'ok' : !unavailable && critical > 0 ? 'critical' : 'pending'}`}
        role="status"
        aria-label="Global fleet status"
      >
        <span className="dot" aria-hidden />
        <span>
          {statusText}
          {unavailable && overview && <small className="stale-note"> Saved observations remain visible. Last received {dataUpdatedAt ? formatDateTime(new Date(dataUpdatedAt).toISOString()) : 'at an unavailable time'}. Controller snapshot {Number.isFinite(projectedAt) ? formatDateTime(overview.generated) : 'time unavailable'}.</small>}
        </span>
      </div>

      <div className="topbar-spacer" />

      <button className="icon-btn" onClick={toggleTheme} aria-label="Toggle theme" title="Toggle theme">
        <Icon name={theme === 'dark' ? 'sun' : 'moon'} size={18} />
      </button>
    </header>
  );
}

import { useLocation } from 'react-router-dom';
import { useOverview } from '../hooks/queries';
import { useTheme } from '../theme/useTheme';
import { Icon } from './Icon';

const TITLES: Record<string, { title: string; crumb: string }> = {
  '/': { title: 'Fleet Overview', crumb: 'Workspace / Overview' },
  '/monitors': { title: 'Monitors', crumb: 'Workspace / Monitors' },
  '/alerts': { title: 'Alerts & Incidents', crumb: 'Workspace / Alerts' },
  '/system': { title: 'System Health', crumb: 'Workspace / System' },
  '/settings': { title: 'Settings', crumb: 'Workspace / Settings' },
};

export function Topbar() {
  const { data: overview, isLoading, isError } = useOverview();
  const { theme, toggleTheme } = useTheme();
  const { pathname } = useLocation();

  const meta = TITLES[pathname] ?? { title: 'CPRA', crumb: 'Workspace' };

  const incidentCount = overview?.by_status?.incident ?? 0;
  const downCount = overview?.by_status?.down ?? 0;
  const critical = incidentCount + downCount;
  const unknown = overview?.by_status?.unknown ?? 0;
  const verifying = overview?.by_status?.verifying ?? 0;
  const degraded = overview?.by_status?.degraded ?? 0;
  const active = (overview?.total ?? 0) - (overview?.disabled ?? 0);
  const ok = !isLoading && !isError && active > 0 && critical === 0 && degraded === 0 && unknown === 0 && verifying === 0;
  const statusText = isLoading ? 'Loading fleet status…'
    : isError ? 'Status unavailable'
    : active === 0 ? 'No active monitors'
    : critical > 0 ? `${critical} monitor${critical !== 1 ? 's' : ''} critical — investigate`
    : degraded > 0 ? `${degraded} monitor${degraded !== 1 ? 's' : ''} degraded — review warnings`
    : unknown > 0 ? 'Awaiting first check results'
    : verifying > 0 ? `${verifying} monitor${verifying !== 1 ? 's' : ''} verifying`
    : 'All systems operational';

  return (
    <header className="topbar">
      <div className="topbar-title">
        <h1>{meta.title}</h1>
        <span className="crumb">{meta.crumb}</span>
      </div>

      <div
        className={`status-banner ${ok ? 'ok' : critical > 0 ? 'critical' : 'pending'}`}
        role="banner"
        aria-label="Global fleet status"
      >
        <span className="dot" aria-hidden />
        <span>
          {statusText}
        </span>
      </div>

      <div className="topbar-spacer" />

      <button className="icon-btn" onClick={toggleTheme} aria-label="Toggle theme" title="Toggle theme">
        <Icon name={theme === 'dark' ? 'sun' : 'moon'} size={18} />
      </button>
    </header>
  );
}

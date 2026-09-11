import type { ReactNode } from 'react';
import { Icon, type IconName } from './Icon';

interface KpiTileProps {
  label: string;
  value: ReactNode;
  sub?: ReactNode;
  loading?: boolean;
  code?: 'red' | 'yellow' | 'green' | 'cyan' | 'gray';
  icon?: IconName;
  trend?: { dir: 'up' | 'down' | 'flat'; label: string };
}

const ACCENT: Record<NonNullable<KpiTileProps['code']>, string> = {
  red: 'var(--status-critical)',
  yellow: 'var(--status-degraded)',
  green: 'var(--status-operational)',
  cyan: 'var(--status-info)',
  gray: 'var(--status-disabled)',
};

export function KpiTile({ label, value, sub, loading, code, icon, trend }: KpiTileProps) {
  const accent = code ? ACCENT[code] : 'var(--accent)';
  return (
    <div
      className="kpi-tile"
      style={{ ['--kpi-accent' as string]: accent }}
    >
      <div className="spread" style={{ marginBottom: 2 }}>
        <span className="kpi-label">{label}</span>
        {icon && <Icon name={icon} size={16} style={{ color: accent, opacity: 0.85 }} />}
      </div>
      {loading ? (
        <div className="skeleton" style={{ height: 30, width: '62%', marginTop: 6 }} />
      ) : (
        <div className="kpi-value">{value}</div>
      )}
      <div className="kpi-sub">
        {trend && !loading && (
          <span className={`kpi-trend ${trend.dir}`}>
            <Icon name={trend.dir === 'up' ? 'arrow-up' : trend.dir === 'down' ? 'arrow-down' : 'activity'} size={12} strokeWidth={2.2} />
            {trend.label}
          </span>
        )}
        {sub && <span>{sub}</span>}
      </div>
    </div>
  );
}

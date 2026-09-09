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

const ACCENT: Record<NonNullable<KpiTileProps['code']>, { color: string; glow: string }> = {
  red: { color: 'var(--status-critical)', glow: 'rgba(255,84,112,0.35)' },
  yellow: { color: 'var(--status-degraded)', glow: 'rgba(255,204,77,0.35)' },
  green: { color: 'var(--status-operational)', glow: 'rgba(52,211,153,0.35)' },
  cyan: { color: 'var(--status-info)', glow: 'rgba(56,189,248,0.35)' },
  gray: { color: 'var(--status-disabled)', glow: 'rgba(100,116,139,0.30)' },
};

export function KpiTile({ label, value, sub, loading, code, icon, trend }: KpiTileProps) {
  const accent = code ? ACCENT[code] : { color: 'var(--accent)', glow: 'var(--accent-glow)' };
  return (
    <div
      className="kpi-tile"
      style={{ ['--kpi-accent' as string]: accent.color, ['--kpi-glow' as string]: accent.glow }}
    >
      <div className="spread" style={{ marginBottom: 2 }}>
        <span className="kpi-label">{label}</span>
        {icon && <Icon name={icon} size={16} style={{ color: accent.color, opacity: 0.85 }} />}
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

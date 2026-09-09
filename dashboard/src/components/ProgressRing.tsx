import type { ReactNode } from 'react';

interface ProgressRingProps {
  /** 0..1 fraction. */
  value: number;
  size?: number;
  thickness?: number;
  color?: string;
  trackColor?: string;
  ariaLabel?: string;
  children?: ReactNode;
}

export function ProgressRing({ value, size = 96, thickness = 10, color = 'var(--status-operational)', trackColor = 'var(--surface-2)', ariaLabel, children }: ProgressRingProps) {
  const pct = Math.max(0, Math.min(1, value));
  const r = (size - thickness) / 2;
  const c = size / 2;
  const circ = 2 * Math.PI * r;
  const dash = pct * circ;
  return (
    <div style={{ position: 'relative', width: size, height: size }} role='img' aria-label={ariaLabel}>
      <svg width={size} height={size}>
        <circle cx={c} cy={c} r={r} fill='none' stroke={trackColor} strokeWidth={thickness} />
        <circle cx={c} cy={c} r={r} fill='none' stroke={color} strokeWidth={thickness} strokeLinecap='round' strokeDasharray={dash + ' ' + circ} transform={'rotate(-90 ' + c + ' ' + c + ')'} style={{ filter: 'drop-shadow(0 0 5px ' + color + ')', transition: 'strokeDasharray var(--dur-slow) var(--ease)' }} />
      </svg>
      {children && (
        <div style={{ position: 'absolute', inset: 0, display: 'grid', placeItems: 'center' }}>
          {children}
        </div>
      )}
    </div>
  );
}

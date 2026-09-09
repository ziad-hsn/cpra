import type { CSSProperties } from 'react';

/**
 * CPRA icon set — crisp inline SVGs on a 24px grid, 1.75 stroke.
 * Replaces the earlier emoji icons with a consistent, professional glyph system.
 */
export type IconName =
  | 'overview' | 'monitors' | 'alerts' | 'system' | 'settings'
  | 'pulse' | 'search' | 'sun' | 'moon' | 'chevron-left' | 'chevron-right'
  | 'chevron-down' | 'refresh' | 'external' | 'activity' | 'check'
  | 'warning' | 'clock' | 'gauge' | 'layers' | 'filter' | 'arrow-up' | 'arrow-down';

const PATHS: Record<IconName, JSX.Element> = {
  overview: (<><path d="M3 13h8V3H3v10Zm0 8h8v-6H3v6Zm10 0h8V9h-8v12Zm0-18v4h8V3h-8Z" /></>),
  monitors: (<><path d="M2 12h4l3-8 4 16 3-8h6" /></>),
  alerts: (<><path d="M12 3a6 6 0 0 0-6 6c0 5-2 6-2 6h16s-2-1-2-6a6 6 0 0 0-6-6Z" /><path d="M10 20a2 2 0 0 0 4 0" /></>),
  system: (<><rect x="3" y="3" width="18" height="6" rx="1.5" /><rect x="3" y="13" width="18" height="8" rx="1.5" /><path d="M7 6h.01M7 17h.01" /></>),
  settings: (<><path d="M4 6h10M18 6h2M4 12h2M10 12h10M4 18h7M15 18h5" /><circle cx="16" cy="6" r="2" /><circle cx="8" cy="12" r="2" /><circle cx="13" cy="18" r="2" /></>),
  pulse: (<><path d="M3 12h4l2-6 3 14 2-8 2 4h5" /></>),
  search: (<><circle cx="11" cy="11" r="7" /><path d="m20 20-3.5-3.5" /></>),
  sun: (<><circle cx="12" cy="12" r="4" /><path d="M12 2v2M12 20v2M4.9 4.9l1.4 1.4M17.7 17.7l1.4 1.4M2 12h2M20 12h2M4.9 19.1l1.4-1.4M17.7 6.3l1.4-1.4" /></>),
  moon: (<><path d="M21 12.8A9 9 0 1 1 11.2 3a7 7 0 0 0 9.8 9.8Z" /></>),
  'chevron-left': (<><path d="m15 18-6-6 6-6" /></>),
  'chevron-right': (<><path d="m9 18 6-6-6-6" /></>),
  'chevron-down': (<><path d="m6 9 6 6 6-6" /></>),
  refresh: (<><path d="M3 12a9 9 0 0 1 15-6.7L21 8M21 3v5h-5" /><path d="M21 12a9 9 0 0 1-15 6.7L3 16M3 21v-5h5" /></>),
  external: (<><path d="M14 3h7v7M21 3l-9 9M19 14v5a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2V7a2 2 0 0 1 2-2h5" /></>),
  activity: (<><path d="M3 12h4l3-8 4 16 3-8h4" /></>),
  check: (<><path d="M20 6 9 17l-5-5" /></>),
  warning: (<><path d="M12 3 2 20h20L12 3Z" /><path d="M12 10v5M12 18h.01" /></>),
  clock: (<><circle cx="12" cy="12" r="9" /><path d="M12 7v5l3 2" /></>),
  gauge: (<><path d="M12 13a3 3 0 1 0-3-3" /><path d="M3.5 18a9 9 0 1 1 17 0" /><path d="m12 13 4-3" /></>),
  layers: (<><path d="m12 3 9 5-9 5-9-5 9-5Z" /><path d="m3 13 9 5 9-5M3 17l9 5 9-5" /></>),
  filter: (<><path d="M3 5h18l-7 8v6l-4 2v-8L3 5Z" /></>),
  'arrow-up': (<><path d="M12 19V5M5 12l7-7 7 7" /></>),
  'arrow-down': (<><path d="M12 5v14M19 12l-7 7-7-7" /></>),
};

interface IconProps {
  name: IconName;
  size?: number;
  className?: string;
  style?: CSSProperties;
  strokeWidth?: number;
}

export function Icon({ name, size = 18, className, style, strokeWidth = 1.75 }: IconProps) {
  return (
    <svg
      className={className}
      style={style}
      width={size}
      height={size}
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      strokeWidth={strokeWidth}
      strokeLinecap="round"
      strokeLinejoin="round"
      aria-hidden="true"
      focusable="false"
    >
      {PATHS[name]}
    </svg>
  );
}

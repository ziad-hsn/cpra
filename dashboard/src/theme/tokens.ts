/** CPRA code color → status label mapping (color always paired with text label). */
export type CpraCode = 'red' | 'yellow' | 'green' | 'cyan' | 'gray';

export const CODE_LABELS: Record<CpraCode, string> = {
  red: 'CRITICAL',
  yellow: 'DEGRADED',
  green: 'RECOVERED',
  cyan: 'RESTORED',
  gray: 'MAINTENANCE',
};

/** Map a monitor status string to a CPRA code (for coloring). */
export type MonitorStatus = 'up' | 'down' | 'degraded' | 'verifying' | 'incident' | 'disabled' | 'unknown';

export const STATUS_TO_CODE: Record<MonitorStatus, CpraCode> = {
	degraded: 'yellow',
  up: 'green',
  down: 'red',
  verifying: 'cyan',
  incident: 'red',
  disabled: 'gray',
  unknown: 'gray',
};

export const STATUS_LABELS: Record<MonitorStatus, string> = {
	degraded: 'DEGRADED',
  up: 'OPERATIONAL',
  down: 'CRITICAL',
  verifying: 'VERIFYING',
  incident: 'INCIDENT',
  disabled: 'DISABLED',
  unknown: 'UNKNOWN',
};

/** Typography tokens (shared across themes). */
export const typography = {
  fontFamily: {
    sans: 'system-ui, -apple-system, BlinkMacSystemFont, "Segoe UI", sans-serif',
    heading: '"Roboto Slab", Georgia, serif',
    mono: '"SF Mono", "JetBrains Mono", "Fira Code", Menlo, Consolas, monospace',
  },
  fontSize: { xs: '12px', sm: '14px', md: '16px', lg: '20px', xl: '28px', display: '40px' },
  fontWeight: { regular: 400, medium: 500, semibold: 600, bold: 700 },
} as const;

export const spacing = {
  '1': '4px', '2': '8px', '3': '12px', '4': '16px',
  '6': '24px', '8': '32px', '12': '48px', '16': '64px',
} as const;

export const radius = { small: '4px', medium: '8px', large: '12px', pill: '9999px' } as const;

export const density = {
  compact: { controlHeight: 28, rowHeight: 36, cardPadding: 12, tableCellPaddingX: 8, tableCellPaddingY: 6, sectionGap: 12 },
  comfortable: { controlHeight: 36, rowHeight: 48, cardPadding: 16, tableCellPaddingX: 12, tableCellPaddingY: 10, sectionGap: 16 },
} as const;

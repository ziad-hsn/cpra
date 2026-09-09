/** Theme colors used by dashboard components. */

export interface StatusColors {
  critical: string;
  degraded: string;
  operational: string;
  info: string;
  disabled: string;
}

export interface ThemeTokens {
  status: StatusColors;
  statusBg: StatusColors;
  surface: { '0': string; '1': string; '2': string; '3': string };
  text: { primary: string; secondary: string; muted: string; inverse: string };
  border: { default: string; strong: string };
  action: { primary: string; primaryHover: string; primaryActive: string };
}

export const darkTokens: ThemeTokens = {
  status: {
    critical: '#ff4d4f',
    degraded: '#fadb14',
    operational: '#52c41a',
    info: '#13c2c2',
    disabled: '#8c8c8c',
  },
  statusBg: {
    critical: '#3d1417',
    degraded: '#3d3410',
    operational: '#11260c',
    info: '#0e2a2a',
    disabled: '#1f1f1f',
  },
  surface: { '0': '#0b0e14', '1': '#111722', '2': '#1a212e', '3': '#222b3a' },
  text: { primary: '#e6edf3', secondary: '#9aa7b8', muted: '#5c6b7a', inverse: '#0b0e14' },
  border: { default: '#2a3342', strong: '#3a4658' },
  action: { primary: '#2f81f7', primaryHover: '#4a96ff', primaryActive: '#1d6fe0' },
};

export const lightTokens: ThemeTokens = {
  status: {
    critical: '#cf1322',
    degraded: '#d48806',
    operational: '#389e0d',
    info: '#08979c',
    disabled: '#8c8c8c',
  },
  statusBg: {
    critical: '#fff1f0',
    degraded: '#fffbe6',
    operational: '#f6ffed',
    info: '#e6fffb',
    disabled: '#f5f5f5',
  },
  surface: { '0': '#f0f2f5', '1': '#ffffff', '2': '#ffffff', '3': '#ffffff' },
  text: { primary: '#1f2329', secondary: '#4e5969', muted: '#86909c', inverse: '#ffffff' },
  border: { default: '#d9d9d9', strong: '#bfbfbf' },
  action: { primary: '#2f81f7', primaryHover: '#1d6fe0', primaryActive: '#155bb5' },
};

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
    sans: '-apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, Helvetica, Arial, sans-serif',
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

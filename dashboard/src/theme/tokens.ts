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
    critical: '#ff5470',
    degraded: '#ffcc4d',
    operational: '#34d399',
    info: '#38bdf8',
    disabled: '#a19d95',
  },
  statusBg: {
    critical: 'rgba(255, 84, 112, 0.10)',
    degraded: 'rgba(255, 204, 77, 0.10)',
    operational: 'rgba(52, 211, 153, 0.10)',
    info: 'rgba(56, 189, 248, 0.10)',
    disabled: 'rgba(100, 116, 139, 0.10)',
  },
  surface: { '0': '#15161a', '1': '#1c1d22', '2': '#24252b', '3': '#2e3037' },
  text: { primary: '#f4f2ee', secondary: '#c3c0b9', muted: '#a19d95', inverse: '#15161a' },
  border: { default: '#34353b', strong: '#51525a' },
  action: { primary: '#e6bc61', primaryHover: '#f0cd83', primaryActive: '#d6a541' },
};

export const lightTokens: ThemeTokens = {
  status: {
    critical: '#bd223c',
    degraded: '#965b00',
    operational: '#18704a',
    info: '#156e96',
    disabled: '#6d685f',
  },
  statusBg: {
    critical: 'rgba(225, 29, 72, 0.08)',
    degraded: 'rgba(217, 119, 6, 0.08)',
    operational: 'rgba(5, 150, 105, 0.08)',
    info: 'rgba(2, 132, 199, 0.08)',
    disabled: 'rgba(100, 116, 139, 0.08)',
  },
  surface: { '0': '#fbf3df', '1': '#fffaf0', '2': '#f3e8cb', '3': '#ebddba' },
  text: { primary: '#1a262e', secondary: '#4a5560', muted: '#6d685f', inverse: '#fbf3df' },
  border: { default: '#e0d3b2', strong: '#bcaa7d' },
  action: { primary: '#875f10', primaryHover: '#6b490c', primaryActive: '#725218' },
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

/** Formatting utilities for CPRA dashboard. Durations are Go time.Duration nanoseconds. */

export function formatNumber(n: number): string {
  return new Intl.NumberFormat('en-US').format(n);
}

export function formatPercent(n: number, digits = 1): string {
  return `${n.toFixed(digits)}%`;
}

/** Convert Go time.Duration nanoseconds to a human-readable string. */
export function formatDurationNs(ns: number): string {
  if (!ns || ns <= 0) return '0ms';
  const s = ns / 1e9;
  if (s < 0.001) return `${ns.toFixed(0)}ns`;
  if (s < 1) return `${(s * 1000).toFixed(1)}ms`;
  if (s < 60) return `${s.toFixed(2)}s`;
  if (s < 3600) return `${(s / 60).toFixed(1)}m`;
  return `${(s / 3600).toFixed(1)}h`;
}

export function formatDateTime(iso: string): string {
  if (!iso || iso.startsWith('0001-01-01')) return '—';
  try {
    const d = new Date(iso);
    if (isNaN(d.getTime())) return '—';
    return d.toLocaleString('en-US', {
      year: 'numeric',
      month: 'short',
      day: '2-digit',
      hour: '2-digit',
      minute: '2-digit',
      second: '2-digit',
      hour12: false,
    });
  } catch {
    return '—';
  }
}

export function formatTime(iso: string): string {
  if (!iso || iso.startsWith('0001-01-01')) return '—';
  try {
    const d = new Date(iso);
    if (isNaN(d.getTime())) return '—';
    return d.toLocaleTimeString('en-US', { hour: '2-digit', minute: '2-digit', second: '2-digit', hour12: false });
  } catch {
    return '—';
  }
}

export function timeAgo(iso: string): string {
  if (!iso || iso.startsWith('0001-01-01')) return '—';
  try {
    const d = new Date(iso);
    const diff = Date.now() - d.getTime();
    if (diff < 0) return 'in the future';
    const s = Math.floor(diff / 1000);
    if (s < 60) return `${s}s ago`;
    const m = Math.floor(s / 60);
    if (m < 60) return `${m}m ago`;
    const h = Math.floor(m / 60);
    if (h < 24) return `${h}h ago`;
    return `${Math.floor(h / 24)}d ago`;
  } catch {
    return '—';
  }
}

/** Format a duration given in milliseconds (interval/latency), compact. */
export function formatMs(ms: number): string {
  if (!ms || ms <= 0) return '0ms';
  if (ms < 1) return '<1ms';
  if (ms < 1000) return ms.toFixed(0) + 'ms';
  const s = ms / 1000;
  if (s < 60) return (Math.round(s * 10) / 10) + 's';
  const m = s / 60;
  if (m < 60) return (Math.round(m * 10) / 10) + 'm';
  return (Math.round(m / 60 * 10) / 10) + 'h';
}

/** Format an uptime fraction (0..1) as a percentage string. */
export function formatUptime(frac: number | undefined): string {
  if (frac === undefined || frac === null || isNaN(frac)) return '—';
  const pct = frac * 100;
  return pct >= 99.95 ? '100%' : pct.toFixed(1) + '%';
}

/** Pick a status color for an uptime fraction. */
export function uptimeColor(frac: number | undefined): string {
  if (frac === undefined || frac === null || isNaN(frac)) return 'var(--status-disabled)';
  if (frac >= 0.99) return 'var(--status-operational)';
  if (frac >= 0.95) return 'var(--status-info)';
  if (frac >= 0.9) return 'var(--status-degraded)';
  return 'var(--status-critical)';
}


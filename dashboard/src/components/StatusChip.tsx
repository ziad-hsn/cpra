import { STATUS_LABELS } from '../theme/tokens';
import type { MonitorStatus } from '../api/types';

export function StatusChip({ status, label: override }: { status: MonitorStatus; label?: string }) {
  const label = override ?? STATUS_LABELS[status] ?? status.toUpperCase();
  return (
    <span className={`status-chip ${status}`}>
      <span className="status-dot" aria-hidden="true" />
      {label}
    </span>
  );
}

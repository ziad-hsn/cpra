import type { ReactNode } from 'react';

export function EmptyState({ title, message, action }: { title: string; message?: string; action?: ReactNode }) {
  return (
    <div className="empty-state" role="status">
      <span style={{ fontSize: 24 }} aria-hidden>
        ✓
      </span>
      <span style={{ fontSize: 14, fontWeight: 600, color: 'var(--text-secondary)' }}>{title}</span>
      {message && <span style={{ fontSize: 12 }}>{message}</span>}
      {action && <div style={{ marginTop: 8 }}>{action}</div>}
    </div>
  );
}

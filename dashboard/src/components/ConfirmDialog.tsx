import { useId, useRef, type ReactNode } from 'react';
import { Dialog } from './Dialog';

export function ConfirmDialog({ title, children, pending, onCancel, onConfirm, confirmDisabled = false, cancelLabel = 'Keep resource', confirmLabel = 'Confirm deletion', pendingLabel = 'Deleting…' }: {
  title: string; children: ReactNode; pending: boolean; onCancel: () => void; onConfirm: () => void;
  confirmDisabled?: boolean; cancelLabel?: string; confirmLabel?: string; pendingLabel?: string;
}) {
  const titleID = useId();
  const cancel = useRef<HTMLButtonElement>(null);
  return <Dialog role="alertdialog" labelledBy={titleID} pending={pending} initialFocus={cancel} onCancel={onCancel}><div className="card"><h2 id={titleID}>{title}</h2>{children}<div className="row" style={{ gap: 12 }}>
    <button ref={cancel} type="button" className="btn" disabled={pending} onClick={onCancel}>{cancelLabel}</button>
    <button type="button" className="btn" disabled={pending || confirmDisabled} onClick={onConfirm}>{pending ? pendingLabel : confirmLabel}</button>
  </div></div></Dialog>;
}

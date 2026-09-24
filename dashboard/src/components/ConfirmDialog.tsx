import { useEffect, useId, useRef, type ReactNode } from 'react';

export function ConfirmDialog({ title, children, pending, onCancel, onConfirm, cancelLabel = 'Keep resource', confirmLabel = 'Confirm deletion', pendingLabel = 'Deleting…' }: {
  title: string; children: ReactNode; pending: boolean; onCancel: () => void; onConfirm: () => void;
  cancelLabel?: string; confirmLabel?: string; pendingLabel?: string;
}) {
  const titleID = useId();
  const cancel = useRef<HTMLButtonElement>(null);
  useEffect(() => {
    const previous = document.activeElement;
    cancel.current?.focus();
    return () => { if (previous instanceof HTMLElement && previous.isConnected) previous.focus(); };
  }, []);
  return <div className="management-modal" role="alertdialog" aria-modal="true" aria-labelledby={titleID} onKeyDown={event => {
    if (event.key === 'Escape' && !pending) onCancel();
    if (event.key === 'Tab') {
      const controls = event.currentTarget.querySelectorAll<HTMLButtonElement>('button:not(:disabled)');
      if (!controls.length) { event.preventDefault(); return; }
      if (event.shiftKey && document.activeElement === controls[0]) { event.preventDefault(); controls[controls.length - 1].focus(); }
      else if (!event.shiftKey && document.activeElement === controls[controls.length - 1]) { event.preventDefault(); controls[0].focus(); }
    }
  }}><div className="card"><h2 id={titleID}>{title}</h2>{children}<div className="row" style={{ gap: 12 }}>
    <button ref={cancel} className="btn" disabled={pending} onClick={onCancel}>{cancelLabel}</button>
    <button className="btn" disabled={pending} onClick={onConfirm}>{pending ? pendingLabel : confirmLabel}</button>
  </div></div></div>;
}

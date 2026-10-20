import { useLayoutEffect, useRef, type ReactNode, type RefObject } from 'react';

export function Dialog({ children, labelledBy, role = 'dialog', pending = false, initialFocus, onCancel }: {
  children: ReactNode;
  labelledBy: string;
  role?: 'dialog' | 'alertdialog';
  pending?: boolean;
  initialFocus?: RefObject<HTMLElement>;
  onCancel: () => void;
}) {
  const root = useRef<HTMLDialogElement>(null);
  useLayoutEffect(() => {
    const dialog = root.current;
    if (!dialog) return;
    const previous = document.activeElement;
    dialog.showModal();
    const first = initialFocus?.current ?? dialog.querySelector<HTMLElement>('input:not(:disabled),textarea:not(:disabled),select:not(:disabled),button:not(:disabled),a[href]');
    first?.focus();
    return () => {
      if (dialog.open) dialog.close();
      // Wait for React's removal before choosing an existing return target.
      queueMicrotask(() => {
        if (document.querySelector('dialog[open]')) return;
        const target = previous instanceof HTMLElement && previous !== document.body && previous.isConnected && !previous.matches(':disabled')
          ? previous : document.getElementById('main-content');
        target?.focus({ preventScroll: true });
      });
    };
  }, [initialFocus]);
  useLayoutEffect(() => {
    const dialog = root.current;
    if (!dialog?.open) return;
    const active = document.activeElement;
    if (dialog.contains(active) && !(active instanceof HTMLElement && active.matches(':disabled'))) return;
    const first = dialog.querySelector<HTMLElement>('input:not(:disabled),textarea:not(:disabled),select:not(:disabled),button:not(:disabled),a[href]');
    (first ?? dialog).focus();
  }, [pending]);
  return <dialog ref={root} className="management-modal" role={role} aria-labelledby={labelledBy} aria-busy={pending || undefined} onCancel={event => {
    // React owns the draft lifetime. A native close request must not hide a
    // pending mutation or discard its uncertain outcome behind the owner.
    event.preventDefault();
    if (!pending) onCancel();
  }} onKeyDown={event => {
    if (event.key !== 'Tab') return;
    const dialog = event.currentTarget;
    const controls = [...dialog.querySelectorAll<HTMLElement>('input,textarea,select,button,a[href],[tabindex]')].filter(element =>
      element.tabIndex >= 0 && !element.matches(':disabled') && !element.closest('[hidden],[inert],[aria-hidden="true"]') &&
      element.getClientRects().length > 0 && getComputedStyle(element).visibility !== 'hidden');
    if (!controls.length) { event.preventDefault(); dialog.focus(); return; }
    const first = controls[0], last = controls[controls.length - 1];
    // Error summaries and headings can receive focus without entering the tab
    // sequence. Keep either direction inside the dialog from those positions.
    if (!controls.some(control => control === document.activeElement)) { event.preventDefault(); (event.shiftKey ? last : first).focus(); }
    else if (event.shiftKey && document.activeElement === first) { event.preventDefault(); last.focus(); }
    else if (!event.shiftKey && document.activeElement === last) { event.preventDefault(); first.focus(); }
  }}>{children}</dialog>;
}

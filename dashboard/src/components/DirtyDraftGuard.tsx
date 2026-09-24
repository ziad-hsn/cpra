import { useContext, useEffect, useRef } from 'react';
import { UNSAFE_DataRouterContext, useBlocker } from 'react-router-dom';

function RouteGuard({ dirty }: { dirty: boolean }) {
  const blocker = useBlocker(dirty);
  const keep = useRef<HTMLButtonElement>(null);
  useEffect(() => { if (blocker.state === 'blocked') keep.current?.focus(); }, [blocker.state]);
  if (blocker.state !== 'blocked') return null;
  return <div className="management-modal" role="alertdialog" aria-modal="true" aria-labelledby="discard-draft-title" onKeyDown={event => {
    if (event.key === 'Escape') blocker.reset();
    if (event.key === 'Tab') {
      const controls = event.currentTarget.querySelectorAll<HTMLButtonElement>('button');
      if (event.shiftKey && document.activeElement === controls[0]) { event.preventDefault(); controls[controls.length - 1].focus(); }
      else if (!event.shiftKey && document.activeElement === controls[controls.length - 1]) { event.preventDefault(); controls[0].focus(); }
    }
  }}>
    <div className="card"><h2 id="discard-draft-title">Leave this editor?</h2><p>Your unsaved changes will be discarded. Leaving does not cancel an operation already accepted by the server.</p>
      <div className="row" style={{ gap: 12 }}><button ref={keep} className="btn" onClick={() => blocker.reset()}>Keep editing</button><button className="btn" onClick={() => blocker.proceed()}>Discard draft and leave</button></div>
    </div>
  </div>;
}

export function DirtyDraftGuard({ dirty }: { dirty: boolean }) {
  const router = useContext(UNSAFE_DataRouterContext);
  useEffect(() => {
    if (!dirty) return;
    const beforeUnload = (event: BeforeUnloadEvent) => { event.preventDefault(); event.returnValue = ''; };
    window.addEventListener('beforeunload', beforeUnload);
    return () => window.removeEventListener('beforeunload', beforeUnload);
  }, [dirty]);
  return router ? <RouteGuard dirty={dirty} /> : null;
}

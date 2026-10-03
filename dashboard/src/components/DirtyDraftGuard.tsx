import { useContext, useEffect, useId, useRef } from 'react';
import { UNSAFE_DataRouterContext, useBlocker } from 'react-router-dom';
import { Dialog } from './Dialog';

function RouteGuard({ dirty }: { dirty: boolean }) {
  const blocker = useBlocker(dirty);
  const keep = useRef<HTMLButtonElement>(null);
  const heading = useId();
  if (blocker.state !== 'blocked') return null;
  return <Dialog role="alertdialog" labelledBy={heading} initialFocus={keep} onCancel={() => blocker.reset()}>
    <div className="card"><h2 id={heading}>Leave this editor?</h2><p>Your unsaved changes will be discarded. Leaving does not cancel an operation already accepted by the server.</p>
      <div className="row" style={{ gap: 12 }}><button ref={keep} type="button" className="btn" onClick={() => blocker.reset()}>Keep editing</button><button type="button" className="btn" onClick={() => blocker.proceed()}>Discard draft and leave</button></div>
    </div>
  </Dialog>;
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

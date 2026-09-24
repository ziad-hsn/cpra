import { useEffect, useRef, useState } from 'react';
import { useAccess, useDashboardSession } from '../auth/SessionBoundary';
import { ManagementError } from '../api/session';

type Display = { phase: 'hidden' } | { phase: 'loading' } | { phase: 'loaded'; text: string } | { phase: 'error'; message: string };

/** Metrics remain only in this mounted view; no query cache, polling or storage. */
export function MetricsView() {
  const session = useDashboardSession();
  const access = useAccess();
  const [display, setDisplay] = useState<Display>({ phase: 'hidden' });
  const pending = useRef<AbortController | null>(null);
  const permitted = access.phase === 'legacy' || session.can('GetMetrics');

  useEffect(() => {
    const unsubscribe = session.onReset(() => {
      pending.current?.abort(); pending.current = null;
      setDisplay({ phase: 'hidden' });
    });
    return () => { unsubscribe(); pending.current?.abort(); pending.current = null; };
  }, [session]);

  function hide() {
    pending.current?.abort(); pending.current = null;
    setDisplay({ phase: 'hidden' });
  }
  async function load() {
    pending.current?.abort();
    const controller = new AbortController();
    const epoch = session.getSnapshot().epoch;
    pending.current = controller;
    setDisplay({ phase: 'loading' });
    try {
      const text = await session.prometheus({ signal: controller.signal });
      if (pending.current === controller && !controller.signal.aborted && session.getSnapshot().epoch === epoch) setDisplay({ phase: 'loaded', text });
    } catch (error) {
      if (pending.current !== controller || controller.signal.aborted || session.getSnapshot().epoch !== epoch) return;
      setDisplay({ phase: 'error', message: error instanceof ManagementError ? error.message : 'Metrics could not be loaded.' });
    } finally { if (pending.current === controller) pending.current = null; }
  }

  return <section className="card" aria-label="Prometheus metrics">
    <div className="card-head"><h2 className="card-title">Prometheus Metrics</h2><span className="card-sub">raw text exposition</span></div>
    <p className="muted">Read a snapshot from this server using your current access. Loaded only on request, with a 1 MiB display limit.</p>
    {!permitted ? <p>Metrics access is not available for this session.</p> : <>
      <div className="row" style={{ gap: 8 }}>
        <button className="btn" disabled={display.phase === 'loading'} onClick={() => void load()}>{display.phase === 'loaded' ? 'Refresh metrics' : 'View metrics'}</button>
        {display.phase !== 'hidden' && <button className="btn ghost" onClick={hide}>{display.phase === 'loading' ? 'Cancel' : 'Hide metrics'}</button>}
      </div>
      {display.phase === 'loading' && <p role="status">Loading metrics…</p>}
      {display.phase === 'error' && <p role="alert">{display.message}</p>}
      {display.phase === 'loaded' && <pre role="region" aria-label="Prometheus metrics output" tabIndex={0} className="mono"
        style={{ maxHeight: 360, overflow: 'auto', whiteSpace: 'pre', marginTop: 12 }}>{display.text || 'The endpoint returned no metric lines.'}</pre>}
    </>}
  </section>;
}

import { useQuery } from '@tanstack/react-query';
import { useAccess, useDashboardSession } from '../auth/SessionBoundary';
import { loadReadiness, loadRuntime } from '../api/runtime';
import { formatDateTime, formatNumber } from '../lib/format';

const milliseconds = (value?: number) => value === undefined ? 'Unavailable' : `${formatNumber(value)} ms`;
const status = (value: boolean | undefined, yes: string, no: string) => value === undefined ? 'Unavailable' : value ? yes : no;

export function RuntimeDiagnostics() {
  const session = useDashboardSession();
  const access = useAccess();
  const legacy = access.phase === 'legacy';
  const canReadState = legacy || session.can('GetState');
  const canReadReady = !legacy && session.can('GetReady');
  const state = useQuery({ queryKey: ['runtime-observations', legacy ? 'legacy' : 'v2'], queryFn: ({ signal }) => loadRuntime(session, signal), enabled: canReadState, refetchInterval: 5000, retry: false, gcTime: 0 });
  const ready = useQuery({ queryKey: ['runtime-readiness'], queryFn: ({ signal }) => loadReadiness(session, signal), enabled: canReadReady, refetchInterval: 5000, retry: false, gcTime: 0 });
  // A failed refresh must not leave an old successful status presented as current.
  const current = state.isError ? undefined : state.data;
  return <section className="card" aria-label="Runtime readiness and progress">
    <div className="card-head"><h2 className="card-title">Runtime readiness and progress</h2>
      {(canReadState || canReadReady) && <button className="btn" disabled={state.isFetching || ready.isFetching} onClick={() => { if (canReadState) void state.refetch(); if (canReadReady) void ready.refetch(); }}>Refresh runtime observations</button>}
    </div>
    {legacy && <p role="status">Legacy observation mode: storage observations are available when reported. Admission readiness, controller progress and projection freshness require the management API.</p>}
    {!canReadState ? <p role="status">Your identity cannot read runtime state.</p> : state.isError ? <p role="alert">Runtime state is unavailable. No previous successful observation is shown as current.</p> : state.isPending ? <p role="status">Loading runtime state…</p> : null}
    {!legacy && (!canReadReady ? <p role="status">Your identity cannot read the readiness endpoint.</p> : ready.isError ? <p role="alert">Readiness endpoint is unavailable; readiness could not be determined.</p> : !ready.data ? <p role="status">Loading readiness observation…</p> : <p>Readiness endpoint: <strong>{ready.data.ready ? 'Ready' : 'Not ready'}</strong>{ready.data.generatedAt ? ` · Observed ${formatDateTime(ready.data.generatedAt)}` : ''}.</p>)}
    {current && <>
      {current.generatedAt && <p className="muted">State observed: {formatDateTime(current.generatedAt)}</p>}
      <dl className="management-observations">
        <dt>Admission readiness (state)</dt><dd>{status(current.ready, 'Ready for new work', 'Unavailable for new work')}</dd>
        <dt>Process liveness (state)</dt><dd>{status(current.live, 'Live', 'Not live')}</dd>
        <dt>Controller initialization and progress</dt><dd>{status(current.controllerReady, 'Ready', 'Not ready')}</dd>
        <dt>Dashboard projection</dt><dd>{status(current.projectionFresh, 'Fresh', 'Stale')}</dd>
        <dt>Projection age</dt><dd>{milliseconds(current.projectionAgeMs)}</dd>
        <dt>Storage availability</dt><dd>{current.storage.available ? 'Available' : 'Unavailable'}</dd>
        <dt>Storage mode</dt><dd>{current.storage.mode}</dd>
        <dt>FSM-applied log position</dt><dd>{current.storage.appliedIndex === undefined ? 'Unavailable' : formatNumber(current.storage.appliedIndex)}</dd>
        <dt>Last recorded commit latency</dt><dd>{milliseconds(current.storage.commitLatencyMs)}</dd>
        <dt>Last recorded snapshot duration</dt><dd>{milliseconds(current.storage.snapshotDurationMs)}</dd>
      </dl>
      {current.readinessReason && <p role="status">{current.readinessReason}</p>}
      {current.controllerReason && <p className="muted">{current.controllerReason}</p>}
      {current.storage.error && <p role="alert">Storage reports an error. Consult protected server diagnostics.</p>}
    </>}
    <p className="muted">Readiness includes initialization, usable storage and admission. An intentionally empty configuration can be ready when the server permits it. Monitored service failures do not by themselves make CPRa unready.</p>
    <p className="muted">Projection freshness describes the dashboard snapshot, separately from controller progress and storage availability. A stale projection does not prove a disk failure. These endpoints are separate observations and can differ during a transition.</p>
  </section>;
}

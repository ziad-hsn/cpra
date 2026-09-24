import { Link, useParams } from 'react-router-dom';
import { useQuery } from '@tanstack/react-query';
import { useAccess, useDashboardSession } from '../auth/SessionBoundary';
import { getResource } from '../api/resources';
import type { Monitor } from '../api/generated';
import { MonitorStatusDetails } from '../components/MonitorFields';
import { MonitorControls } from '../components/MonitorControls';
import { MonitorActions } from '../components/MonitorActions';
import { MonitorTimeline } from '../components/MonitorTimeline';

/** Saved monitor identity is sufficient for observation and generic controls,
 * including configurations absent from the legacy numeric fleet projection. */
export default function MonitorState() {
  const { monitorID = '' } = useParams();
  const session = useDashboardSession();
  const access = useAccess();
  const validID = /^[a-zA-Z0-9][a-zA-Z0-9._:-]{0,127}$/.test(monitorID);
  const canRead = session.can('GetMonitor');
  // Share the saved-resource observation with MonitorControls. Polling never
  // replaces a control's frozen preconditions with newer versions.
  const detail = useQuery({ queryKey: ['management', 'monitors', 'detail', monitorID], queryFn: ({ signal }) => getResource(session, 'monitors', monitorID, signal), enabled: validID && canRead, refetchInterval: 5000, gcTime: 0 });
  const monitor = detail.data?.resource as Monitor | undefined;
  const matched = monitor?.metadata.id === monitorID;
  return <div className="stack-16">
    <div className="row" style={{ gap: 8, flexWrap: 'wrap' }}>
      <h1 className="page-title">Monitor state</h1>
      <Link className="btn ghost" to="/monitor-configurations">Monitor configurations</Link>
      {validID && canRead && <Link className="btn ghost" to={`/monitor-configurations/${encodeURIComponent(monitorID)}`}>View saved configuration</Link>}
    </div>
    {!validID ? <p role="alert">This monitor ID is not valid.</p> : !canRead ? <p role="status">Your identity cannot read this monitor. This view requires the management API.</p> : detail.isError || (monitor && !matched) ? <section className="card" role="alert">
      <p>Monitor state is unavailable. Controls cannot use an older or mismatched observation.</p>
      <button className="btn" onClick={() => void detail.refetch()}>Retry monitor state</button>
    </section> : !monitor ? <p role="status">Loading monitor state…</p> : <>
      <section className="card" aria-label="Saved monitor identity">
        <div className="card-head"><h2>{monitor.metadata.name || monitorID}</h2><button className="btn" disabled={detail.isFetching} onClick={() => void detail.refetch()}>Refresh monitor state</button></div>
        <p>Stable ID: <span className="mono">{monitorID}</span> · Incarnation: <span className="mono">{monitor.metadata.uid || 'Unavailable'}</span></p>
        <p>Configuration version: <span className="mono">{monitor.metadata.resourceVersion || 'Unavailable'}</span></p>
        {access.access?.role === 'reader' && <p role="status">Read-only access. Monitor observations and retained history remain available.</p>}
        {detail.data?.unsupported && <p role="status">{detail.data.unsupported} Supported observations and permitted generic controls remain available; this view does not edit driver configuration.</p>}
        <MonitorStatusDetails monitor={monitor} />
      </section>
      {/* A replacement incarnation must discard drafts for its predecessor. */}
      {monitor.metadata.uid ? <MonitorControls key={`controls/${monitorID}/${monitor.metadata.uid}`} monitorID={monitorID} /> : <p role="status">Monitor controls are unavailable until its incarnation is reported.</p>}
      {session.can('ListActions') ? <MonitorActions key={`actions/${monitorID}/${monitor.metadata.uid ?? ''}`} monitorID={monitorID} /> : <p role="status">Your identity cannot list action outcomes for this monitor.</p>}
      {session.can('GetHistory') ? <MonitorTimeline key={`history/${monitorID}/${monitor.metadata.uid ?? ''}`} monitorID={monitorID} /> : <p role="status">Your identity cannot read event history for this monitor.</p>}
    </>}
  </div>;
}

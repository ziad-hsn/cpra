import { Link, useParams } from 'react-router-dom';
import { useQuery } from '@tanstack/react-query';
import { useAccess, useDashboardSession } from '../auth/SessionBoundary';
import { getResource, getMonitorObservation } from '../api/resources';
import type { Monitor } from '../api/generated';
import { MonitorStatusDetails } from '../components/MonitorFields';
import { MonitorControls } from '../components/MonitorControls';
import { MonitorActions } from '../components/MonitorActions';
import { MonitorTimeline } from '../components/MonitorTimeline';
import { FailureDetails } from '../components/FailureDetails';
import { RecoveryDecisionDetails, RecoveryAdmissionDetails } from '../components/RecoveryRules';
import { MonitorNotificationConditions } from '../components/MonitorNotificationConditions';
import { formatDateTime } from '../lib/format';

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
  if (validID && !canRead && session.can('GetMonitorObservation')) return <ObservedMonitorState monitorID={monitorID} />;
  return <div className="stack-16">
    <div className="row" style={{ gap: 8, flexWrap: 'wrap' }}>
      <h1 className="page-title">Monitor state</h1>
      <Link className="btn ghost" to="/monitor-configurations">Monitor configurations</Link>
      {validID && canRead && <Link className="btn ghost" to={`/monitor-configurations/${encodeURIComponent(monitorID)}`}>View saved configuration</Link>}
      {validID && canRead && session.can('CreateOperation') && <Link className="btn" to={`/import?monitor=${encodeURIComponent(monitorID)}`}>Set up notifications</Link>}
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

function ObservedMonitorState({ monitorID }: { monitorID: string }) {
  const session = useDashboardSession();
  const query = useQuery({ queryKey: ['observations', 'monitors', monitorID], queryFn: ({ signal }) => getMonitorObservation(session, monitorID, signal), refetchInterval: 5000, retry: false, gcTime: 0 });
  const row = query.data;
  return <div className="stack-16">
    <div className="row"><h1 className="page-title">Monitor state</h1><Link className="btn ghost" to="/monitors">Monitor fleet</Link></div>
    <p role="status">Read-only observations. File-configured monitors have no writable catalog resource version.</p>
    {query.isError ? <section className="card" role="alert"><p>Current monitor state is unavailable. A previous observation does not establish current health.</p><button className="btn" onClick={() => void query.refetch()}>Retry monitor state</button></section> : !row ? <p role="status">Loading monitor state…</p> : <>
      <section className="card" aria-label="Monitor observation">
        <div className="card-head"><h2>{row.name || monitorID}</h2><button className="btn" disabled={query.isFetching} onClick={() => void query.refetch()}>Refresh monitor state</button></div>
        <p>Stable ID: <span className="mono">{monitorID}</span> · Incarnation: <span className="mono">{row.incarnationUID || 'Unavailable'}</span></p>
        <h3>Current observations</h3>
        <p>Current health: {row.health} · Last check: {formatDateTime(row.lastCheckedAt ?? '')}</p>
        <p>Check availability: {row.availability?.state ?? 'Unavailable'}{row.availability?.reason ? ` (${row.availability.reason})` : ''} · Assessed: {formatDateTime(row.availability?.observedAt ?? '')}</p>
        {row.lastObservedAt && <p>Last target observation: {row.lastObservedHealth || 'unknown'} at {formatDateTime(row.lastObservedAt)}.</p>}
        <p>Checks: {row.enabled ? row.checkAdmission?.replaceAll('_', ' ') || 'Admission not reported' : 'Disabled'} · Ignore: {row.ignored ? 'Active; observations continue' : 'Inactive'} · Snoozed until: {formatDateTime(row.snoozedUntil ?? '')}</p>
        <p>Maintenance: {row.maintenance === undefined ? 'Unavailable' : row.maintenance ? 'Active' : 'Inactive'} · Ordinary notifications: {row.notificationsSuppressed === undefined ? 'Suppression unavailable' : row.notificationsSuppressed ? 'Suppressed' : 'Eligible under incident policy'}</p>
        <RecoveryDecisionDetails decision={row.recoveryDecision} />
    <RecoveryAdmissionDetails admission={row.recoveryAdmission} />
        <FailureDetails diagnostic={row.lastFailure} label="Last check failure" />
        <FailureDetails diagnostic={row.configurationProblem} label="Check configuration problem" />
        <p>Last-check latency: {row.lastCheckLatencyMs.available && row.lastCheckLatencyMs.value !== undefined ? `${row.lastCheckLatencyMs.value} ms` : 'Unavailable'}</p>
        <p>Configured notifications: {row.configuredNotifications.join(', ') || 'None'} · Pending notifications: {row.pendingNotifications} · Cancelled attempts (retained): {row.cancelledNotifications ?? 'Unavailable'} · Failed deliveries: {row.failedNotifications} · Unknown provider outcomes: {row.unknownActions} · Actions needing review: {row.actionsNeedingReview}</p>
        {row.warnings?.some(warning => warning.reason === 'unknownRecoveryDestination') && <p className="warning">Uncertain recovery has no enabled blue notification destination. Its action remains held for review.</p>}
        {row.warnings?.some(warning => warning.reason === 'recoveryOnlyCoverage') && <p className="warning">The configured red trigger covers recovery outcomes. A no-intervention decision can leave an outage outside its coverage.</p>}
        <MonitorNotificationConditions conditions={row.notificationConditions} truncated={row.notificationConditionsTruncated} observedAt={row.notificationConditionsObservedAt} sequence={row.notificationConditionsSequence} />
      </section>
      {session.can('ListActions') && <MonitorActions key={`actions/${monitorID}/${row.incarnationUID}`} monitorID={monitorID} />}
      {session.can('GetHistory') && <MonitorTimeline key={`history/${monitorID}/${row.incarnationUID}`} monitorID={monitorID} />}
    </>}
  </div>;
}

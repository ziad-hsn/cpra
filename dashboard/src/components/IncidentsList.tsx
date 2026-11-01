import { useState } from 'react';
import { Link } from 'react-router-dom';
import { useQuery } from '@tanstack/react-query';
import { useDashboardSession } from '../auth/SessionBoundary';
import { listIncidents, type IncidentFilters } from '../api/incidents';
import { ManagementError } from '../api/session';
import { formatDateTime, formatNumber } from '../lib/format';

/** One bounded snapshot page, with explicit refresh instead of polling new snapshots. */
export function IncidentsList() {
  const session = useDashboardSession();
  const [filter, setFilter] = useState('');
  const [filters, setFilters] = useState<IncidentFilters>({order: 'triage'});
  const [selection, setSelection] = useState({ monitorID: '', cursor: '', snapshot: '', revision: 0, filters });
  const valid = filter === '' || /^[a-zA-Z0-9][a-zA-Z0-9._:-]{0,127}$/.test(filter);
  const query = useQuery({ queryKey: ['management', 'incidents', 'list', selection],
    queryFn: async ({ signal }) => {
      const page = await listIncidents(session, selection.cursor, selection.monitorID, signal, selection.filters);
      if (selection.cursor && selection.snapshot && page.snapshot !== selection.snapshot) throw new ManagementError('invalid', 'The incident snapshot changed during pagination.');
      return page;
    }, enabled: session.can('ListIncidents'), retry: false, gcTime: 0, staleTime: Infinity,
    refetchOnWindowFocus: false, refetchOnReconnect: false,
  });
  const page = query.data;
  const expired = query.error instanceof ManagementError && query.error.status === 410;
  return <section className="card" aria-labelledby="incidents-list-heading">
    <h2 id="incidents-list-heading">Latest incidents</h2>
    <p>One latest outage episode per monitor, pending the unhealthy threshold, open or closed. Earlier events remain in the monitor timeline.</p>
    <form className="management-form" aria-label="Filter incidents" onSubmit={event => {
      event.preventDefault();
      if (valid) setSelection(current => ({ monitorID: filter, cursor: '', snapshot: '', revision: current.revision + 1, filters: {...filters} }));
    }}>
      <label className="management-field">Exact monitor ID (optional)<input className="input" value={filter} onChange={event => setFilter(event.target.value)} /></label>
      <div className="grid-2">
        <label className="management-field">Incident state<select className="input" value={filters.state ?? ''} onChange={e => setFilters({...filters, state: e.target.value as IncidentFilters['state']})}><option value="">All states</option><option value="open">Open</option><option value="pending">Pending threshold</option><option value="closed">Closed</option></select></label>
        <label className="management-field">Acknowledgement<select className="input" value={filters.acknowledged ?? ''} onChange={e => setFilters({...filters, acknowledged: e.target.value as IncidentFilters['acknowledged']})}><option value="">All</option><option value="false">Unacknowledged</option><option value="true">Acknowledged</option></select></label>
        <label className="management-field">Dismissal<select className="input" value={filters.dismissed ?? ''} onChange={e => setFilters({...filters, dismissed: e.target.value as IncidentFilters['dismissed']})}><option value="">All</option><option value="false">Not dismissed</option><option value="true">Dismissed</option></select></label>
        <label className="management-field">Incident order<select className="input" value={filters.order} onChange={e => setFilters({...filters, order: e.target.value as IncidentFilters['order']})}><option value="triage">Triage</option><option value="id">Incident ID</option></select></label>
      </div>
      <p className="muted">Triage: open before pending and closed; not dismissed before dismissed; unacknowledged before acknowledged; then oldest first failure and incident ID.</p>
      {!valid && <p role="alert">Enter a valid monitor ID, or leave it empty for all monitors.</p>}
      <button className="btn" type="submit" disabled={!valid || query.isFetching}>Apply filter</button>
    </form>
    {query.isPending && <p role="status">Reading incident page…</p>}
    {query.isError && <p role="alert">{expired ? 'This incident snapshot expired. Refresh from the first page to continue.' : 'Incident observations are unavailable. Refresh to try again; this does not establish that incidents are resolved.'}</p>}
    {page && <>
      <p className="muted">{page.generatedAt ? <>Snapshot taken <time dateTime={page.generatedAt}>{formatDateTime(page.generatedAt)}</time>.</> : 'Snapshot time is not reported.'} Refresh for current observations.</p>
      <p role="group" aria-label="Incident page counts">This page: {formatNumber(page.items.length)} latest incidents; {formatNumber(page.items.filter(item => item.state === 'open').length)} open; {formatNumber(page.items.filter(item => item.acknowledgedBy).length)} acknowledged; {formatNumber(page.items.filter(item => item.dismissed === true).length)} dismissed. These are page counts, not fleet totals.</p>
      {page.items.length === 0 ? <p>{page.nextCursor ? 'No incidents in this part of the snapshot. Continue to the next page.' : 'No latest incidents in this part of the snapshot. This does not establish fleet health.'}</p> :
        <div className="table-scroll" role="region" aria-label="Latest incident page" tabIndex={0}><table className="data-table">
          <thead><tr><th>Monitor</th><th>Incident</th><th>State</th><th>First failure</th><th>Threshold reached</th><th>Acknowledged by</th><th>Notification control</th><th>Notifications</th><th>Closed</th></tr></thead>
          <tbody>{page.items.map(item => <tr key={item.id}>
            <td className="mono"><span className="table-identifier">{session.can('GetMonitor') || session.can('GetMonitorObservation') ? <Link to={`/monitors/by-id/${encodeURIComponent(item.monitorID)}`}>{item.monitorID}</Link> : item.monitorID}</span></td>
            <td className="mono"><span className="table-identifier">{item.id}</span></td><td>{item.state === 'unrecognized' ? 'Unrecognized state' : item.state}</td>
            <td>{item.firstFailureAt ? formatDateTime(item.firstFailureAt) : 'Not reported'}</td>
            <td>{item.openedAt ? formatDateTime(item.openedAt) : 'Not reported'}</td>
            <td><span className="table-identifier">{item.acknowledgedBy || 'Not acknowledged'}</span>{item.acknowledgedAt && <> · {formatDateTime(item.acknowledgedAt)}</>}</td>
            <td>{item.dismissed === true ? 'Dismissed for this incident' : item.dismissed === false ? 'Not dismissed' : 'Not reported'}</td>
            <td>{item.notifications ? <span>{formatNumber(item.notifications.pending)} pending · {formatNumber(item.notifications.accepted)} accepted · {formatNumber(item.notifications.failed)} failed · {formatNumber(item.notifications.unknown)} unknown · {formatNumber(item.notifications.cancelled)} cancelled</span> : 'Not reported'}</td>
            <td>{item.closedAt ? formatDateTime(item.closedAt) : 'Not reported'}</td>
          </tr>)}</tbody>
        </table></div>}
    </>}
    <div className="row incident-navigation" style={{ gap: 12, flexWrap: 'wrap' }}>
      <button className="btn" disabled={query.isFetching} onClick={() => setSelection(current => ({ ...current, cursor: '', snapshot: '', revision: current.revision + 1 }))}>Refresh from first page</button>
      {page?.nextCursor && <button className="btn" disabled={query.isFetching || query.isError} onClick={() => setSelection(current => ({ ...current, cursor: page.nextCursor!, snapshot: page.snapshot ?? '' }))}>Next incident page</button>}
    </div>
    <p className="muted">At most 100 incidents are loaded per page. Notification counts show the latest attempt of each logical delivery; earlier attempts remain in action history. Accepted does not prove delivery or provider resolution. Reading this list does not acknowledge, dismiss, or repeat work.</p>
  </section>;
}

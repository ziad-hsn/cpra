import { useState } from 'react';
import { Link } from 'react-router-dom';
import { useQuery } from '@tanstack/react-query';
import { useDashboardSession } from '../auth/SessionBoundary';
import { listIncidents } from '../api/incidents';
import { ManagementError } from '../api/session';
import { formatDateTime, formatNumber } from '../lib/format';

/** One bounded snapshot page, with explicit refresh instead of polling new snapshots. */
export function IncidentsList() {
  const session = useDashboardSession();
  const [filter, setFilter] = useState('');
  const [selection, setSelection] = useState({ monitorID: '', cursor: '', snapshot: '', revision: 0 });
  const valid = filter === '' || /^[a-zA-Z0-9][a-zA-Z0-9._:-]{0,127}$/.test(filter);
  const query = useQuery({ queryKey: ['management', 'incidents', 'list', selection],
    queryFn: async ({ signal }) => {
      const page = await listIncidents(session, selection.cursor, selection.monitorID, signal);
      if (selection.cursor && selection.snapshot && page.snapshot !== selection.snapshot) throw new ManagementError('invalid', 'The incident snapshot changed during pagination.');
      return page;
    }, enabled: session.can('ListIncidents'), retry: false, gcTime: 0, staleTime: Infinity,
    refetchOnWindowFocus: false, refetchOnReconnect: false,
  });
  const page = query.data;
  const expired = query.error instanceof ManagementError && query.error.status === 410;
  return <section className="card" aria-labelledby="incidents-list-heading">
    <h2 id="incidents-list-heading">Latest incidents</h2>
    <p>One latest incident per monitor, open or closed. Earlier incident events remain in the monitor timeline.</p>
    <form className="management-form" aria-label="Filter incidents" onSubmit={event => {
      event.preventDefault();
      if (valid) setSelection(current => ({ monitorID: filter, cursor: '', snapshot: '', revision: current.revision + 1 }));
    }}>
      <label className="management-field">Exact monitor ID (optional)<input className="input" value={filter} onChange={event => setFilter(event.target.value)} /></label>
      {!valid && <p role="alert">Enter a valid monitor ID, or leave it empty for all monitors.</p>}
      <button className="btn" type="submit" disabled={!valid || query.isFetching}>Apply filter</button>
    </form>
    {query.isPending && <p role="status">Reading incident page…</p>}
    {query.isError && <p role="alert">{expired ? 'This incident snapshot expired. Refresh from the first page to continue.' : 'Incident observations are unavailable. Refresh to try again; this does not establish that incidents are resolved.'}</p>}
    {page && <>
      <p className="muted">{page.generatedAt ? <>Snapshot taken <time dateTime={page.generatedAt}>{new Date(page.generatedAt).toLocaleString()}</time>.</> : 'Snapshot time is not reported.'} Refresh for current observations.</p>
      <p aria-label="Incident page counts">This page: {formatNumber(page.items.length)} latest incidents; {formatNumber(page.items.filter(item => item.state === 'open').length)} open; {formatNumber(page.items.filter(item => item.acknowledgedBy).length)} acknowledged; {formatNumber(page.items.filter(item => item.dismissed === true).length)} dismissed. These are page counts, not fleet totals.</p>
      {page.items.length === 0 ? <p>{page.nextCursor ? 'No incidents in this part of the snapshot. Continue to the next page.' : 'No latest incidents in this part of the snapshot. This does not establish fleet health.'}</p> :
        <div className="table-scroll" role="region" aria-label="Latest incident page" tabIndex={0}><table className="data-table">
          <thead><tr><th>Monitor</th><th>Incident</th><th>State</th><th>Opened</th><th>Acknowledged by</th><th>Notifications</th><th>Closed</th></tr></thead>
          <tbody>{page.items.map(item => <tr key={item.id}>
            <td className="mono">{session.can('GetMonitor') ? <Link to={`/monitors/by-id/${encodeURIComponent(item.monitorID)}`}>{item.monitorID}</Link> : item.monitorID}</td>
            <td className="mono">{item.id}</td><td>{item.state === 'unrecognized' ? 'Unrecognized state' : item.state}</td>
            <td>{item.openedAt ? formatDateTime(item.openedAt) : 'Not reported'}</td>
            <td>{item.acknowledgedBy || 'Not acknowledged'}{item.acknowledgedAt && <> · {formatDateTime(item.acknowledgedAt)}</>}</td>
            <td>{item.dismissed === true ? 'Dismissed for this incident' : item.dismissed === false ? 'Not dismissed' : 'Not reported'}</td>
            <td>{item.closedAt ? formatDateTime(item.closedAt) : 'Not reported'}</td>
          </tr>)}</tbody>
        </table></div>}
    </>}
    <div className="row" style={{ gap: 12 }}>
      <button className="btn" disabled={query.isFetching} onClick={() => setSelection(current => ({ ...current, cursor: '', snapshot: '', revision: current.revision + 1 }))}>Refresh from first page</button>
      {page?.nextCursor && <button className="btn" disabled={query.isFetching || query.isError} onClick={() => setSelection(current => ({ ...current, cursor: page.nextCursor!, snapshot: page.snapshot ?? '' }))}>Next incident page</button>}
    </div>
    <p className="muted">At most 100 incidents are loaded per page. Reading this list does not acknowledge, dismiss, or repeat work.</p>
  </section>;
}

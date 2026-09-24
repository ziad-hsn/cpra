import { useState } from 'react';
import { useQuery } from '@tanstack/react-query';
import { api } from '../api/client';
import { loadTimeline } from '../api/history';
import { useDashboardSession } from '../auth/SessionBoundary';
import { formatDateTime } from '../lib/format';

export function MonitorTimeline({ monitorID }: { monitorID: string }) {
  const session = useDashboardSession();
  const [cursors, setCursors] = useState(['']);
  const cursor = cursors[cursors.length - 1];
  const history = useQuery({ queryKey: ['history', monitorID, cursor], queryFn: ({ signal }) => loadTimeline(session, monitorID, cursor, signal), refetchInterval: query => cursor || query.state.data?.next_cursor ? false : 10000 });
  const legacy = session.getSnapshot().phase === 'legacy';
  const state = useQuery({ queryKey: ['durable-state', monitorID], queryFn: () => api.getState(monitorID), enabled: legacy, refetchInterval: 5000 });
  const unknown = state.data?.actions.filter(a => a.state === 'unknown') ?? [];
  return <section className='card' aria-label='Monitor event timeline'>
    <div className='card-head'><h2 className='card-title'>Event timeline</h2><span className='card-sub'>30 days</span></div>
    <p className='muted'>Stable monitor ID: <span className='mono'>{monitorID}</span>. Incident, intervention and notification events are retained. Raw health checks are not stored in this timeline.</p>
    {state.isError && <p role='alert'>Action recovery status unavailable.</p>}
    {unknown.length > 0 && <div role='status'>
      <strong>{unknown.length} action outcome{unknown.length === 1 ? ' is' : 's are'} unknown.</strong> Automatic repetition is held. Check the provider’s records before deciding how to resolve these actions.
      <ul>{unknown.map(a => <li key={a.id}>{a.kind} {a.color} endpoint {a.endpoint + 1}: <span className='mono'>{a.id}</span></li>)}</ul>
    </div>}
    {history.isError ? <p role='alert'>Event history unavailable. <button className='btn ghost' onClick={() => history.refetch()}>Retry</button></p> : history.isPending ? <p>Loading events…</p> : <>
      {history.data.events.length === 0 ? <p>No retained events in this period.</p> : <div style={{ overflowX: 'auto' }}><table className='data-table'>
        <thead><tr><th>Time</th><th>Event</th><th>Action</th><th>Endpoint</th><th>Outcome</th><th>Operator</th><th>Reason or note</th></tr></thead>
        <tbody>{history.data.events.map(e => <tr key={e.id}><td>{formatDateTime(e.at)}</td><td>{e.type.replaceAll('_', ' ')}</td><td>{e.kind} {e.color}</td><td>{e.kind === 'code' && e.endpoint !== undefined ? e.endpoint + 1 : '—'}</td><td>{e.outcome?.replaceAll('_', ' ') || '—'}</td><td>{e.actor || '—'}</td><td style={{ whiteSpace: 'pre-wrap', overflowWrap: 'anywhere', maxWidth: 420 }}>{[e.reason, e.note].filter(Boolean).join('\n') || '—'}{e.evidence_refs?.length ? <p>Evidence references (not fetched): {e.evidence_refs.join('\n')}</p> : null}</td></tr>)}</tbody>
      </table></div>}
      <div className='row' style={{ gap: 8, marginTop: 12 }}>
        <button className='btn ghost' disabled={cursors.length === 1} onClick={() => setCursors(cursors.slice(0, -1))}>Previous</button>
        <span>Page {cursors.length}</span>
        <button className='btn ghost' disabled={!history.data.next_cursor} onClick={() => setCursors([...cursors, history.data.next_cursor!])}>Next</button>
        <button className='btn ghost' onClick={() => { setCursors(['']); void history.refetch(); }}>Refresh timeline</button>
      </div>
    </>}
  </section>;
}

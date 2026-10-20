import { FailureDetails } from './FailureDetails';
import { useState } from 'react';
import { useQuery } from '@tanstack/react-query';
import { loadTimeline } from '../api/history';
import { useDashboardSession } from '../auth/SessionBoundary';
import { formatDateTime } from '../lib/format';

export function MonitorTimeline({ monitorID }: { monitorID: string }) {
  const session = useDashboardSession();
  const [cursors, setCursors] = useState(['']);
  const cursor = cursors[cursors.length - 1];
  const history = useQuery({ queryKey: ['history', monitorID, cursor], queryFn: ({ signal }) => loadTimeline(session, monitorID, cursor, signal), refetchInterval: query => cursor || query.state.data?.next_cursor ? false : 10000 });
  return <section className='card' aria-label='Monitor event timeline'>
    <div className='card-head'><h2 className='card-title'>Event timeline</h2><span className='card-sub'>30 days</span></div>
    <p className='muted'>Stable monitor ID: <span className='mono'>{monitorID}</span>. Incident, intervention and notification events are retained. Raw health checks are not stored in this timeline.</p>
    {history.isError ? <p role='alert'>Event history unavailable. <button className='btn ghost' onClick={() => history.refetch()}>Retry</button></p> : history.isPending ? <p>Loading events…</p> : <>
      {history.data.events.length === 0 ? <p>No retained events in this period.</p> : <div className='table-scroll' role='region' aria-label='Event timeline table' tabIndex={0}><table className='data-table'>
        <thead><tr><th>Time</th><th>Event</th><th>Action</th><th>Endpoint</th><th>Outcome</th><th>Operator</th><th>Reason or note</th></tr></thead>
        <tbody>{history.data.events.map(e => <tr key={e.id}><td>{formatDateTime(e.at)}</td><td>{e.type.replaceAll('_', ' ')}</td><td>{e.kind} {e.color}</td><td>{e.kind === 'code' && e.endpoint !== undefined ? e.endpoint + 1 : '—'}</td><td>{e.outcome?.replaceAll('_', ' ') || '—'}<FailureDetails diagnostic={e.failure} /><FailureDetails diagnostic={e.cause} label="Notification cause" />{e.recoveryFailure && <p style={{ overflowWrap: 'anywhere' }}>Confirmed {e.recoveryFailure.driver} failure, attempt {e.recoveryFailure.attempt} of {e.recoveryFailure.maxAttempts}; source action {e.recoveryFailure.actionID}.</p>}{e.agentReceipt && <p style={{ overflowWrap: 'anywhere' }}>Agent {e.agentReceipt.taskID ? `task ${e.agentReceipt.taskID}: ${e.agentReceipt.state}` : `message ${e.agentReceipt.messageID}: accepted`}. Receipt {e.agentReceipt.sequence}.</p>}</td><td>{e.actor || '—'}</td><td style={{ whiteSpace: 'pre-wrap', overflowWrap: 'anywhere', maxWidth: 420 }}>{[e.reason, e.note].filter(Boolean).join('\n') || '—'}{e.evidence_refs?.length ? <p>Evidence references (not fetched): {e.evidence_refs.join('\n')}</p> : null}</td></tr>)}</tbody>
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

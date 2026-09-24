import { useEffect, useRef, useState } from 'react';
import { useDashboardSession } from '../auth/SessionBoundary';
import { collectionDelay, readCollectionExecution, type CollectionIdentity } from '../api/collectionOperations';
import type { ApplyResult, ExecutionResultAvailability, Operation } from '../api/generated';
import { ManagementError } from '../api/session';
import { formatNumber } from '../lib/format';

const decisions: Record<string, string> = { accepted: 'Change committed', unchanged: 'Already up to date', conflict: 'Version conflict', dependencyBlocked: 'Blocked by a dependency', unattempted: 'Not attempted' };
function childLabel(item: ApplyResult): string {
  const child = item.childDisposition;
  if (!child) return item.catalogDecision === 'unchanged' ? 'No new application needed' : 'No controller operation';
  if (child.invalidatedByRestore) return 'Invalidated by restore';
  if (child.state === 'completed' && child.outcome === 'applied') return 'Applied';
  if (child.state === 'failed' && child.outcome === 'projection_failed') return 'Application failed';
  if (child.state === 'partial' && child.outcome === 'superseded') return 'Superseded';
  if (child.state === 'pending') return 'Awaiting controller';
  return 'Unrecognized controller outcome';
}
function readError(error: unknown): string {
  if (error instanceof ManagementError) {
    if (error.status === 410 && error.problem?.code === 'cursorExpired') return 'This result cursor expired. Read again from the first page.';
    if (error.status === 410) return 'The original results have expired or this operation is no longer available. Committed changes were not reversed.';
    if (error.status === 403 || error.reason === 'forbidden') return 'Your identity cannot read these application results.';
    if (error.status === 404) return 'Application results are unavailable to this identity.';
    if (error.status === 503) return 'Application history is unavailable. This does not establish success or failure.';
  }
  return 'Application results could not be confirmed. Reading stopped; the operation was not changed.';
}

/** The parent keys this lifetime to one original inventory. Keep one detached
 * result page and a row-free seal; never collect the complete input in memory. */
export function CollectionExecution({ id, identity, observation, sourceNames }: { id: string; identity: CollectionIdentity; observation?: ExecutionResultAvailability; sourceNames?: readonly string[] }) {
  const session = useDashboardSession();
  const [page, setPage] = useState<Operation>();
  const [busy, setBusy] = useState(false);
  const [waiting, setWaiting] = useState(false);
  const [message, setMessage] = useState('');
  const [failed, setFailed] = useState(false);
  const [observationInvalidated, setObservationInvalidated] = useState(false);
  const current = useRef<{ sequence: number; abort?: AbortController; timer?: ReturnType<typeof setTimeout>; anchor?: Operation }>({ sequence: 0 });
  const permitted = session.can('GetOperation');
  const availability = busy || failed || observationInvalidated ? undefined : page?.executionResult ?? observation;
  const counts = availability?.counts ?? availability?.summary;
  const summary = availability?.summary;

  useEffect(() => {
    const state = current.current;
    const clear = () => { state.sequence++; state.abort?.abort(); state.abort = undefined; clearTimeout(state.timer); state.timer = undefined; state.anchor = undefined; };
    const unsubscribe = session.onReset(() => { clear(); setPage(undefined); setBusy(false); setWaiting(false); setMessage(''); setFailed(false); setObservationInvalidated(false); });
    return () => { unsubscribe(); clear(); };
  }, [session]);

  const read = async (cursor = '', wait = false) => {
    if (!permitted || current.current.abort) return;
    const state = current.current;
    clearTimeout(state.timer); state.timer = undefined;
    const previous = cursor ? page : state.anchor;
    const sequence = ++state.sequence;
    const abort = new AbortController(); state.abort = abort;
    setPage(undefined); setBusy(true); setWaiting(wait); setMessage(''); setFailed(false);
    try {
      const response = await readCollectionExecution(session, identity, id, abort.signal, cursor, previous);
      if (sequence !== state.sequence || abort.signal.aborted) return;
      const result = response.data;
      if (result.executionResult?.state === 'ready') {
        const anchor = { ...result }; delete anchor.items; delete anchor.nextCursor;
        state.anchor = anchor;
      }
      setPage(result);
      setObservationInvalidated(false);
      if (wait && result.executionResult?.state === 'pending') {
        const delay = collectionDelay(response);
        setMessage(`Results are pending. Reading again in ${Math.ceil(delay / 1000)} seconds.`);
        state.timer = setTimeout(() => { if (sequence === state.sequence) void read('', true); }, delay);
      } else setWaiting(false);
    } catch (error) {
      if (sequence !== state.sequence || abort.signal.aborted) return;
      setWaiting(false); setFailed(true); setObservationInvalidated(true); setMessage(readError(error));
    } finally {
      if (sequence === state.sequence) { state.abort = undefined; setBusy(false); }
    }
  };
  const stop = () => {
    const state = current.current;
    state.sequence++; state.abort?.abort(); state.abort = undefined; clearTimeout(state.timer); state.timer = undefined;
    setPage(undefined); setBusy(false); setWaiting(false); setFailed(false); setMessage('Waiting stopped. The operation was not canceled or changed.');
  };

  return <section className="card" aria-label="Collection application results">
    <h2>Application results</h2>
    {!permitted ? <p role="status">Your identity cannot read application results.</p> : <>
      <p role="status">{failed || observationInvalidated ? 'Current resource-result availability could not be confirmed.' : busy ? 'Reading current resource-result availability…' : availability?.state === 'ready' ? 'Resource results are retained and ready to read.'
        : availability?.state === 'pending' ? 'Resource results are pending. An operation can stop before its accepted changes finish applying.'
          : availability?.state === 'expired' ? 'Retained resource results have expired. Committed changes were not reversed.'
            : availability ? 'This server reports a result availability the dashboard cannot interpret.' : 'No application results are reported for this operation.'}</p>
      {counts && <div aria-label="Application counts">
        <p>Decided: {formatNumber(counts.processed)} · Committed changes: {formatNumber(counts.accepted)} · Already up to date: {formatNumber(counts.unchanged)}</p>
        <p>Conflicts: {formatNumber(counts.conflicts)} · Dependency blocked: {formatNumber(counts.dependencyBlocked)} · Not attempted: {formatNumber(counts.unattempted)}</p>
        <p>Controller applied: {formatNumber(counts.childApplied)} · Failed: {formatNumber(counts.childFailed)} · Superseded: {formatNumber(counts.childSuperseded)} · Restore invalidated: {formatNumber(counts.childInvalidated)} · Pending: {formatNumber(counts.childPending)}</p>
      </div>}
      <div className="row" style={{ gap: 12 }}>
        <button className="btn" disabled={busy || waiting} onClick={() => void read()}>{page?.items?.length ? 'Refresh results from first page' : 'Read resource results'}</button>
        {page?.nextCursor && <button className="btn" disabled={busy || waiting} onClick={() => void read(page.nextCursor)}>Next results page</button>}
        {availability?.state === 'pending' && !waiting && <button className="btn" disabled={busy} onClick={() => void read('', true)}>Wait for resource results</button>}
        {(busy || waiting) && <button className="btn" onClick={stop}>Stop waiting for results</button>}
        {page?.items?.length && <button className="btn" onClick={() => { setPage(undefined); setMessage(''); setFailed(false); }}>Hide resource results</button>}
      </div>
      {busy && <p role="status">Reading application results…</p>}
      {message && <p role={failed ? 'alert' : 'status'}>{message}</p>}
      {summary && <p>Finalized <time dateTime={summary.finalizedAt}>{new Date(summary.finalizedAt).toLocaleString()}</time> · Retained until <time dateTime={summary.expiresAt}>{new Date(summary.expiresAt).toLocaleString()}</time></p>}
      {page?.executionResult?.state === 'ready' && page.items?.length ? <>
        <p>Showing {formatNumber(page.items[0].inputOrdinal!)}–{formatNumber(page.items.at(-1)!.inputOrdinal!)} of {formatNumber(identity.itemCount)} resources in original input order.</p>
        <div className="table-scroll" role="region" aria-label="Application resource results" tabIndex={0}><table className="data-table">
          <thead><tr><th>Input order</th><th>Resource</th><th>Configuration decision</th><th>Controller result</th><th>Controller operation</th><th>Previous version</th><th>New version</th>{sourceNames && <th>Local file</th>}<th>Source token</th><th>Document</th><th>Item</th></tr></thead>
          <tbody>{page.items.map(item => <tr key={item.inputOrdinal}>
            <td>{formatNumber(item.inputOrdinal!)}</td><td className="mono">{item.id}</td><td>{decisions[item.catalogDecision ?? ''] ?? 'Unrecognized decision'}</td><td>{childLabel(item)}</td>
            <td className="mono">{item.childDisposition?.operationID ?? 'None'}</td><td className="mono">{item.oldVersion ?? 'Not reported'}</td><td className="mono">{item.newVersion ?? 'Not reported'}</td>
            {sourceNames && <td>{sourceNames[Number(item.source?.slice(7)) - 1] ?? 'Local filename unavailable'}</td>}<td className="mono">{item.source}</td><td>{formatNumber(item.sourceDocument!)}</td><td>{formatNumber(item.sourceItem!)}</td>
          </tr>)}</tbody>
        </table></div>
      </> : null}
      <p className="muted">A committed configuration change and its controller result are separate outcomes. Each read returns at most 100 resources. Waiting or leaving this page does not repeat or cancel the operation.</p>
    </>}
  </section>;
}

import { useEffect, useRef, useState } from 'react';
import { useDashboardSession } from '../auth/SessionBoundary';
import { collectionValidationMessage, collectionValidationPendingDelay, readCollectionValidation, type CollectionIdentity, type CollectionValidationPage } from '../api/collectionOperations';
import { ManagementError } from '../api/session';
import { formatNumber } from '../lib/format';

const issues: Record<string, string> = {
  invalidSource: 'Invalid source', invalidResource: 'Invalid resource', duplicateIdentity: 'Duplicate identity', invalidGraph: 'Invalid references',
  validationLimit: 'Validation limit exceeded', conflict: 'Version conflict', missingReference: 'Missing reference', unsafePrefix: 'Unsafe application order',
  notEvaluated: 'Not evaluated', validationInterrupted: 'Validation interrupted', unrecognized: 'Unrecognized issue',
};
const changes: Record<string, string> = { create: 'Create', update: 'Update', unchanged: 'Unchanged', unrecognized: 'Unrecognized change' };

/** One explicitly requested metadata page. The parent keys this lifetime by the
 * original operation and immutable inventory identity; no input files or keys
 * are needed, retained or reconstructed here. A result is never an action grant. */
export function CollectionValidation({ id, identity }: { id: string; identity: CollectionIdentity }) {
  const session = useDashboardSession();
  const [page, setPage] = useState<CollectionValidationPage>();
  const [message, setMessage] = useState('');
  const [failed, setFailed] = useState(false);
  const [busy, setBusy] = useState(false);
  const current = useRef<{ sequence: number; abort?: AbortController; anchor?: CollectionValidationPage }>({ sequence: 0 });
  const permitted = session.can('GetOperationValidation');

  useEffect(() => {
    const state = current.current;
    const clear = () => {
      state.sequence++; state.abort?.abort(); state.abort = undefined; state.anchor = undefined;
    };
    const unsubscribe = session.onReset(() => { clear(); setPage(undefined); setMessage(''); setFailed(false); setBusy(false); });
    return () => { unsubscribe(); clear(); };
  }, [session]);

  const read = async (cursor = '') => {
    if (!permitted || current.current.abort) return;
    const state = current.current;
    const previous = cursor ? page : state.anchor;
    const sequence = ++state.sequence;
    const abort = new AbortController(); state.abort = abort;
    setBusy(true); setFailed(false); setMessage(''); setPage(undefined);
    try {
      const result = await readCollectionValidation(session, identity, id, abort.signal, cursor, previous);
      if (sequence !== state.sequence || abort.signal.aborted) return;
      // Keep only the immutable summary between reads. The displayed page is the
      // only retained row array, including when moving through a large result.
      state.anchor = { ...result.data, items: [], nextCursor: undefined };
      setPage(result.data);
    } catch (error) {
      if (sequence !== state.sequence || abort.signal.aborted) return;
      const delay = collectionValidationPendingDelay(error, id);
      setFailed(delay === undefined);
      setMessage(delay !== undefined
        ? `Validation is pending for this original operation. No verdict is available yet. Read again after ${Math.ceil(delay / 1000)} seconds; this view does not poll or submit validation.`
        : error instanceof ManagementError && error.status === 404
          ? 'The original validation result is unavailable to this identity. This does not establish a passing or rejected verdict.'
          : collectionValidationMessage(error));
    } finally {
      if (sequence === state.sequence) { state.abort = undefined; setBusy(false); }
    }
  };
  const stop = () => {
    const state = current.current;
    state.sequence++; state.abort?.abort(); state.abort = undefined;
    setPage(undefined); setBusy(false); setFailed(false);
    setMessage('Reading stopped. The operation was not canceled or changed.');
  };
  const hide = () => { setPage(undefined); setMessage(''); setFailed(false); };

  return <section className="card" aria-label="Original collection validation">
    <h2>Original validation result</h2>
    <p>Read the retained verdict for this exact collection. It describes validation at its recorded time; the operation receipt above remains the source of current progress.</p>
    {!permitted ? <p role="status">Your identity cannot read collection validation results.</p> : <>
      <div className="row" style={{ gap: 12 }}>
        <button className="btn" disabled={busy} onClick={() => void read()}>{page ? 'Refresh validation from first page' : 'Read original validation result'}</button>
        {page?.nextCursor && <button className="btn" disabled={busy} onClick={() => void read(page.nextCursor)}>Next validation page</button>}
        {busy && <button className="btn" onClick={stop}>Stop reading validation</button>}
        {page && <button className="btn" onClick={hide}>Hide validation result</button>}
      </div>
      {busy && <p role="status">Reading original validation result…</p>}
      {message && <p role={failed ? 'alert' : 'status'}>{message}</p>}
      {page && <>
        <p role="status">Original verdict: <strong>{page.summary.valid ? 'Passed' : 'Rejected'}</strong>. Validation does not activate configuration or confirm controller application.</p>
        <p>Result ID: <span className="mono">{page.summary.resultID}</span></p>
        <p>Finalized <time dateTime={page.summary.finalizedAt}>{new Date(page.summary.finalizedAt).toLocaleString()}</time> · Retained until <time dateTime={page.summary.expiresAt}>{new Date(page.summary.expiresAt).toLocaleString()}</time></p>
        {page.summary.issue && <p>Collection issue: {issues[page.summary.issue] ?? 'Unrecognized issue'}.</p>}
        {!page.supported && <p>This server uses result details that this dashboard cannot interpret. The observation remains read-only.</p>}
        <p>{page.summary.summaryOnly ? `Summary only for ${formatNumber(identity.itemCount)} original resources. No per-resource results are retained.` : `${formatNumber(page.summary.count)} original resource results. Showing ${page.items.length ? `${formatNumber(page.items[0].ordinal)}–${formatNumber(page.items.at(-1)!.ordinal)}` : '0'} on this page.`}</p>
        {page.items.length > 0 && <div className="table-scroll" role="region" aria-label="Original validation resource results" tabIndex={0}><table className="data-table">
          <thead><tr><th>Ordinal</th><th>Kind</th><th>Resource ID</th><th>Change</th><th>Issue</th><th>Source token</th><th>Document</th><th>Item</th><th>Original UID</th><th>Original version</th></tr></thead>
          <tbody>{page.items.map(item => <tr key={item.ordinal}><td>{formatNumber(item.ordinal)}</td><td>{item.kind}</td><td className="mono">{item.id}</td><td>{item.change ? changes[item.change] ?? 'Unrecognized change' : 'Not reported'}</td><td>{item.issue ? issues[item.issue] ?? 'Unrecognized issue' : 'None reported'}</td><td className="mono">{item.source}</td><td>{formatNumber(item.sourceDocument)}</td><td>{formatNumber(item.sourceItem)}</td><td className="mono">{item.uid ?? 'Not reported'}</td><td className="mono">{item.resourceVersion ?? 'Not reported'}</td></tr>)}</tbody>
        </table></div>}
        <p className="muted">Source tokens preserve the original input ordering. Local file names and resource contents are not part of this retained view.</p>
      </>}
      <p className="muted">Each read returns at most 100 results. Reading, refreshing, hiding or leaving this view never revalidates, activates or cancels the operation.</p>
    </>}
  </section>;
}

import { useEffect, useId, useRef, useState } from 'react';
import { useQuery, useQueryClient } from '@tanstack/react-query';
import { useDashboardSession } from '../auth/SessionBoundary';
import { loadAction, loadActions } from '../api/actions';
import type { Action, ActionReview } from '../api/generated';
import { ManagementError } from '../api/session';
import { mutationReceipt } from '../api/resources';
import { useManagementMutation } from '../hooks/mutations';
import { MutationReceipt, OperationLink } from './OperationReceipt';
import { formatDateTime } from '../lib/format';

export function MonitorActions({ monitorID }: { monitorID: string }) {
  const session = useDashboardSession();
  const queries = useQueryClient();
  const [cursors, setCursors] = useState(['']);
  const [draft, setDraft] = useState<Action>();
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string>();
  const request = useRef<AbortController | undefined>(undefined);
  const cursor = cursors[cursors.length - 1];
  const page = useQuery({ queryKey: ['management', 'actions', monitorID, cursor], queryFn: ({ signal }) => loadActions(session, monitorID, cursor, signal), enabled: session.can('ListActions'), refetchInterval: query => cursor || query.state.data?.nextCursor ? false : 5000, gcTime: 0 });
  const mutation = useManagementMutation(mutationReceipt);
  useEffect(() => {
    const reset = () => { request.current?.abort(); request.current = undefined; setDraft(undefined); setError(undefined); setLoading(false); setCursors(['']); };
    const unsubscribe = session.onReset(reset);
    return () => { request.current?.abort(); request.current = undefined; unsubscribe(); };
  }, [session]);
  if (!session.can('ListActions')) return null;
  const unavailable = loading || mutation.pending || mutation.error?.reason === 'unconfirmed' || page.isError;
  const begin = async (id: string) => {
    if (unavailable || request.current || !session.can('GetAction') || !session.can('ReviewAction')) return;
    const controller = new AbortController();
    request.current = controller;
    setLoading(true); setError(undefined); mutation.clearFeedback();
    try {
      const action = await loadAction(session, monitorID, id, controller.signal);
      if (request.current !== controller || controller.signal.aborted) return;
      if (action.state !== 'unknown' || !action.reviewRevision || !action.incarnationUID) throw new ManagementError('invalid', 'This action no longer has an unknown outcome that can be reviewed. Refresh its observation.');
      setDraft(action);
    } catch (err) {
      if (request.current === controller && !controller.signal.aborted) setError(err instanceof ManagementError ? err.message : 'The latest action observation is unavailable.');
    } finally { if (request.current === controller) { request.current = undefined; setLoading(false); } }
  };
  const submit = async (input: { resolution: ActionReview['resolution']; reason: string; note: string; evidenceRefs: string[] }) => {
    if (!draft || mutation.pending || mutation.error?.reason === 'unconfirmed') return;
    try {
      await mutation.execute(`/api/v2/actions/${encodeURIComponent(draft.id)}/review`, { operation: 'ReviewAction', method: 'POST', resourceVersion: draft.reviewRevision,
        body: { revision: draft.reviewRevision, resolution: input.resolution, reason: input.reason.trim(), ...(input.note.trim() ? { note: input.note.trim() } : {}), ...(input.evidenceRefs.length ? { evidenceRefs: input.evidenceRefs } : {}) } });
      setDraft(undefined);
      await queries.invalidateQueries({ queryKey: ['management'] });
      await queries.invalidateQueries({ queryKey: ['history', monitorID] });
      await queries.invalidateQueries({ queryKey: ['durable-state', monitorID] });
    } catch { /* Preserve the reviewed observation. Never rebase or retry a write. */ }
  };
  return <section className="card" aria-label="Action outcomes">
    <h2 className="card-title">Action outcomes</h2>
    <p>Provider observations and operator reviews are recorded separately. Reviewing an action never repeats it.</p>
    {page.isPending && <p role="status">Loading action outcomes…</p>}
    {page.isError && <p role="alert">Action outcomes are unavailable. <button className="btn" onClick={() => void page.refetch()}>Retry reading actions</button></p>}
    {loading && <p role="status">Loading the latest action before review…</p>}
    {error && <p role="alert">{error}</p>}
    {page.data && <>
      {page.data.items.length === 0 ? <p>No retained actions for this monitor.</p> : <ul className="stack-12">{page.data.items.map(action => <li key={action.id} style={{ overflowWrap: 'anywhere' }}>
        <strong>{action.kind === 'intervention' ? 'Recovery' : action.kind === 'code' ? 'Notification' : action.kind || 'Action'}</strong> · <span className="mono">{action.id}</span>
        <p>Provider outcome: {(action.outcome || action.state).replaceAll('_', ' ')}. {action.held === true ? 'Held for investigation.' : action.held === false ? 'No investigation hold.' : 'Hold status unavailable.'}</p>
        {action.updatedAt && <p className="muted">Last recorded change: {formatDateTime(action.updatedAt)}</p>}
        {action.lateEvidence && <p>Later provider evidence: {action.lateEvidence.outcome}, recorded {formatDateTime(action.lateEvidence.recordedAt)}.</p>}
        {action.conflictingEvidence && <p role="alert">Conflicting provider evidence: {action.conflictingEvidence.outcome}, recorded {formatDateTime(action.conflictingEvidence.recordedAt)}. The investigation hold remains.</p>}
        {action.state === 'unknown' && action.executorFenced !== true && <p>The original executor has not been proven stopped. A conclusive review cannot release the hold.</p>}
        {action.review && <div>
          <p>Operator review: {action.review.resolution} · {action.review.actor} at {formatDateTime(action.review.reviewedAt)}.</p>
          <p style={{ whiteSpace: 'pre-wrap', overflowWrap: 'anywhere' }}>{[action.review.reason, action.review.note].filter(Boolean).join('\n')}</p>
          {action.review.evidenceRefs?.length ? <p style={{ whiteSpace: 'pre-wrap', overflowWrap: 'anywhere' }}>Evidence references (not fetched): {action.review.evidenceRefs.join('\n')}</p> : null}
          {action.review.conflict && <p role="alert">Later provider evidence conflicts with this review. The hold is restored.</p>}
        </div>}
        {action.state === 'unknown' && session.can('GetAction') && session.can('ReviewAction') && <button className="btn" disabled={unavailable} onClick={() => void begin(action.id)}>Review action {action.id}</button>}
      </li>)}</ul>}
      <div className="row" style={{ gap: 8 }}>
        <button className="btn" disabled={unavailable || cursors.length === 1} onClick={() => setCursors(cursors.slice(0, -1))}>Previous actions</button>
        <span>Page {cursors.length}</span>
        <button className="btn" disabled={unavailable || !page.data.nextCursor} onClick={() => setCursors([...cursors, page.data.nextCursor!])}>Next actions</button>
        <button className="btn" disabled={loading || mutation.pending} onClick={() => { setCursors(['']); void page.refetch(); }}>Refresh actions</button>
      </div>
      {(cursor || page.data.nextCursor) && <p className="muted">This paginated view stays fixed while you browse. Refresh actions to start from current observations.</p>}
    </>}
    {mutation.error && <p role="alert">{mutation.error.message}{mutation.error.status === 412 ? ' The action changed since this review was opened. Close this draft and inspect its latest observation before reviewing again.' : ''}{mutation.error.reason === 'unconfirmed' ? ' Inspect the original operation before taking another action.' : ''}</p>}
    {mutation.error?.response?.operationID && <OperationLink id={mutation.error.response?.operationID} label="Inspect submitted review" />}
    {mutation.response && <MutationReceipt response={mutation.response} />}
    {draft && <ReviewDialog key={`${draft.id}/${draft.reviewRevision}`} action={draft} pending={mutation.pending} unconfirmed={mutation.error?.reason === 'unconfirmed'} onCancel={() => setDraft(undefined)} onSubmit={submit} />}
  </section>;
}

function ReviewDialog({ action, pending, unconfirmed, onCancel, onSubmit }: { action: Action; pending: boolean; unconfirmed: boolean; onCancel: () => void; onSubmit: (input: { resolution: ActionReview['resolution']; reason: string; note: string; evidenceRefs: string[] }) => Promise<void> }) {
  const heading = useId();
  const root = useRef<HTMLDivElement>(null);
  const [resolution, setResolution] = useState<ActionReview['resolution']>('inconclusive');
  const [reason, setReason] = useState('');
  const [note, setNote] = useState('');
  const [evidence, setEvidence] = useState('');
  const refs = evidence.split('\n').map(ref => ref.trim()).filter(Boolean);
  const bytes = (text: string) => new TextEncoder().encode(text).byteLength;
  const canConclude = (value: string) => action.executorFenced === true && !action.conflictingEvidence && (!action.lateEvidence || action.lateEvidence.outcome === value);
  const valid = !!reason.trim() && [reason, note].every(text => bytes(text) <= 4096 && !/[\0\r]/.test(text)) && refs.length <= 8 && new Set(refs).size === refs.length && refs.every(ref => bytes(ref) <= 2048 && !/\p{Cc}/u.test(ref)) && (resolution === 'inconclusive' || canConclude(resolution));
  useEffect(() => {
    const previous = document.activeElement;
    root.current?.querySelector<HTMLElement>('select')?.focus();
    return () => { if (previous instanceof HTMLElement && previous.isConnected) previous.focus(); };
  }, []);
  return <div ref={root} className="management-modal" role="dialog" aria-modal="true" aria-labelledby={heading} onKeyDown={event => {
    if (event.key === 'Escape' && !pending) onCancel();
    if (event.key !== 'Tab') return;
    const controls = root.current?.querySelectorAll<HTMLElement>('select:not(:disabled),textarea:not(:disabled),button:not(:disabled)');
    if (!controls?.length) { event.preventDefault(); return; }
    if (event.shiftKey && document.activeElement === controls[0]) { event.preventDefault(); controls[controls.length - 1].focus(); }
    else if (!event.shiftKey && document.activeElement === controls[controls.length - 1]) { event.preventDefault(); controls[0].focus(); }
  }}><form className="card" onSubmit={event => { event.preventDefault(); if (valid && !pending && !unconfirmed) void onSubmit({ resolution, reason, note, evidenceRefs: refs }); }}>
    <h2 id={heading}>Review unknown action</h2>
    <p>Action <span className="mono">{action.id}</span> belongs to monitor incarnation <span className="mono">{action.incarnationUID}</span>. Your review is an audited assertion; it does not replace provider evidence or replay the action.</p>
    <label className="management-field">Conclusion<select className="input" value={resolution} disabled={pending} onChange={event => setResolution(event.target.value as ActionReview['resolution'])}>
      <option value="inconclusive">Inconclusive — keep investigating</option>
      <option value="accepted" disabled={!canConclude('accepted')}>Accepted — evidence shows the operation was accepted</option>
      <option value="rejected" disabled={!canConclude('rejected')}>Rejected — evidence shows the operation was rejected</option>
    </select></label>
    {action.executorFenced !== true && <p>The executor has not been proven stopped. Only an inconclusive review is available.</p>}
    {action.lateEvidence && <p>Recorded provider evidence: {action.lateEvidence.outcome}. A review cannot contradict this observation.</p>}
    {action.conflictingEvidence && <p>Contradictory provider evidence remains unresolved. Only an inconclusive review is available.</p>}
    <label className="management-field">Reason<textarea className="input" required maxLength={4096} disabled={pending} value={reason} onChange={event => setReason(event.target.value)} /></label>
    <label className="management-field">Note (optional)<textarea className="input" maxLength={4096} disabled={pending} value={note} onChange={event => setNote(event.target.value)} /></label>
    <label className="management-field">Evidence references (optional)<textarea className="input" disabled={pending} value={evidence} maxLength={16392} onChange={event => setEvidence(event.target.value)} /><span className="muted">One reference per line, at most 8. CPRa records this text without opening links. Keep credentials out of notes and references.</span></label>
    {!valid && reason.trim() && <p role="alert">Each reason and note must fit within 4,096 UTF-8 bytes; each evidence reference within 2,048 bytes, with at most 8 references. A conclusive review also requires a stopped or fenced executor.</p>}
    <div className="row" style={{ gap: 8 }}><button type="button" className="btn" disabled={pending} onClick={onCancel}>Cancel</button><button type="submit" className="btn" disabled={!valid || pending || unconfirmed}>{pending ? 'Submitting…' : 'Record review'}</button></div>
  </form></div>;
}

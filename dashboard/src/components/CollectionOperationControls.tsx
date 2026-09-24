import { useEffect, useRef, useState } from 'react';
import { useQuery } from '@tanstack/react-query';
import { useAccess, useDashboardSession } from '../auth/SessionBoundary';
import { collectionCall, collectionMutable, collectionPermissions, collectionValidationMessage, collectionValidationPendingDelay, readCollection, readCollectionValidation, type CollectionIdentity, type CollectionValidationPage } from '../api/collectionOperations';
import { supportedCollectionIdentity, type OperationObservation } from '../api/operations';
import { fileNormalizationProfile } from '../api/collectionInventory';
import { ManagementError } from '../api/session';
import type { Operation } from '../api/generated';
import { formatNumber } from '../lib/format';
import { ConfirmDialog } from './ConfirmDialog';

type Action = 'ValidateOperation' | 'ActivateOperation' | 'CancelOperation';
type Lifetime = { active: boolean; generation: number; busy: boolean; uncertain: boolean; validationAttempted: boolean; abort?: AbortController };

/** Controls bind to the original immutable inventory. Retained validation and a
 * fresh receipt are observations; the server still performs conditional admission. */
export function CollectionOperationControls({ id, identity, operation, onProgress }: {
  id: string; identity: CollectionIdentity; operation: OperationObservation; onProgress: () => void;
}) {
  const session = useDashboardSession();
  const access = useAccess();
  const owned = useRef<Lifetime>({ active: true, generation: 0, busy: false, uncertain: false, validationAttempted: false });
  const [observed, setObserved] = useState(operation);
  const [validation, setValidation] = useState<CollectionValidationPage>();
  const [confirm, setConfirm] = useState<'activate' | 'cancel'>();
  const [busy, setBusy] = useState(false);
  const [uncertain, setUncertain] = useState(false);
  const [message, setMessage] = useState('');
  const capabilities = useQuery({ queryKey: ['management', 'import-capabilities'], queryFn: ({ signal }) => session.get<unknown>('/api/v2/discovery', { signal }), enabled: session.can('GetCapabilities'), retry: false });
  const allowed = collectionPermissions(session, capabilities.data?.data);
  const operator = access.phase === 'authenticated' && access.access?.role === 'operator';
  const known = supportedCollectionIdentity(observed);
  const supported = observed.id === id && !!known && known.identityFormat === identity.identityFormat && known.contentDigest === identity.contentDigest &&
    known.itemCount === identity.itemCount && known.normalizationProfile === identity.normalizationProfile;
  const lifetimeKey = JSON.stringify([id, identity.identityFormat, identity.contentDigest, identity.itemCount, identity.normalizationProfile]);

  useEffect(() => {
    const state = owned.current;
    state.active = true; state.busy = false; state.uncertain = false; state.validationAttempted = false;
    setBusy(false); setUncertain(false); setValidation(undefined); setConfirm(undefined); setMessage('');
    const close = () => { state.active = false; state.generation++; state.abort?.abort(); state.abort = undefined; };
    const unsubscribe = session.onReset(close);
    return () => { unsubscribe(); close(); };
  }, [session, lifetimeKey]);
  useEffect(() => { setObserved(operation); }, [operation]);

  const can = (action: string) => operator && supported && allowed.has('GetOperation') && allowed.has(action) &&
    (!['ValidateOperation', 'ActivateOperation'].includes(action) || identity.normalizationProfile === fileNormalizationProfile);
  const fullUpload = (value: OperationObservation) => value.uploaded === identity.itemCount && !value.executionResult;
  const inactive = (value: OperationObservation) => !value.executionResult && collectionMutable(value as Operation);
  const validationReady = (value: OperationObservation) => value.state === 'uploading' && fullUpload(value);
  const activationReady = (value: OperationObservation, page = validation) => value.state === 'validated' && fullUpload(value) && value.validated !== false &&
    !!page?.supported && page.operationID === id && page.summary.valid && !page.summary.summaryOnly && page.summary.count === identity.itemCount &&
    !!page.summary.planID && !!page.summary.planDigest && Date.parse(page.summary.expiresAt) > Date.now();

  const run = async (task: (signal: AbortSignal, current: () => boolean) => Promise<void>, mutation = false) => {
    const state = owned.current;
    if (!state.active || state.busy || !supported) return;
    state.busy = true;
    const generation = ++state.generation;
    const controller = new AbortController(); state.abort = controller;
    const current = () => state.active && generation === state.generation && !controller.signal.aborted;
    setBusy(true); setMessage('');
    try { await task(controller.signal, current); }
    catch (error) {
      if (!current()) return;
      if (mutation) { state.uncertain = true; setUncertain(true); setValidation(undefined); onProgress(); }
      setConfirm(undefined);
      setMessage(error instanceof ManagementError && error.reason === 'unconfirmed'
        ? 'The original operation outcome is unconfirmed. No automatic retry was made. Read original progress before another control; accepted changes may already be applying.'
        : mutation ? 'This control could not be confirmed. Read original progress before continuing. No automatic retry was made, and prior changes were not rolled back.'
          : 'The original operation could not be read. No control was submitted.');
    } finally {
      if (state.active && generation === state.generation) { state.busy = false; state.abort = undefined; setBusy(false); }
    }
  };

  const read = () => run(async (signal, current) => {
    if (!allowed.has('GetOperation')) return;
    const response = await readCollection(session, identity, id, signal);
    if (!current()) return;
    setObserved(response.data); setConfirm(undefined); setValidation(undefined);
    const unknown = response.data.state === 'unrecognized';
    owned.current.uncertain = unknown; setUncertain(unknown); onProgress();
    if (!unknown && validationReady(response.data)) owned.current.validationAttempted = false;
    setMessage(unknown ? 'The original state is unsupported. Controls remain unavailable.' : 'Original progress was read. No validation, activation or cancellation was submitted.');
  });

  const review = (cursor = '') => run(async (signal, current) => {
    if (!can('ActivateOperation') || !allowed.has('GetOperationValidation') || owned.current.uncertain) return;
    const previous = validation;
    setConfirm(undefined);
    try {
      const response = await readCollectionValidation(session, identity, id, signal, cursor, previous);
      if (!current()) return;
      setValidation(response.data);
      const receipt = await readCollection(session, identity, id, signal);
      if (!current()) return;
      setObserved(receipt.data); onProgress();
      if (!activationReady(receipt.data, response.data)) {
        setMessage('The retained result or current operation is not eligible for activation. No control was submitted.');
        return;
      }
      setConfirm('activate');
    } catch (error) {
      if (!current()) return;
      setValidation(undefined);
      const delay = collectionValidationPendingDelay(error, id);
      setMessage(delay === undefined ? collectionValidationMessage(error) : `Original validation is pending. Read again after ${Math.ceil(delay / 1000)} seconds; no validation request was resubmitted.`);
    }
  });

  const submit = (action: Action) => {
    const reviewed = validation;
    setConfirm(undefined);
    return run(async (signal, current) => {
      if (!can(action) || owned.current.uncertain) return;
      if (action === 'ValidateOperation' && owned.current.validationAttempted) return;
      // A confirmation cannot authorize a stale receipt. Refresh before sending
      // the single bodyless control; the server fences any later state change.
      const before = await readCollection(session, identity, id, signal);
      if (!current()) return;
      setObserved(before.data);
      if (action === 'ValidateOperation' ? !validationReady(before.data) : action === 'ActivateOperation' ? !allowed.has('GetOperationValidation') || !activationReady(before.data, reviewed) : !inactive(before.data)) {
        setValidation(undefined); setMessage('Original progress changed or the review expired. No control was submitted.'); onProgress(); return;
      }
      if (action === 'ValidateOperation') owned.current.validationAttempted = true;
      const response = await collectionCall(session, allowed, action, identity, undefined, id, signal);
      if (!current()) return;
      setObserved(response.data as Operation); setValidation(undefined); onProgress();
      setMessage(action === 'ValidateOperation' ? 'Validation was accepted for the original collection. Read its retained result before reviewing activation; no activation was requested.'
        : action === 'ActivateOperation' ? 'Activation was requested for the original collection. Committed changes and controller application are reported separately; partial results are possible.'
          : 'Cancellation was requested for the original operation. Previously committed changes and external actions are not reversed.');
    }, true);
  };

  if (!operator) return <section className="card" aria-label="Original collection controls"><h2>Original collection controls</h2><p>Read-only access. Collection controls require an authorized operator.</p></section>;
  if (!supported) return <section className="card" aria-label="Original collection controls"><h2>Original collection controls</h2><p>This collection identity or normalization profile is unsupported for dashboard controls. Its receipt remains read only; use the compatible SDK or CLI.</p></section>;
  return <section className="card" aria-label="Original collection controls">
    <h2>Original collection controls</h2>
    <p>Continue operation <span className="mono">{id}</span> with its {formatNumber(identity.itemCount)} original resources. These controls never create a replacement collection.</p>
    {identity.normalizationProfile !== fileNormalizationProfile && <p>Unprofiled collections can be read or canceled while inactive. Use the compatible SDK or CLI to validate and activate them.</p>}
    <div className="row" style={{ gap: 12, flexWrap: 'wrap' }}>
      <button className="btn" disabled={busy || uncertain || !can('ValidateOperation') || owned.current.validationAttempted || !validationReady(observed)} onClick={() => void submit('ValidateOperation')}>Validate original collection</button>
      <button className="btn" disabled={busy || uncertain || !can('ActivateOperation') || !allowed.has('GetOperationValidation') || observed.state !== 'validated' || !fullUpload(observed)} onClick={() => void review()}>Review activation</button>
      <button className="btn" disabled={busy || !allowed.has('GetOperation')} onClick={() => void read()}>Read original progress</button>
      <button className="btn" disabled={busy || uncertain || !can('CancelOperation') || !inactive(observed)} onClick={() => setConfirm('cancel')}>Cancel inactive operation</button>
    </div>
    {busy && <p role="status">Reading or updating the original operation…</p>}
    {message && <p role={uncertain ? 'alert' : 'status'}>{message}</p>}
    {!can('ValidateOperation') && !can('ActivateOperation') && !can('CancelOperation') && <p role="status">This server and identity do not offer these collection controls.</p>}
    {validation && <section aria-label="Activation review results">
      <h3>Retained validation review</h3>
      <p>Original verdict: {validation.summary.valid ? 'Passed' : 'Rejected'}. The sealed summary covers {formatNumber(validation.summary.count)} resources. Result: <span className="mono">{validation.summary.resultID}</span>.</p>
      {!validation.supported && <p role="status">This result contains unsupported vocabulary and cannot authorize activation.</p>}
      <div className="table-scroll" role="region" aria-label="Activation review resource results" tabIndex={0}><table className="data-table"><caption>Current validation review page</caption><thead><tr><th scope="col">Ordinal</th><th scope="col">Resource</th><th scope="col">Source / document / item</th><th scope="col">Change</th><th scope="col">Issue</th></tr></thead>
        <tbody>{validation.items.map(item => <tr key={item.ordinal}><td>{item.ordinal}</td><td>{item.kind}/{item.id}</td><td>{Number(item.source.slice(7))} / {item.sourceDocument} / {item.sourceItem}</td><td>{item.change ?? 'Not evaluated'}</td><td>{item.issue ?? 'None'}</td></tr>)}</tbody></table></div>
      {validation.nextCursor && <button className="btn" disabled={busy || uncertain} onClick={() => void review(validation.nextCursor)}>Next activation review page</button>}
    </section>}
    {confirm && <ConfirmDialog title={confirm === 'activate' ? 'Activate this original collection?' : 'Cancel this inactive operation?'} pending={busy}
      cancelLabel="Keep reviewing" confirmLabel={confirm === 'activate' ? 'Confirm activation' : 'Confirm cancellation'} pendingLabel="Submitting…" onCancel={() => setConfirm(undefined)}
      onConfirm={() => void submit(confirm === 'activate' ? 'ActivateOperation' : 'CancelOperation')}>
      <p>Original operation: <span className="mono">{id}</span>. Original resource count: {formatNumber(identity.itemCount)}.</p>
      {confirm === 'activate' ? <><p>Sealed validation result: <span className="mono">{validation?.summary.resultID}</span>. Activation changes active monitoring, recovery and notification configuration.</p>
        <p>Each resource is applied conditionally. Conflicts or dependency failures can produce partial commits; rollback cannot reverse external actions.</p></>
        : <p>This operation was observed inactive. Cancellation does not undo previously committed configuration or external actions; it can stop remaining work if activation has begun.</p>}
    </ConfirmDialog>}
  </section>;
}

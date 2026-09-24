import { useEffect, useRef, useState } from 'react';
import { useQuery } from '@tanstack/react-query';
import { Link } from 'react-router-dom';
import { useAccess, useDashboardSession } from '../auth/SessionBoundary';
import { BrowserCollection, CollectionImportError, type ImportProgress } from '../import/collection';
import { collectionFileSources } from '../import/files';
import type { WorkerCollectionItemSummary, WorkerCollectionSummary } from '../api/collectionInventory';
import { collectionCall, collectionDelay, collectionMutable, collectionCancelable, collectionPermissions, collectionTerminal, prepareCollection, readCollection, readCollectionValidation, collectionValidationPendingDelay, collectionValidationMessage, type CollectionValidationPage } from '../api/collectionOperations';
import { ManagementError, type APIResponse, type CollectionRequestOperation } from '../api/session';
import type { ApplyResult, Operation, Preflight } from '../api/generated';
import { operationID } from '../api/operations';
import { DirtyDraftGuard } from '../components/DirtyDraftGuard';
import { CollectionExecution } from '../components/CollectionExecution';

type PreviewItem = WorkerCollectionItemSummary & { sourceName: string };
type PrivateImport = { collection?: BrowserCollection; ticket?: string; expiresAt?: string; createAttempted: boolean; validationAttempted?: boolean; activationAttempted?: boolean; sourceNames?: readonly string[]; operation?: Operation; busy: boolean; active: boolean; generation: number; controller?: AbortController };
const durableSteps = ['PrepareCollection', 'CreateOperation', 'UploadOperation', 'ValidateOperation', 'GetOperationValidation', 'GetOperation'];
const unavailable = 'This server or identity does not offer the complete staging and validation flow. You can prepare files locally and use server preview if available; no apply is submitted.';

export default function CollectionImport() {
  const session = useDashboardSession();
  const access = useAccess();
  const lifetime = useRef<PrivateImport>({ active: true, generation: 0, busy: false, createAttempted: false });
  const [summary, setSummary] = useState<WorkerCollectionSummary>();
  const [preview, setPreview] = useState<readonly PreviewItem[]>([]);
  const [offset, setOffset] = useState(0);
  const [progress, setProgress] = useState<ImportProgress>();
  const [busy, setBusy] = useState('');
  const [feedback, setFeedback] = useState('');
  const [uncertain, setUncertain] = useState(false);
  const [preflight, setPreflight] = useState<Preflight>();
  const [validation, setValidation] = useState<CollectionValidationPage>();
  const [validationStatus, setValidationStatus] = useState('Not requested');
  const [waitKind, setWaitKind] = useState<'progress' | 'validation'>('progress');
  const waitDelay = useRef(5000);
  const [operation, setOperation] = useState<Operation>();
  const [waiting, setWaiting] = useState(false);
  const [confirm, setConfirm] = useState<'activate' | 'cancel'>();
  const [cursor, setCursor] = useState('');
  const [resultPage, setResultPage] = useState<ApplyResult[]>([]);
  const waitAbort = useRef<AbortController>();
  const confirmKeep = useRef<HTMLButtonElement>(null);
  const capabilities = useQuery({ queryKey: ['management', 'import-capabilities'], queryFn: ({ signal }) => session.get<unknown>('/api/v2/discovery', { signal }), enabled: access.phase === 'authenticated' && session.can('GetCapabilities'), retry: false });
  const allowed = collectionPermissions(session, capabilities.data?.data);
  const operator = access.phase === 'authenticated' && access.access?.role === 'operator';
  const durable = operator && durableSteps.every(step => allowed.has(step));
  const stopWaiting = () => { waitAbort.current?.abort(); setWaiting(false); };

  useEffect(() => {
    const owned = lifetime.current;
    owned.active = true;
    const close = () => {
      owned.active = false; owned.generation++; owned.controller?.abort(); owned.collection?.close(); owned.collection = undefined;
      owned.ticket = undefined; owned.expiresAt = undefined; owned.operation = undefined; owned.sourceNames = undefined; waitAbort.current?.abort();
    };
    const unsubscribe = session.onReset(close);
    return () => { unsubscribe(); close(); };
  }, [session]);
  useEffect(() => { if (confirm) confirmKeep.current?.focus(); }, [confirm]);

  const storeOperation = (value: Operation) => {
    const observation = { ...value };
    if (observation.executionResult) { delete observation.items; delete observation.nextCursor; }
    lifetime.current.operation = observation; setOperation(observation); setResultPage(observation.items ?? []); setCursor(observation.nextCursor ?? '');
    if (collectionTerminal(value)) {
      lifetime.current.collection?.close(); lifetime.current.collection = undefined; lifetime.current.ticket = undefined;
    }
  };
  const failure = (error: unknown) => {
    if (error instanceof ManagementError) {
      if (error.reason === 'unconfirmed') {
        setUncertain(true);
        const id = error.response?.operationID;
        if (!lifetime.current.operation && operationID(id) && summary) storeOperation({ ...summary, id, state: 'unrecognized' });
        setFeedback(lifetime.current.activationAttempted && lifetime.current.operation
          ? 'Activation outcome unconfirmed. Read the original operation to reconcile it before another activation request. Accepted changes may already be applying; no automatic retry or new operation was submitted.'
          : 'Outcome unconfirmed. No automatic retry was made. Read the original operation before continuing. If creation returned no handle, only an explicit retry using this tab’s original admission ticket is available.');
      } else if (error.reason === 'not-admitted') setFeedback('No configuration change was submitted. The server did not confirm admission. Inspect the original operation before retrying.');
      else if (error.status === 410) setFeedback('This original operation has expired or been invalidated. It cannot be resumed. No new apply was started.');
      else if (error.reason === 'forbidden' || error.status === 403) setFeedback('Your identity is not permitted to perform this step. No further step was submitted.');
      else if (error.status === 409 || error.status === 412) setFeedback('The original operation cannot continue with this request. Read its original validation result; changed intent requires a new collection operation. No validation or activation was retried.');
      else setFeedback('This step could not be completed. No automatic retry or following step was submitted. Read the original operation when available.');
    } else if (error instanceof CollectionImportError) setFeedback(lifetime.current.createAttempted
      ? 'The private collection is unavailable. No following step was submitted. Inspect the original operation; earlier accepted work is not reversed.' : error.message);
    else setFeedback('The import step could not be completed. No following step was submitted.');
  };
  async function run(label: string, task: (signal: AbortSignal) => Promise<void>) {
    const owned = lifetime.current;
    if (!owned.active || owned.busy) return;
    owned.busy = true; owned.controller = new AbortController(); const generation = owned.generation;
    stopWaiting(); setBusy(label); setFeedback('');
    try { await task(owned.controller.signal); }
    catch (error) { if (owned.active && owned.generation === generation) failure(error); }
    finally { if (owned.active && owned.generation === generation) { owned.busy = false; setBusy(''); } }
  }
  function current(signal: AbortSignal) { return lifetime.current.active && !signal.aborted; }
  async function loadPage(collection: BrowserCollection, start: number, signal: AbortSignal) {
    const rows = await collection.page(start);
    if (!current(signal)) return;
    setPreview(rows.map(row => ({ ...row, sourceName: collection.sourceName(Number(row.source.token.slice(7))) })));
    setOffset(start);
  }
  async function select(files: File[]) {
    if (!files.length || summary || lifetime.current.busy || !operator) return;
    await run('Preparing files', async signal => {
      try {
        const collection = await BrowserCollection.prepare(files, { signal, onProgress: value => { if (current(signal)) setProgress(value); } });
        if (!current(signal)) { collection.close(); return; }
        lifetime.current.collection = collection;
        lifetime.current.sourceNames = Array.from({ length: collection.summary.sourceCount }, (_, index) => collection.sourceName(index + 1));
        setSummary(collection.summary); setUncertain(false); setPreflight(undefined); setOperation(undefined);
        await loadPage(collection, 0, signal);
      } catch (error) {
        if (current(signal) && error instanceof CollectionImportError && error.location?.source) {
          // Names are local display data only. Never attach them to errors or requests.
          const ordered = collectionFileSources(files);
          const name = ordered[error.location.source - 1]?.name;
          if (name) { setFeedback(`${error.message} Source: ${name}; document ${error.location.document ?? 'unavailable'}, item ${error.location.item ?? 'unavailable'}.`); return; }
        }
        throw error;
      }
    });
  }
  function discard() {
    if (lifetime.current.busy) return;
    stopWaiting(); lifetime.current.collection?.close(); lifetime.current.collection = undefined;
    lifetime.current.ticket = undefined; lifetime.current.expiresAt = undefined; lifetime.current.createAttempted = false; lifetime.current.validationAttempted = false; lifetime.current.activationAttempted = false; lifetime.current.operation = undefined; lifetime.current.sourceNames = undefined;
    setSummary(undefined); setPreview([]); setProgress(undefined); setPreflight(undefined); setValidation(undefined); setValidationStatus('Not requested'); setOperation(undefined); setResultPage([]); setCursor(''); setFeedback(''); setUncertain(false);
  }
  async function previewServer() {
    await run('Previewing on server', async signal => {
      const collection = lifetime.current.collection;
      if (!collection || !summary) return;
      const response = await collectionCall(session, allowed, 'PreflightCollection', summary, await collection.preflightBody(), undefined, signal);
      if (current(signal)) { setPreflight(response.data as Preflight); setValidation(undefined); setValidationStatus('Not requested'); setFeedback((response.data as Preflight).valid ? 'Server preview passed. This preview did not activate anything.' : 'Server preview found invalid resources or references. This preview did not activate anything.'); }
    });
  }
  async function create() {
    await run('Creating inactive operation', async signal => {
      const owned = lifetime.current, collection = owned.collection;
      if (!collection || !summary || !durable || owned.operation) return;
      if (!owned.ticket) {
        if (owned.createAttempted) throw new ManagementError('invalid', 'The original admission ticket is unavailable.');
        const prepared = await prepareCollection(session, allowed, await collection.createBody(), signal);
        if (!current(signal)) return;
        owned.ticket = prepared.ticket; owned.expiresAt = prepared.expiresAt;
      }
      if (Date.parse(owned.expiresAt ?? '') <= Date.now()) throw new ManagementError('http', 'The original admission ticket expired.', 410);
      owned.createAttempted = true;
      const response = await collectionCall(session, allowed, 'CreateOperation', summary, await collection.createBody(), undefined, signal, owned.ticket);
      if (current(signal)) { storeOperation(response.data as Operation); setUncertain(false); setPreflight(undefined); setValidation(undefined); setValidationStatus('Not requested'); setFeedback('Inactive operation created. Upload and whole-collection validation are required before activation.'); }
    });
  }
  async function upload() {
    await run('Uploading frozen collection', async signal => {
      const owned = lifetime.current, collection = owned.collection;
      if (!collection || !summary || !collectionMutable(owned.operation) || uncertain || !durable) return;
      let observed = owned.operation!;
      if (observed.uploaded === undefined) throw new ManagementError('invalid', 'Upload progress is unavailable.');
      while (observed.uploaded! < summary.itemCount) {
        const body = await collection.uploadBody(observed.uploaded!);
        const next = body.nextOffset;
        if (next === undefined || next <= observed.uploaded! || next > summary.itemCount || next - observed.uploaded! > 256) { body.close(); throw new ManagementError('invalid', 'Invalid upload boundary.'); }
        const response = await collectionCall(session, allowed, 'UploadOperation', summary, body, observed.id, signal);
        if (!current(signal)) return;
        observed = response.data as Operation; storeOperation(observed); setPreflight(undefined); setValidation(undefined); setValidationStatus('Not requested');
        if (observed.uploaded !== next || !collectionMutable(observed)) { setUncertain(true); throw new ManagementError('unconfirmed', 'The original upload progress could not be confirmed.'); }
      }
      setFeedback('All resources are staged. Validate the complete collection before activation.');
    });
  }
  async function retainValidation(value: CollectionValidationPage, signal: AbortSignal) {
    setValidation(value); setValidationStatus(!value.supported ? 'Unsupported result vocabulary — read only' : value.summary.valid ? 'Passed — original sealed result' : 'Rejected — original sealed result');
    setFeedback(!value.supported ? 'This retained result uses unsupported vocabulary. It is read only and cannot authorize activation.' : value.summary.valid ? 'The original validation passed. No activation was requested.' : 'The original validation was rejected. Review its source-attributed results; changed intent requires a new operation.');
    // A sealed verdict does not determine today's operation state: cancellation
    // may have happened afterward. Observe the original receipt before enabling
    // any next step, and keep the result readable if this separate GET fails.
    try {
      const response = await readCollection(session, value, value.operationID, signal);
      if (current(signal)) { storeOperation(response.data); setUncertain(false); }
    } catch {
      if (current(signal)) { setUncertain(true); setFeedback('The original sealed result is retained below, but current operation progress could not be confirmed. Read original progress before another step; no mutation was submitted.'); }
    }
  }
  function validationFailure(error: unknown, id: string): number | undefined {
    const delay = collectionValidationPendingDelay(error, id);
    if (delay !== undefined) { setValidationStatus('Pending — no verdict yet'); setFeedback('The original validation is pending. Waiting only reads its result; it never resubmits validation.'); return delay; }
    setUncertain(true);
    setValidationStatus(collectionValidationMessage(error));
    setFeedback(collectionValidationMessage(error));
    return undefined;
  }
  async function control(action: 'ValidateOperation' | 'ActivateOperation' | 'CancelOperation') {
    setConfirm(undefined);
    await run(action === 'ValidateOperation' ? 'Requesting whole-collection validation' : action === 'ActivateOperation' ? 'Requesting activation' : 'Requesting cancellation', async signal => {
      const owned = lifetime.current, original = owned.operation;
      if (!summary || !original || !(action === 'CancelOperation' ? collectionCancelable(original) : collectionMutable(original))) return;
      if (action === 'ValidateOperation') {
        if (owned.validationAttempted || original.state !== 'uploading' || original.uploaded !== summary.itemCount) return;
        owned.validationAttempted = true; setValidationStatus('Submission outcome not yet confirmed');
      }
      if (action === 'ActivateOperation') {
        if (!activationReady(original)) return;
        owned.activationAttempted = true;
      }
      const response = await collectionCall(session, allowed, action, summary, undefined, original.id, signal);
      if (!current(signal)) return;
      storeOperation(response.data as Operation); setUncertain(false);
      if (action === 'ValidateOperation') {
        setPreflight(undefined); setValidationStatus('Accepted — awaiting original sealed result');
        setFeedback('Validation was accepted. No verdict or activation is implied; waiting reads the original result.');
        waitDelay.current = collectionDelay(response as APIResponse<Operation>); setWaitKind('validation'); setWaiting(true);
      } else {
        setFeedback(action === 'ActivateOperation' ? 'Activation was requested. Saved changes and controller application are reported separately; partial results are possible.' : 'Cancellation was requested for the original operation. Existing committed changes are not rolled back.');
        if (action === 'ActivateOperation' && !collectionTerminal(response.data as Operation) && (response.data as Operation).state !== 'unrecognized') { setWaitKind('progress'); waitDelay.current = collectionDelay(response as APIResponse<Operation>); setWaiting(true); }
      }
    });
  }
  async function refresh(nextCursor = '') {
    await run('Reading original operation', async signal => {
      const original = lifetime.current.operation;
      if (!summary || !original) return;
      const response = await readCollection(session, summary, original.id, signal, nextCursor);
      if (current(signal)) { storeOperation(response.data); setUncertain(false); if (!nextCursor) setPreflight(undefined); }
    });
  }
  async function refreshValidation(nextCursor = '') {
    await run('Reading original validation result', async signal => {
      const original = lifetime.current.operation;
      if (!summary || !original) return;
      try {
        const response = await readCollectionValidation(session, summary, original.id, signal, nextCursor, validation);
        if (current(signal)) await retainValidation(response.data, signal);
      } catch (error) { if (current(signal)) validationFailure(error, original.id); }
    });
  }
  useEffect(() => {
    if (!waiting || !summary || !operation?.id || !allowed.has(waitKind === 'validation' ? 'GetOperationValidation' : 'GetOperation')) return;
    const controller = new AbortController(); waitAbort.current = controller;
    let timer: ReturnType<typeof setTimeout> | undefined;
    const schedule = (delay: number) => { timer = setTimeout(() => { void poll(); }, delay); };
    const poll = async () => {
      try {
        if (waitKind === 'validation') {
          const response = await readCollectionValidation(session, summary, operation.id, controller.signal);
          if (!lifetime.current.active || controller.signal.aborted) return;
          await retainValidation(response.data, controller.signal);
          if (lifetime.current.active && !controller.signal.aborted) setWaiting(false);
          return;
        }
        const response: APIResponse<Operation> = await readCollection(session, summary, operation.id, controller.signal);
        if (!lifetime.current.active || controller.signal.aborted) return;
        storeOperation(response.data);
        if (collectionTerminal(response.data) || response.data.state === 'unrecognized' || response.data.state === 'validated') { setWaiting(false); return; }
        schedule(collectionDelay(response));
      } catch (error) {
        if (controller.signal.aborted || !lifetime.current.active) return;
        if (waitKind === 'validation') {
          const delay = validationFailure(error, operation.id);
          if (delay !== undefined) { schedule(delay); return; }
        } else setFeedback('Waiting stopped because progress could not be read. The server operation was not canceled.');
        setWaiting(false);
      }
    };
    schedule(waitDelay.current);
    return () => { controller.abort(); clearTimeout(timer); };
    // One polling loop owns one original handle; writes and manual reads stop it.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [waiting, waitKind, operation?.id, summary, session]);

  const results = new Map((preflight?.items ?? resultPage).map(item => [item.id, item]));
  const canStep = (name: CollectionRequestOperation) => operator && allowed.has(name) && !busy;
  function activationReady(original?: Operation) {
    return !uncertain && !!validation?.summary.valid && validation.supported && Date.parse(validation.summary.expiresAt) > Date.now() &&
      original?.state === 'validated' && !original.executionResult && original.id === validation.operationID && original.uploaded === summary?.itemCount;
  }
  const validationWait = !validation && (lifetime.current.validationAttempted || operation?.state === 'validating');
  const canWait = !!operation && (validationWait ? allowed.has('GetOperationValidation') : allowed.has('GetOperation')) &&
    !collectionTerminal(operation) && operation.state !== 'unrecognized' && operation.state !== 'validated';
  const pendingDraft = !!summary && !collectionTerminal(operation);
  return <main className="page">
    <DirtyDraftGuard dirty={pendingDraft || !!busy} />
    <h1>Import configuration files</h1>
    <p>Choose YAML or JSON files for monitors, recipients, notification endpoints, groups and secrets. Files are parsed in a private worker; the preview shows identities and outcomes, never secret values.</p>
    <p className="muted">Up to 1,000 files, 64 MiB of source data, 10,000 resources and 1 MiB per resource. Upload chunks contain at most 256 resources or 4 MiB. File names remain local to this tab.</p>
    <p className="muted">Refreshing or leaving discards this tab’s files and identity key. Server-assisted file reselection is not available yet. Leaving or stopping waiting does not cancel an accepted server operation.</p>
    {!operator && <p role="status">Import requires an operator identity. Existing resources and operation observations remain available to authorized readers.</p>}
    {operator && !durable && <p role="status">{unavailable}</p>}
    {capabilities.isError && <p role="alert">Server capabilities could not be read. Server import steps remain disabled.</p>}
    <section className="card" aria-label="Local file preparation">
      <label htmlFor="collection-files">Configuration files</label>{' '}
      <input id="collection-files" type="file" accept=".yaml,.yml,.json,application/json,application/yaml,text/yaml" multiple disabled={!operator || !!busy || !!summary} onChange={event => { const files = Array.from(event.currentTarget.files ?? []); event.currentTarget.value = ''; void select(files); }} />
      {busy && <p role="status">{busy}…{busy === 'Preparing files' && progress ? ` ${progress.sources} sources, ${progress.items} resources (${progress.phase}).` : ''}</p>}
      {busy === 'Preparing files' && <button className="btn" onClick={() => lifetime.current.controller?.abort()}>Cancel preparation</button>}
      {summary && <>
        <p>{summary.itemCount} resources from {summary.sourceCount} files; {summary.sourceBytes.toLocaleString()} source bytes.</p>
        <button className="btn" disabled={!!busy} onClick={discard}>{operation ? 'Close this import' : 'Discard selection'}</button>{' '}
        <button className="btn" disabled={!canStep('PreflightCollection') || !summary.preflightAvailable || !lifetime.current.collection} onClick={() => { void previewServer(); }}>Preview on server</button>
        {!summary.preflightAvailable && <p>This collection exceeds the 4 MiB ephemeral preview limit. Whole-collection validation is available after staged upload when the server supports it.</p>}
      </>}
    </section>
    {feedback && <p role="alert">{feedback}</p>}
    {!!summary && <section className="card" aria-label="Collection preview">
      <h2>Source-attributed preview</h2><p>Resource contents are hidden. Create, update and unchanged outcomes appear after server validation; no resources are implicitly deleted.</p>
      <table><caption>Resources {preview.length ? offset + 1 : 0}–{offset + preview.length} of {summary.itemCount}</caption><thead><tr><th scope="col">Resource</th><th scope="col">Local file</th><th scope="col">Document / item</th><th scope="col">Outcome</th><th scope="col">Observed version</th></tr></thead><tbody>{preview.map(item => <tr key={item.id}><td>{item.id}</td><td>{item.sourceName}</td><td>{item.source.document} / {item.source.item}</td><td>{results.get(item.id)?.outcome ?? 'Not evaluated'}</td><td>{results.get(item.id)?.oldVersion ?? 'Unavailable'}</td></tr>)}</tbody></table>
      <div className="row" style={{ gap: 12 }}><button className="btn" disabled={!!busy || offset === 0 || !lifetime.current.collection} onClick={() => { void run('Reading preview page', signal => loadPage(lifetime.current.collection!, Math.max(0, offset - 100), signal)); }}>Previous resources</button><button className="btn" disabled={!!busy || offset + preview.length >= summary.itemCount || !lifetime.current.collection} onClick={() => { void run('Reading preview page', signal => loadPage(lifetime.current.collection!, offset + 100, signal)); }}>Next resources</button></div>
    </section>}
    {summary && summary.itemCount > 0 && <section className="card" aria-label="Apply collection">
      <h2>Stage and validate this frozen collection</h2>
      <p>Create and upload store an inactive collection. Validation records the original whole-collection verdict without changing active resources. Activation is a separate capability.</p>
      {!allowed.has('ActivateOperation') && <p>Activation is not available on this server. Staging and validation do not activate resources.</p>}
      {!operation && <button className="btn" disabled={!durable || !!busy || !lifetime.current.collection} onClick={() => { void create(); }}>{lifetime.current.createAttempted ? 'Retry original create' : 'Create inactive operation'}</button>}
      {operation && <>
        <p>Original operation: <Link className="mono" to={`/operations/${encodeURIComponent(operation.id)}`}>{operation.id}</Link></p>
        <dl><dt>State</dt><dd>{operation.state === 'unrecognized' ? 'Unsupported or unconfirmed state; mutations are disabled.' : operation.state}</dd><dt>Uploaded</dt><dd>{operation.uploaded ?? 'Unavailable'} / {summary.itemCount}</dd><dt>Durably committed</dt><dd>{operation.committed ?? 'Unavailable'}</dd><dt>Controller applied</dt><dd>{operation.applied ?? 'Unavailable'}</dd><dt>Validation</dt><dd>{validationStatus}</dd></dl>
        <div className="row" style={{ gap: 12, flexWrap: 'wrap' }}>
          <button className="btn" disabled={!durable || !!busy || uncertain || operation.state !== 'uploading' || operation.uploaded === undefined || operation.uploaded >= summary.itemCount || !lifetime.current.collection} onClick={() => { void upload(); }}>Upload remaining resources</button>
          <button className="btn" disabled={!canStep('ValidateOperation') || uncertain || lifetime.current.validationAttempted || operation.state !== 'uploading' || operation.uploaded !== summary.itemCount} onClick={() => { void control('ValidateOperation'); }}>Validate whole collection</button>
          <button className="btn" disabled={!canStep('ActivateOperation') || !activationReady(operation)} onClick={() => setConfirm('activate')}>Review activation</button>
          <button className="btn" disabled={!!busy || !allowed.has('GetOperation')} onClick={() => { void refresh(); }}>Read original progress</button>
          <button className="btn" disabled={!!busy || !allowed.has('GetOperationValidation')} onClick={() => { void refreshValidation(); }}>Read original validation result</button>
          <button className="btn" disabled={!!busy || (!waiting && !canWait)} onClick={() => { if (waiting) stopWaiting(); else { setWaitKind(validationWait ? 'validation' : 'progress'); waitDelay.current = 5000; setWaiting(true); } }}>{waiting ? 'Stop waiting' : 'Wait for progress'}</button>
          <button className="btn" disabled={!canStep('CancelOperation') || !collectionCancelable(operation)} onClick={() => setConfirm('cancel')}>Cancel original operation</button>
        </div>
        {waiting && <p role="status">Waiting on the original {waitKind === 'validation' ? 'validation result' : 'operation'}. Only GET reads occur, at least five seconds apart; the server may request a longer interval. Stopping this wait does not cancel the operation.</p>}
        {validation && <section aria-label="Original validation result"><h3>Original sealed validation result</h3><p>{validation.summary.valid ? 'Passed' : 'Rejected'}; {validation.itemCount} original resources. Result: <span className="mono">{validation.summary.resultID}</span></p>
          {validation.summary.summaryOnly && <p>This result contains a bounded summary only. No per-resource verdict is available.</p>}
          {validation.summary.issue && <p>Summary issue: {validation.summary.issue}</p>}
          {!!validation.items.length && <table><caption>Original validation outcomes</caption><thead><tr><th scope="col">Ordinal</th><th scope="col">Resource</th><th scope="col">Local file</th><th scope="col">Document / item</th><th scope="col">Change</th><th scope="col">Issue</th><th scope="col">Observed version</th></tr></thead><tbody>{validation.items.map(item => <tr key={item.ordinal}><td>{item.ordinal}</td><td>{item.kind}/{item.id}</td><td>{lifetime.current.sourceNames?.[Number(item.source.slice(7)) - 1] ?? 'Local filename unavailable'}</td><td>{item.sourceDocument} / {item.sourceItem}</td><td>{item.change ?? 'Not evaluated'}</td><td>{item.issue ?? 'None'}</td><td>{item.resourceVersion ?? 'Unavailable'}</td></tr>)}</tbody></table>}
          {validation.nextCursor && <button className="btn" disabled={!!busy || !allowed.has('GetOperationValidation')} onClick={() => { void refreshValidation(validation.nextCursor); }}>Next validation page</button>}
        </section>}
        {!!resultPage.length && <><h3>Original operation results</h3><ul>{resultPage.map(item => <li key={item.id}>{item.id}: {item.outcome}; committed {item.committed === undefined ? 'unavailable' : String(item.committed)}, applied {item.applied === undefined ? 'unavailable' : String(item.applied)}</li>)}</ul></>}
        {cursor && <button className="btn" disabled={!!busy || !allowed.has('GetOperation')} onClick={() => { void refresh(cursor); }}>Next result page</button>}
        {(operation.executionResult || lifetime.current.activationAttempted) && <CollectionExecution key={`execution:${operation.id}:${summary.contentDigest}`} id={operation.id} identity={summary} observation={operation.executionResult} sourceNames={lifetime.current.sourceNames} />}
        {collectionTerminal(operation) && <p>This operation is terminal. The private file worker and admission ticket have been closed. Committed changes are not undone.</p>}
      </>}
    </section>}
    {confirm && <div className="management-modal" role="alertdialog" aria-modal="true" aria-labelledby="collection-confirm-title" onKeyDown={event => {
      if (event.key === 'Escape') setConfirm(undefined);
      if (event.key === 'Tab') { const buttons = event.currentTarget.querySelectorAll('button'); if (event.shiftKey && document.activeElement === buttons[0]) { event.preventDefault(); buttons[buttons.length - 1].focus(); } else if (!event.shiftKey && document.activeElement === buttons[buttons.length - 1]) { event.preventDefault(); buttons[0].focus(); } }
    }}><div className="card"><h2 id="collection-confirm-title">{confirm === 'activate' ? 'Activate this collection?' : 'Cancel this original operation?'}</h2>
      {confirm === 'activate' && <p>Apply the {summary?.itemCount} resources reviewed for operation <span className="mono">{operation?.id}</span>, using sealed validation result <span className="mono">{validation?.summary.resultID}</span>.</p>}
      <p>{confirm === 'activate' ? 'This changes active configuration and can affect monitoring, recovery and notification delivery. Each resource is conditional; conflicts or dependency failures can produce partial results.' : 'Cancellation is a separate server request. It does not roll back changes already committed or reverse external actions.'}</p><button ref={confirmKeep} className="btn" onClick={() => setConfirm(undefined)}>Keep reviewing</button>{' '}<button className="btn" disabled={!!busy || (confirm === 'activate' && (!canStep('ActivateOperation') || !activationReady(operation)))} onClick={() => { void control(confirm === 'activate' ? 'ActivateOperation' : 'CancelOperation'); }}>{confirm === 'activate' ? 'Confirm activation' : 'Confirm cancellation'}</button></div></div>}
  </main>;
}

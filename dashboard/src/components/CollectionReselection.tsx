import { useEffect, useRef, useState } from 'react';
import { useQuery } from '@tanstack/react-query';
import { useDashboardSession } from '../auth/SessionBoundary';
import { collectionPermissions } from '../api/collectionOperations';
import { createReselection, discardReselection, getReselection, reselectionOperations, resumeReselection, uploadReselectionSource, verifyReselection, type ReselectionStatus } from '../api/collectionReselection';
import { ManagementError, type APIResponse } from '../api/session';
import { collectionFileSources, type SelectedCollectionSource } from '../import/files';
import { DirtyDraftGuard } from './DirtyDraftGuard';

type Lifetime = { active: boolean; generation: number; busy: boolean; uncertain: boolean; createAttempted: boolean; files?: SelectedCollectionSource[]; attempt?: ReselectionStatus; abort?: AbortController };
const failures: Record<string, string> = {
  input_mismatch: 'These files do not match the original input. The original upload was not replaced.',
  authorization_changed: 'Authorization changed. Sign in with the original authorized operator and inspect the operation.',
  operation_changed: 'The original upload changed. Read its current receipt before continuing.',
  expired: 'This temporary verification attempt expired. The original operation was not canceled.',
  quota_exceeded: 'The selected input exceeded the verification storage limit.',
  storage_unavailable: 'Verification storage is unavailable. Inspect the original upload before continuing.',
  verification_failed: 'The original input could not be verified. No replacement operation was created.',
  transfer_failed: 'The remaining upload could not be confirmed. Read the original operation before continuing.',
};

export function CollectionReselection({ id, onProgress }: { id: string; onProgress: () => void }) {
  const session = useDashboardSession();
  const owned = useRef<Lifetime>({ active: true, generation: 0, busy: false, uncertain: false, createAttempted: false });
  const [selected, setSelected] = useState(0);
  const [attempt, setAttempt] = useState<ReselectionStatus>();
  const [busy, setBusy] = useState(false);
  const [uncertain, setUncertain] = useState(false);
  const [message, setMessage] = useState('');
  const [delay, setDelay] = useState(5000);
  const input = useRef<HTMLInputElement>(null);
  const capabilities = useQuery({ queryKey: ['management', 'import-capabilities'], queryFn: ({ signal }) => session.get<unknown>('/api/v2/discovery', { signal }), enabled: session.can('GetCapabilities'), retry: false });
  const allowed = collectionPermissions(session, capabilities.data?.data);
  const permitted = reselectionOperations.every(operation => allowed.has(operation));

  useEffect(() => {
    const state = owned.current;
    state.active = true;
    const close = () => { state.active = false; state.generation++; state.abort?.abort(); state.files = undefined; state.attempt = undefined; if (input.current) input.current.value = ''; };
    const unsubscribe = session.onReset(close);
    return () => { unsubscribe(); close(); };
  }, [session]);

  const observe = (response: APIResponse<ReselectionStatus>) => {
    owned.current.attempt = response.data;
    if (response.data.phase !== 'uploading') { owned.current.files = undefined; setSelected(0); }
    setAttempt(response.data);
    setDelay(Math.min(2_147_483_647, Math.max(5000, response.retryAfterMs || 0)));
    if (response.data.phase === 'failed') setMessage(failures[response.data.errorCode ?? ''] ?? 'This verification attempt failed. Inspect the original operation.');
    if (response.data.phase === 'completed') { setMessage('The original upload is complete. Configuration remains inactive until validation and explicit activation.'); onProgress(); }
    return response.data;
  };
  const run = async (work: (signal: AbortSignal, current: () => boolean) => Promise<void>) => {
    const state = owned.current;
    if (!state.active || state.busy || !permitted) return;
    state.busy = true; const generation = ++state.generation;
    const controller = new AbortController(); state.abort = controller;
    const current = () => state.active && generation === state.generation && !controller.signal.aborted;
    setBusy(true); setMessage('');
    try { await work(controller.signal, current); }
    catch (error) {
      if (!current()) return;
      if (!state.attempt && error instanceof ManagementError && error.reason === 'http' && error.status === 429) {
        state.createAttempted = false; state.uncertain = false; setUncertain(false);
        setMessage('Verification capacity is busy. No automatic retry was made. Try again after the server has capacity.');
        return;
      }
      state.uncertain = true; setUncertain(true);
      setMessage(error instanceof ManagementError && error.reason === 'unconfirmed'
        ? 'The response was lost or could not confirm progress. No request was retried. Read attempt progress before continuing.'
        : 'The request did not complete. Read attempt progress before continuing; no new operation was created.');
    } finally {
      if (state.active && generation === state.generation) { state.busy = false; state.abort = undefined; setBusy(false); }
    }
  };
  const read = () => run(async (signal, current) => {
    const saved = owned.current.attempt;
    if (!saved) return;
    try {
      const response = await getReselection(session, allowed, id, saved.id, signal);
      if (!current()) return;
      observe(response); owned.current.uncertain = false; setUncertain(false);
    } catch (error) {
      if (!current()) return;
      if (!(error instanceof ManagementError) || error.reason !== 'http' || ![404, 410].includes(error.status ?? 0)) throw error;
      const state = owned.current;
      state.attempt = undefined; state.files = undefined; state.createAttempted = false; state.uncertain = false;
      setAttempt(undefined); setSelected(0); setUncertain(false);
      setMessage('This temporary attempt is no longer available. The original receipt is being refreshed. Select the original files again if the upload is still eligible.');
      onProgress();
    }
  });
  useEffect(() => {
    if (busy || uncertain || !attempt || !['verifying', 'transferring'].includes(attempt.phase)) return;
    const timer = setTimeout(() => { void read(); }, delay);
    return () => clearTimeout(timer);
    // Each observation schedules one read; no mutation runs on a timer.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [attempt, busy, uncertain, delay]);

  const verify = () => run(async (signal, current) => {
    const state = owned.current;
    if (state.uncertain || !state.files) return;
    const files = state.files;
    let progress = state.attempt;
    if (!progress) {
      if (state.createAttempted) return;
      state.createAttempted = true;
      const response = await createReselection(session, allowed, id, files.length, signal);
      if (!current()) return;
      progress = observe(response);
    }
    while (progress.phase === 'uploading' && progress.nextSource > 0) {
      const source = files[progress.nextSource - 1];
      if (!source || progress.nextOffset > source.file.size) throw new ManagementError('invalid', 'Source progress is unavailable.');
      const end = Math.min(source.file.size, progress.nextOffset + (1 << 20));
      const bytes = new Uint8Array(await source.file.slice(progress.nextOffset, end).arrayBuffer());
      try {
        if (!current()) return;
        const response = await uploadReselectionSource(session, allowed, id, progress.id, { source: progress.nextSource, offset: progress.nextOffset, end: end === source.file.size, data: bytes }, signal);
        if (!current()) return;
        progress = observe(response);
      } finally { bytes.fill(0); }
    }
    if (progress.phase !== 'uploading' || progress.sourcesCompleted !== files.length) return;
    const response = await verifyReselection(session, allowed, id, progress.id, signal);
    if (current()) { state.files = undefined; setSelected(0); observe(response); }
  });
  const resume = () => run(async (signal, current) => {
    const state = owned.current;
    if (state.uncertain || state.attempt?.phase !== 'verified') return;
    const response = await resumeReselection(session, allowed, id, state.attempt.id, signal);
    if (current()) observe(response);
  });
  const discard = () => run(async (signal, current) => {
    const state = owned.current;
    if (!state.attempt) return;
    await discardReselection(session, allowed, id, state.attempt.id, signal);
    if (!current()) return;
    state.attempt = undefined; state.files = undefined; state.uncertain = false; state.createAttempted = false;
    setAttempt(undefined); setSelected(0); setUncertain(false); setMessage('Temporary verification discarded. The original operation remains available.');
  });

  return <section className="card" aria-label="Resume original collection upload">
    <DirtyDraftGuard dirty={selected > 0 || busy} />
    <h2>Resume original upload</h2>
    <p>Select the same YAML or JSON files used for this operation. Verification sends their complete contents, including comments, to this CPRa server. File names stay in this tab.</p>
    <p>Files are ordered by their local names as in the original import. Changed contents require a new operation. Verification and upload do not activate configuration.</p>
    {!permitted ? <p role="status">This server or your identity does not allow original-file verification.</p> : <>
      {!attempt && !owned.current.createAttempted && <label className="management-field"><span>Original collection files</span><input ref={input} type="file" accept=".yaml,.yml,.json" multiple disabled={busy} onChange={event => {
        try {
          const files = collectionFileSources(Array.from(event.target.files ?? []));
          owned.current.files = files; setSelected(files.length); setMessage('');
        } catch { owned.current.files = undefined; setSelected(0); setMessage('Select up to 1,000 unique YAML or JSON files totaling at most 64 MiB.'); }
        finally { event.target.value = ''; }
      }} /></label>}
      {selected > 0 && <p>{selected} files selected. Their contents are held only for this verification attempt.</p>}
      <div className="row" style={{ gap: 12, flexWrap: 'wrap' }}>
        {selected > 0 && (!attempt || attempt.phase === 'uploading') && <button className="btn" disabled={busy || uncertain} onClick={() => void verify()}>{attempt ? 'Continue file verification' : 'Verify original files'}</button>}
        {attempt?.phase === 'verified' && <button className="btn primary" disabled={busy || uncertain} onClick={() => void resume()}>Resume original upload</button>}
        {attempt && <button className="btn" disabled={busy} onClick={() => void read()}>Read attempt progress</button>}
        {attempt && !['verifying', 'transferring', 'completed'].includes(attempt.phase) && <button className="btn" disabled={busy} onClick={() => void discard()}>Discard verification attempt</button>}
      </div>
      {busy && <p role="status">Processing the original collection…</p>}
      {attempt && <p role="status">Verification: {attempt.phase}. Sources received: {attempt.sourcesCompleted} of {attempt.sourceCount}. Original resources uploaded: {attempt.operationUploaded}.</p>}
      {uncertain && !attempt && <p>The temporary attempt identity was not received. Inspect the original operation; this page will not automatically create another attempt. An existing temporary attempt expires within 15 minutes.</p>}
    </>}
    {message && <p role={uncertain || attempt?.phase === 'failed' ? 'alert' : 'status'}>{message}</p>}
    <p className="muted">Refreshing or leaving forgets selected files and stops waiting. It does not cancel the original operation or undo a committed upload.</p>
  </section>;
}

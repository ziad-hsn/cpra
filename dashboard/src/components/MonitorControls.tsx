import { useEffect, useId, useRef, useState } from 'react';
import { useQuery, useQueryClient } from '@tanstack/react-query';
import { useDashboardSession } from '../auth/SessionBoundary';
import { useManagementMutation } from '../hooks/mutations';
import { getResource, mutationReceipt } from '../api/resources';
import { ManagementError } from '../api/session';
import { object } from '../api/value';
import type { Incident, Monitor } from '../api/generated';
import { MutationReceipt, OperationLink } from './OperationReceipt';
import { formatDateTime } from '../lib/format';

type Action = 'acknowledge' | 'dismiss' | 'reopen' | 'snooze' | 'unsnooze' | 'disable' | 'enable' | 'recover';
const actions = {
  acknowledge: { title: 'Acknowledge', permission: 'AcknowledgeIncident', description: 'Record that you are checking this incident. Checks, notifications and recovery continue.' },
  dismiss: { title: 'Dismiss', permission: 'DismissIncident', description: 'Stop future notifications for this incident. Checks and recovery continue. Unsent deliveries for this incident are cancelled.' },
  reopen: { title: 'Reopen notifications', permission: 'ReopenIncident', description: 'Allow future notifications for this active incident. Previously cancelled or completed deliveries are not repeated.' },
  snooze: { title: 'Snooze', permission: 'SnoozeMonitor', description: 'Pause checks, notifications and new recovery for a period. Work that already started keeps its recorded outcome.' },
  unsnooze: { title: 'End snooze', permission: 'UnsnoozeMonitor', description: 'End this pause early. A disabled monitor remains disabled.' },
  disable: { title: 'Disable', permission: 'PatchMonitor', description: 'Stop new checks, notifications and recovery until this monitor is enabled again. State and history remain available.' },
  enable: { title: 'Enable', permission: 'PatchMonitor', description: 'Enable this monitor. An active snooze or maintenance restriction still applies.' },
  recover: { title: 'Request recovery', permission: 'RecoverMonitor', description: 'Request the configured recovery for the current unhealthy target. CPRa checks maintenance, active or unresolved work, cooldowns and attempt limits before admitting it. This can change the monitored service.' },
} as const;

interface FrozenControl { action: Action; monitorID: string; monitorUID: string; revision: string; incidentID?: string }

function validSnoozeDuration(input: string): boolean {
  const duration = input.trim().replace(/^\+/, '');
  const parts = [...duration.matchAll(/(?:\d+(?:\.\d*)?|\.\d+)(ns|us|µs|μs|ms|s|m|h)/g)];
  if (!parts.length || parts.map(part => part[0]).join('') !== duration) return false;
  const seconds: Record<string, number> = { ns: 1e-9, us: 1e-6, 'µs': 1e-6, 'μs': 1e-6, ms: 1e-3, s: 1, m: 60, h: 3600 };
  const total = parts.reduce((sum, part) => sum + parseFloat(part[0]) * seconds[part[1]], 0);
  return total >= 1e-9 && total <= 30 * 24 * 3600;
}

function incidentView(value: unknown, expectedID: string, monitorID: string): Incident {
  if (!object(value) || value.id !== expectedID || value.monitorID !== monitorID || typeof value.revision !== 'string' || !value.revision || typeof value.state !== 'string') {
    throw new ManagementError('invalid', 'The incident response could not be matched to this monitor.');
  }
  return { id: expectedID, monitorID, revision: value.revision, state: value.state,
    dismissed: value.dismissed === true,
    ...(typeof value.acknowledgedBy === 'string' ? { acknowledgedBy: value.acknowledgedBy } : {}),
    ...(typeof value.acknowledgedAt === 'string' ? { acknowledgedAt: value.acknowledgedAt } : {}),
    ...(typeof value.closedAt === 'string' ? { closedAt: value.closedAt } : {}),
  };
}

/** Controls use exact saved identities, never the read-only numeric summary. */
export function MonitorControls({ monitorID }: { monitorID: string }) {
  const session = useDashboardSession();
  const queries = useQueryClient();
  const [draft, setDraft] = useState<FrozenControl>();
  const mutation = useManagementMutation(mutationReceipt);
  const resource = useQuery({ queryKey: ['management', 'monitors', 'detail', monitorID], queryFn: ({ signal }) => getResource(session, 'monitors', monitorID, signal), enabled: session.can('GetMonitor'), refetchInterval: 5000, gcTime: 0 });
  const monitor = resource.data?.resource as Monitor | undefined;
  const incidentID = monitor?.status?.incidentID;
  const incident = useQuery({ queryKey: ['management', 'incident', incidentID], queryFn: async ({ signal }) => {
    const response = await session.get<unknown>(`/api/v2/incidents/${encodeURIComponent(incidentID!)}`, { signal });
    return incidentView(response.data, incidentID!, monitorID);
  }, enabled: !!incidentID && session.can('GetIncident'), refetchInterval: 5000, gcTime: 0 });
  useEffect(() => session.onReset(() => setDraft(undefined)), [session]);

  if (!session.can('GetMonitor')) return null;
  const currentIncident = !incident.isError && incident.data?.state === 'open' ? incident.data : undefined;
  const writesUnavailable = mutation.pending || mutation.error?.reason === 'unconfirmed' || resource.isError;
  const until = monitor?.status?.snoozedUntil;
  const snoozed = typeof until === 'string' && Number.isFinite(Date.parse(until)) && Date.parse(until) > Date.now();
  const begin = (action: Action) => {
    if (!monitor || writesUnavailable || !session.can(actions[action].permission)) return;
    const isIncident = action === 'acknowledge' || action === 'dismiss' || action === 'reopen';
    const revision = isIncident ? currentIncident?.revision : action === 'disable' || action === 'enable' ? monitor.metadata.resourceVersion : monitor.status?.controlRevision;
    if (!revision || !monitor.metadata.uid || (isIncident && !currentIncident)) return;
    mutation.clearFeedback();
    setDraft({ action, monitorID, monitorUID: monitor.metadata.uid, revision, ...(isIncident ? { incidentID: currentIncident!.id } : {}) });
  };
  const submit = async (input: { reason: string; note: string; duration: string }) => {
    if (!draft || mutation.pending || mutation.error?.reason === 'unconfirmed') return;
    try {
      if (draft.action === 'disable' || draft.action === 'enable') {
        await mutation.execute(`/api/v2/monitors/${encodeURIComponent(draft.monitorID)}`, { operation: 'PatchMonitor', method: 'PATCH', resourceVersion: draft.revision, body: { spec: { enabled: draft.action === 'enable' } } });
      } else {
        const path = draft.incidentID ? `/api/v2/incidents/${encodeURIComponent(draft.incidentID)}/${draft.action}` : `/api/v2/monitors/${encodeURIComponent(draft.monitorID)}/${draft.action}`;
        await mutation.execute(path, { operation: actions[draft.action].permission, method: 'POST', resourceVersion: draft.revision,
          body: { revision: draft.revision, ...(draft.incidentID ? { incidentID: draft.incidentID } : {}), ...(input.note.trim() ? { note: input.note.trim() } : {}), ...(input.reason.trim() ? { reason: input.reason.trim() } : {}), ...(draft.action === 'snooze' ? { duration: input.duration.trim() } : {}) } });
      }
      setDraft(undefined);
      await queries.invalidateQueries({ queryKey: ['management'] });
      await queries.invalidateQueries({ queryKey: ['monitor'] });
      await queries.invalidateQueries({ queryKey: ['history', draft.monitorID] });
      await queries.invalidateQueries({ queryKey: ['durable-state', draft.monitorID] });
    } catch { /* Keep the frozen request and explicit error. Never retry or update its version. */ }
  };
  return <section className="card" aria-labelledby={`controls-${monitorID}`}>
    <h2 id={`controls-${monitorID}`} className="card-title">Monitor controls</h2>
    {resource.isPending && <p role="status">Loading saved control state…</p>}
    {resource.isError && <p role="alert">Saved control state is unavailable. Reload it before making a change.</p>}
    {monitor && <>
      <p>{monitor.spec.enabled === false ? 'Disabled' : 'Enabled'}{snoozed ? ` · Snoozed until ${formatDateTime(until!)}` : ''}</p>
      {currentIncident?.acknowledgedBy && <p role="status">Acknowledged by {currentIncident.acknowledgedBy}{currentIncident.acknowledgedAt ? ` at ${formatDateTime(currentIncident.acknowledgedAt)}` : ''}. Monitoring continues.</p>}
      {currentIncident?.dismissed && <p>Notifications dismissed for this incident. Checks and recovery continue.</p>}
      {incidentID && incident.isError && <p role="alert">Incident attention could not be loaded. No incident action will use an older version.</p>}
      <div className="row" style={{ gap: 8, flexWrap: 'wrap' }}>
        {currentIncident && (['acknowledge', currentIncident.dismissed ? 'reopen' : 'dismiss'] as Action[]).map(action => session.can(actions[action].permission) && <button key={action} className="btn" disabled={writesUnavailable} onClick={() => begin(action)}>{actions[action].title}</button>)}
        {(['snooze', ...(snoozed ? ['unsnooze'] : []), monitor.spec.enabled === false ? 'enable' : 'disable'] as Action[]).map(action => session.can(actions[action].permission) && <button key={action} className="btn" disabled={writesUnavailable || !monitor.metadata.uid || ((action === 'snooze' || action === 'unsnooze') && !monitor.status?.controlRevision)} onClick={() => begin(action)}>{actions[action].title}</button>)}
        {session.can('RecoverMonitor') && monitor.spec.recovery && <button className="btn" disabled={writesUnavailable || !monitor.metadata.uid || !monitor.status?.controlRevision || monitor.spec.enabled === false || snoozed || monitor.status?.health !== 'unhealthy'} onClick={() => begin('recover')}>Request recovery</button>}
      </div>
      {!monitor.status?.controlRevision && <p className="muted">Snooze is unavailable until the controller publishes a control version.</p>}
    </>}
    {mutation.error && <p role="alert">{mutation.error.message}{mutation.error.status === 412 ? ' The saved state changed. Close this draft and inspect the latest state before making another change.' : ''}{mutation.error.reason === 'unconfirmed' ? ' Inspect the operation or current state before taking another action.' : ''}</p>}
    {mutation.error?.response?.operationID && <OperationLink id={mutation.error.response?.operationID} label="Inspect submitted operation" />}
    {mutation.response && <MutationReceipt response={mutation.response} />}
    {draft && <ControlDialog key={`${draft.action}/${draft.revision}`} draft={draft} pending={mutation.pending} unconfirmed={mutation.error?.reason === 'unconfirmed'} onCancel={() => setDraft(undefined)} onConfirm={submit} />}
  </section>;
}

function ControlDialog({ draft, pending, unconfirmed, onCancel, onConfirm }: { draft: FrozenControl; pending: boolean; unconfirmed: boolean; onCancel: () => void; onConfirm: (input: { reason: string; note: string; duration: string }) => Promise<void> }) {
  const heading = useId();
  const root = useRef<HTMLDivElement>(null);
  const [reason, setReason] = useState('');
  const [note, setNote] = useState('');
  const [duration, setDuration] = useState('30m');
  const needsReason = draft.action === 'dismiss' || draft.action === 'snooze' || draft.action === 'recover';
  const durationValid = draft.action !== 'snooze' || validSnoozeDuration(duration);
  const textValid = [reason, note].every(text => new TextEncoder().encode(text).byteLength <= 4096 && !text.includes('\0') && !text.includes('\r'));
  useEffect(() => {
    const previous = document.activeElement;
    root.current?.querySelector<HTMLElement>('input,textarea,button')?.focus();
    return () => { if (previous instanceof HTMLElement && previous.isConnected) previous.focus(); };
  }, []);
  return <div ref={root} className="management-modal" role="dialog" aria-modal="true" aria-labelledby={heading} onKeyDown={event => {
    if (event.key === 'Escape' && !pending) onCancel();
    if (event.key !== 'Tab') return;
    const controls = root.current?.querySelectorAll<HTMLElement>('input:not(:disabled),textarea:not(:disabled),button:not(:disabled)');
    if (!controls?.length) { event.preventDefault(); return; }
    if (event.shiftKey && document.activeElement === controls[0]) { event.preventDefault(); controls[controls.length - 1].focus(); }
    else if (!event.shiftKey && document.activeElement === controls[controls.length - 1]) { event.preventDefault(); controls[0].focus(); }
  }}><form className="card" onSubmit={event => { event.preventDefault(); if (textValid && durationValid && (!needsReason || reason.trim())) void onConfirm({ reason, note, duration }); }}>
    <h2 id={heading}>{actions[draft.action].title}</h2><p>{actions[draft.action].description}</p>
    {draft.incidentID && <p className="muted">Applies to this incident only: <span className="mono">{draft.incidentID}</span></p>}
    {draft.action === 'acknowledge' && <label className="management-field">Note (optional)<textarea className="input" value={note} maxLength={4096} disabled={pending} onChange={event => setNote(event.target.value)} /></label>}
    {needsReason && <label className="management-field">Reason<textarea className="input" required value={reason} maxLength={4096} disabled={pending} onChange={event => setReason(event.target.value)} /></label>}
    {draft.action === 'snooze' && <label className="management-field">Duration<input className="input" aria-describedby={`${heading}-duration`} aria-invalid={!durationValid} required value={duration} disabled={pending} onChange={event => setDuration(event.target.value)} placeholder="30m" /><span id={`${heading}-duration`} className="muted">For example, 15m, 2h or 24h. Use a positive duration of at most 30 days (720h).</span></label>}
    {!textValid && <p role="alert">Notes and reasons must each fit within 4,096 UTF-8 bytes and contain no null or carriage-return characters.</p>}
    <div className="row" style={{ gap: 8 }}><button type="button" className="btn" disabled={pending} onClick={onCancel}>Cancel</button><button type="submit" className="btn" disabled={pending || unconfirmed || !textValid || !durationValid || (needsReason && !reason.trim())}>{pending ? 'Submitting…' : actions[draft.action].title}</button></div>
  </form></div>;
}

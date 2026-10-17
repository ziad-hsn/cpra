import { useEffect, useRef, useState } from 'react';
import { useDashboardSession } from '../auth/SessionBoundary';
import { getResource, object } from '../api/resources';
import { ManagementError } from '../api/session';
import type { Monitor } from '../api/generated';
import { ReferencePicker } from './ResourceForms';
import { ProblemDetails } from './ProblemDetails';

/** Compose a small collection; the shared import path owns validation and apply. */
export function notificationSetupDocuments(monitor: Monitor, name: string, identity: string,
  destination: { endpoint: string } | { credential: string } | { url: string }) {
  if (!monitor.metadata.uid || !monitor.metadata.resourceVersion) throw new Error('A current monitor version is required.');
  const notifications = monitor.spec.notifications ?? {};
  if (notifications.red || notifications.green) throw new Error('This monitor already has incident or recovery notifications. Edit those rules in its configuration.');
  const document = (kind: string, id: string, spec: object) => ({ apiVersion: 'cpra.io/v2', kind, metadata: { id, name }, spec });
  const resources: object[] = [];
  const secretID = `${identity}-url`, endpointID = 'endpoint' in destination ? destination.endpoint : `${identity}-endpoint`;
  if ('url' in destination) resources.push(document('Credential', secretID, { value: destination.url }));
  if (!('endpoint' in destination)) resources.push(document('NotificationEndpoint', endpointID, {
    type: 'webhook', config: {}, credentialRefs: { url: 'credential' in destination ? destination.credential : secretID },
  }));
  const recipientID = `${identity}-recipient`, groupID = `${identity}-group`;
  resources.push(document('Recipient', recipientID, { endpointRefs: [endpointID] }));
  resources.push(document('NotificationGroup', groupID, { recipientRefs: [recipientID] }));
  // Freeze the exact observed identity and version. Validation rejects a
  // concurrent edit; this flow never fetches a newer version to overwrite it.
  resources.push({ apiVersion: monitor.apiVersion, kind: monitor.kind, metadata: { ...monitor.metadata },
    spec: { ...monitor.spec, notifications: { ...notifications,
      red: { notifyType: 'webhook', groupRef: groupID }, green: { notifyType: 'webhook', groupRef: groupID },
    } } });
  return resources;
}

export function NotificationSetup({ disabled, onPrepare, onDirty, onStart, onFinish }: {
  disabled: boolean; onPrepare: (files: File[]) => Promise<boolean>; onDirty: (dirty: boolean) => void; onStart: () => boolean; onFinish: () => void;
}) {
  const session = useDashboardSession();
  const [monitorID, setMonitorID] = useState<string[]>([]);
  const [name, setName] = useState('');
  const [source, setSource] = useState<'new' | 'credential' | 'endpoint'>('new');
  const [reference, setReference] = useState<string[]>([]);
  const [url, setURL] = useState('');
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState('');
  const [problem, setProblem] = useState<ManagementError>();
  const request = useRef<AbortController>();
  const active = useRef(true);
  const dirty = !!name || !!url || !!monitorID.length || !!reference.length;
  useEffect(() => { onDirty(dirty); }, [dirty, onDirty]);
  useEffect(() => {
    active.current = true;
    const clear = () => { request.current?.abort(); setURL(''); setName(''); setMonitorID([]); setReference([]); setError(''); setProblem(undefined); onDirty(false); };
    const unsubscribe = session.onReset(clear);
    return () => { active.current = false; request.current?.abort(); unsubscribe(); onDirty(false); };
  }, [session, onDirty]);

  async function prepare() {
    if (disabled || busy) return;
    setError(''); setProblem(undefined);
    if (!monitorID[0] || !name.trim()) { setError('Choose a monitor and name the recipient.'); return; }
    if (source !== 'new' && !reference[0]) { setError('Choose an existing destination reference.'); return; }
    if (source === 'new') {
      try {
        const parsed = new URL(url);
        if (parsed.protocol !== 'https:' || !parsed.hostname || parsed.username || parsed.password || url.length > 8192) throw new Error();
      } catch { setError('Enter an HTTPS webhook URL without user information, up to 8,192 characters.'); return; }
    }
    if (!onStart()) { setError("Finish or discard the current file preparation first. Your draft is retained."); return; }
    const controller = new AbortController(); request.current = controller;
    const epoch = session.getSnapshot().epoch;
    const current = () => active.current && !controller.signal.aborted && session.getSnapshot().epoch === epoch;
    setBusy(true);
    try {
      const observed = await getResource(session, 'monitors', monitorID[0], controller.signal);
      if (!current()) return;
      if (observed.unsupported || observed.resource.kind !== 'Monitor') throw new Error('unsupported monitor');
      if (source === 'endpoint') {
        const endpoint = await getResource(session, 'endpoints', reference[0], controller.signal);
        if (!current()) return;
        const spec: unknown = endpoint.resource.spec;
        if (endpoint.unsupported || !object(spec) || spec.type !== 'webhook') {
          setError('Choose a webhook endpoint. Other notification types use the destination editor.'); return;
        }
      }
      const monitor = observed.resource as Monitor;
      if (monitor.spec.notifications?.red || monitor.spec.notifications?.green) {
        setError('This monitor already has incident or recovery notifications. Edit those rules in its configuration.'); return;
      }
      const destination = source === 'new' ? { url } : source === 'credential' ? { credential: reference[0] } : { endpoint: reference[0] };
      const documents = notificationSetupDocuments(monitor, name.trim(), `notify-${crypto.randomUUID()}`, destination);
      const file = new File([JSON.stringify(documents)], 'notification-setup.json', { type: 'application/json' });
      // The shared file path may already own a concurrent preparation. Retain
      // the draft until it confirms adoption; never discard it on a no-op.
      const accepted = await onPrepare([file]);
      if (!current()) return;
      if (!accepted) { setError('Another file preparation is in progress, or this setup could not be parsed. Your draft is retained; finish or discard the other selection before trying again.'); return; }
      setURL(''); setName(''); setMonitorID([]); setReference([]); onDirty(false);
    } catch (caught) {
      if (current()) {
        if (caught instanceof ManagementError) setProblem(caught);
        setError('Notification setup could not be prepared. No apply was submitted.');
      }
    } finally { onFinish(); if (active.current) setBusy(false); }
  }

  return <details className="card">
    <summary>Set up webhook notifications</summary>
    <p>Choose a monitor and a destination. This prepares a recipient, group and incident/recovery rules for review together.</p>
    <fieldset disabled={disabled || busy}>
      <ReferencePicker type="monitors" single label="Monitor" values={monitorID} onChange={setMonitorID} />
      <label className="management-field"><span>Recipient name</span><input className="input" value={name} maxLength={128} onChange={event => setName(event.target.value)} /></label>
      <label className="management-field"><span>Webhook destination</span><select className="input" value={source} onChange={event => { setSource(event.target.value as typeof source); setURL(''); setReference([]); }}>
        <option value="new">New protected webhook URL</option><option value="credential">Existing URL secret</option><option value="endpoint">Existing webhook endpoint</option>
      </select></label>
      {source === 'new' ? <div className="management-field"><label htmlFor="notification-webhook-url">Webhook URL</label><input id="notification-webhook-url" aria-describedby="notification-webhook-help" className="input" type="password" autoComplete="new-password" maxLength={8192} value={url} onChange={event => setURL(event.target.value)} /><span id="notification-webhook-help" className="muted">Saved as an encrypted secret. Its value is excluded from previews and operation results.</span></div>
        : <ReferencePicker key={source} type={source === 'credential' ? 'credentials' : 'endpoints'} single label={source === 'credential' ? 'URL secret' : 'Webhook endpoint'} values={reference} onChange={setReference} />}
      <p>Red reports an incident. Green follows verified recovery after an earlier alert attempt, or the configured Ignore recovery exception. Existing red or green rules are not replaced by this setup.</p>
      <button className="btn" type="button" disabled={!session.can('GetMonitor')} onClick={() => { void prepare(); }}>{busy ? 'Preparing…' : 'Prepare notification setup'}</button>
    </fieldset>
    <p className="muted">Review the complete collection below, validate it, then activate explicitly. No notification is sent during setup.</p>
    {error && <p role="alert">{error}</p>}<ProblemDetails error={problem} />
  </details>;
}

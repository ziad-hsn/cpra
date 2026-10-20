import { useEffect, useRef, useState } from 'react';
import { useDashboardSession } from '../auth/SessionBoundary';
import { getResource, object } from '../api/resources';
import { ManagementError } from '../api/session';
import type { AlertRule, Monitor } from '../api/generated';
import { ReferencePicker } from './ResourceForms';
import { ProblemDetails } from './ProblemDetails';

/** Compose a small collection; the shared import path owns validation and apply. */
export function notificationSetupDocuments(monitor: Monitor, name: string, identity: string,
  destination: { endpoint: string } | { credential: string } | { url: string } | { recipient: string }, replaceRules = false) {
  if (!monitor.metadata.uid || !monitor.metadata.resourceVersion) throw new Error('A current monitor version is required.');
  const notifications = monitor.spec.notifications ?? {};
  if (!replaceRules && (notifications.red || notifications.green)) throw new Error('This monitor already has incident or recovery notifications. Review the conditional change before replacing them.');
  const document = (kind: string, id: string, spec: object) => ({ apiVersion: 'cpra.io/v2', kind, metadata: { id, name }, spec });
  const resources: object[] = [];
  const secretID = `${identity}-url`, endpointID = 'endpoint' in destination ? destination.endpoint : `${identity}-endpoint`;
  if ('url' in destination) resources.push(document('Credential', secretID, { value: destination.url }));
  if (!('endpoint' in destination) && !('recipient' in destination)) resources.push(document('NotificationEndpoint', endpointID, {
    type: 'webhook', config: {}, credentialRefs: { url: 'credential' in destination ? destination.credential : secretID },
  }));
  const recipientID = 'recipient' in destination ? destination.recipient : `${identity}-recipient`, groupID = `${identity}-group`;
  if (!('recipient' in destination)) resources.push(document('Recipient', recipientID, { endpointRefs: [endpointID] }));
  resources.push(document('NotificationGroup', groupID, { recipientRefs: [recipientID] }));
  // Freeze the exact observed identity and version. Validation rejects a
  // concurrent edit; this flow never fetches a newer version to overwrite it.
  resources.push({ apiVersion: monitor.apiVersion, kind: monitor.kind, metadata: { ...monitor.metadata },
    spec: { ...monitor.spec, notifications: { ...notifications,
      red: { notifyType: 'webhook', groupRef: groupID }, green: { notifyType: 'webhook', groupRef: groupID },
    } } });
  return resources;
}

export function NotificationSetup({ disabled, onPrepare, onDirty, onStart, onFinish, initialMonitor = '', initialRecipient = '' }: {
  disabled: boolean; onPrepare: (files: File[]) => Promise<boolean>; onDirty: (dirty: boolean) => void; onStart: () => boolean; onFinish: () => void;
  initialMonitor?: string; initialRecipient?: string;
}) {
  const session = useDashboardSession();
  const [monitorID, setMonitorID] = useState<string[]>(initialMonitor ? [initialMonitor] : []);
  const [name, setName] = useState('');
  const [source, setSource] = useState<'new' | 'credential' | 'endpoint' | 'recipient'>(initialRecipient ? 'recipient' : 'new');
  const [reference, setReference] = useState<string[]>(initialRecipient ? [initialRecipient] : []);
  const [url, setURL] = useState('');
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState('');
  const [problem, setProblem] = useState<ManagementError>();
  const [proposal, setProposal] = useState<{ monitor: Monitor; identity: string }>();
  const errorRef = useRef<HTMLDivElement>(null);
  const reviewRef = useRef<HTMLDivElement>(null);
  const request = useRef<AbortController>();
  const active = useRef(true);
  const dirty = !!name || !!url || !!monitorID.length || !!reference.length;
  useEffect(() => { onDirty(dirty); }, [dirty, onDirty]);
  useEffect(() => { if (error || problem) errorRef.current?.focus(); }, [error, problem]);
  useEffect(() => { if (proposal) reviewRef.current?.focus(); }, [proposal]);
  useEffect(() => {
    active.current = true;
    const clear = () => { request.current?.abort(); setURL(''); setName(''); setMonitorID([]); setReference([]); setError(''); setProblem(undefined); setProposal(undefined); onDirty(false); };
    const unsubscribe = session.onReset(clear);
    return () => { active.current = false; request.current?.abort(); unsubscribe(); onDirty(false); };
  }, [session, onDirty]);

  async function prepare(reviewed = false) {
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
      const observed = proposal && reviewed ? { resource: proposal.monitor, unsupported: undefined } : await getResource(session, 'monitors', monitorID[0], controller.signal);
      if (!current()) return;
      if (observed.unsupported || observed.resource.kind !== 'Monitor') throw new Error('unsupported monitor');
      if (!reviewed && source === 'endpoint') {
        const endpoint = await getResource(session, 'endpoints', reference[0], controller.signal);
        if (!current()) return;
        const spec: unknown = endpoint.resource.spec;
        if (endpoint.unsupported || !object(spec) || spec.type !== 'webhook') {
          setError('Choose a webhook endpoint. Other notification types use the destination editor.'); return;
        }
      }
      const monitor = observed.resource as Monitor;
      if (!reviewed && source === 'recipient') {
        const recipient = await getResource(session, 'recipients', reference[0], controller.signal);
        if (!current()) return;
        if (recipient.unsupported || recipient.resource.kind !== 'Recipient') throw new Error('unsupported recipient');
        const spec: unknown = recipient.resource.spec;
        const endpoints = object(spec) && Array.isArray(spec.endpointRefs) ? spec.endpointRefs : [];
        if (!endpoints.length || endpoints.length > 256 || endpoints.some(id => typeof id !== 'string')) throw new Error('unsupported recipient endpoints');
        let webhook = false;
        for (const endpointID of endpoints) {
          const endpoint = await getResource(session, 'endpoints', endpointID, controller.signal);
          if (!current()) return;
          const endpointSpec: unknown = endpoint.resource.spec;
          if (!endpoint.unsupported && endpoint.resource.kind === 'NotificationEndpoint' && object(endpointSpec) && endpointSpec.type === 'webhook') { webhook = true; break; }
        }
        if (!webhook) {
          setError('This recipient has no webhook endpoint. Choose a recipient with a webhook endpoint, or configure its other notification types in the monitor editor.'); return;
        }
      }
      if (!reviewed && (monitor.spec.notifications?.red || monitor.spec.notifications?.green)) {
        setProposal({ monitor, identity: `notify-${crypto.randomUUID()}` }); return;
      }
      if (reviewed && !proposal) return;
      const destination = source === 'new' ? { url } : source === 'credential' ? { credential: reference[0] } : source === 'recipient' ? { recipient: reference[0] } : { endpoint: reference[0] };
      const documents = notificationSetupDocuments(monitor, name.trim(), proposal?.identity ?? `notify-${crypto.randomUUID()}`, destination, reviewed);
      const file = new File([JSON.stringify(documents)], 'notification-setup.json', { type: 'application/json' });
      // The shared file path may already own a concurrent preparation. Retain
      // the draft until it confirms adoption; never discard it on a no-op.
      const accepted = await onPrepare([file]);
      if (!current()) return;
      if (!accepted) { setError('Another file preparation is in progress, or this setup could not be parsed. Your draft is retained; finish or discard the other selection before trying again.'); return; }
      setURL(''); setName(''); setMonitorID([]); setReference([]); setProposal(undefined); onDirty(false);
    } catch (caught) {
      if (current()) {
        if (caught instanceof ManagementError) setProblem(caught);
        setError('Notification setup could not be prepared. No apply was submitted.');
      }
    } finally { onFinish(); if (active.current) setBusy(false); }
  }

  return <details className="card" open={initialMonitor || initialRecipient ? true : undefined}>
    <summary>Set up webhook notifications</summary>
    <p>Choose a monitor and a destination. This prepares a recipient, group and incident/recovery rules for review together.</p>
    <fieldset disabled={disabled || busy || !!proposal}>
      <ReferencePicker type="monitors" single label="Monitor" values={monitorID} onChange={setMonitorID} />
      <label className="management-field"><span>{source === 'recipient' ? 'Notification group name' : 'Recipient name'}</span><input className="input" value={name} maxLength={128} onChange={event => setName(event.target.value)} /></label>
      <label className="management-field"><span>Webhook destination</span><select className="input" value={source} onChange={event => { setSource(event.target.value as typeof source); setURL(''); setReference([]); }}>
        <option value="new">New protected webhook URL</option><option value="credential">Existing URL secret</option><option value="endpoint">Existing webhook endpoint</option><option value="recipient">Existing recipient with a webhook endpoint</option>
      </select></label>
      {source === 'new' ? <div className="management-field"><label htmlFor="notification-webhook-url">Webhook URL</label><input id="notification-webhook-url" aria-describedby="notification-webhook-help" className="input" type="password" autoComplete="new-password" maxLength={8192} value={url} onChange={event => setURL(event.target.value)} /><span id="notification-webhook-help" className="muted">Saved as an encrypted secret. Its value is excluded from previews and operation results.</span></div>
        : <ReferencePicker key={source} type={source === 'credential' ? 'credentials' : source === 'recipient' ? 'recipients' : 'endpoints'} single label={source === 'credential' ? 'URL secret' : source === 'recipient' ? 'Recipient' : 'Webhook endpoint'} values={reference} onChange={setReference} />}
      <p>Red reports an incident. Green follows verified recovery after an earlier alert attempt, or the configured Ignore recovery exception. Existing rules require review before replacement. Other colors, including blue, retain their own configured destinations.</p>
      <button className="btn" type="button" disabled={!session.can('GetMonitor')} onClick={() => { void prepare(); }}>{busy ? 'Preparing…' : 'Prepare notification setup'}</button>
    </fieldset>
    {proposal && <div className="card" tabIndex={-1} ref={reviewRef} aria-label="Review notification rule changes">
      <h3>Review notification rule changes</h3>
      <p>Monitor <span className="mono">{proposal.monitor.metadata.id}</span>, incarnation <span className="mono">{proposal.monitor.metadata.uid}</span>, version <span className="mono">{proposal.monitor.metadata.resourceVersion}</span>. A concurrent edit will conflict; this setup does not fetch a replacement version.</p>
      <div className="table-scroll" role="region" tabIndex={0} aria-label="Notification rule diff"><table className="data-table"><thead><tr><th>Rule</th><th>Current</th><th>Proposed</th></tr></thead><tbody>
        {(['red', 'green'] as const).map(color => <tr key={color}><th>{color}</th><td>{ruleDescription(proposal.monitor.spec.notifications?.[color])}</td><td>Enabled webhook notifications through new group <span className="mono">{proposal.identity}-group</span>{source === 'recipient' && <> using recipient <span className="mono">{reference[0]}</span></>}. Trigger: health transition.</td></tr>)}
      </tbody></table></div>
      <p>Inline values are hidden. Other notification rules and monitor settings remain unchanged. This step only prepares the reviewed collection; validate and activate it separately.</p>
      <div className="row" style={{gap: 8, flexWrap: 'wrap'}}><button className="btn" disabled={disabled || busy} onClick={() => void prepare(true)}>Prepare reviewed collection</button><button className="btn ghost" disabled={busy} onClick={() => setProposal(undefined)}>Edit setup draft</button></div>
    </div>}
    <p className="muted">Review the complete collection below, validate it, then activate explicitly. No notification is sent during setup.</p>
    {(error || problem) && <div role="alert" tabIndex={-1} ref={errorRef}><p>{error}</p><ProblemDetails error={problem} /></div>}
  </details>;
}

function ruleDescription(rule?: AlertRule) {
  if (!rule) return 'Not configured';
  const parts = [rule.dispatch === false ? 'Disabled' : 'Enabled', rule.notifyType || rule.driver?.type || 'untyped destinations', `trigger: ${rule.trigger || 'state_change'}`];
  if (rule.groupRef) parts.push(`group ${rule.groupRef}`);
  if (rule.recipientRefs?.length) parts.push(`recipients ${rule.recipientRefs.join(', ')}`);
  if (rule.endpointRefs?.length) parts.push(`endpoints ${rule.endpointRefs.join(', ')}`);
  if (rule.driver) parts.push('inline configuration (values hidden)');
  return parts.join(' · ');
}

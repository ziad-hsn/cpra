import { useState } from 'react';
import { useQuery } from '@tanstack/react-query';
import type { AlertRule, Capabilities, Monitor, MonitorSpec } from '../api/generated';
import { fieldsFor, object, protectedHTTPURL, type DriverCategory } from '../api/resources';
import { useDashboardSession } from '../auth/SessionBoundary';
import { CODE_LABELS } from '../theme/tokens';
import { formatDateTime } from '../lib/format';
import { DriverFields, ReferencePicker } from './ResourceForms';
import { FailureDetails } from './FailureDetails';
import { useFieldValidation } from './FieldValidation';

const noop = () => undefined;
const record = (value: unknown): Record<string, unknown> => object(value) ? value : {};
const strings = (value: unknown): string[] => Array.isArray(value) ? value.filter(item => typeof item === 'string') : [];
function setField(source: Record<string, unknown>, key: string, value: unknown): Record<string, unknown> {
  const result = { ...source };
  if (value === undefined) delete result[key]; else Object.defineProperty(result, key, { value, enumerable: true, writable: true, configurable: true });
  return result;
}

function ValueField({ label, value, type = 'string', onChange, readOnly = false, required = false, hint, field }: {
  label: string; value: unknown; type?: 'string' | 'number' | 'boolean' | 'strings'; onChange: (value: unknown) => void; readOnly?: boolean; required?: boolean; hint?: string;
  field?: string;
}) {
  const validation = useFieldValidation();
  if (readOnly) return <div className="management-field"><span>{label}</span><p className="mono">{value === undefined ? 'Use default (not configured)' : type === 'boolean' ? value ? 'Yes' : 'No' : type === 'strings' ? strings(value).join(', ') || 'Empty list' : String(value) || 'Empty value'}</p></div>;
  return <div className="management-field"><label className="management-field"><span>{label}</span>
    {type === 'boolean' ? <select {...validation.attributes(field)} className="input" aria-label={label} disabled={readOnly} value={value === undefined ? 'default' : value ? 'true' : 'false'} onChange={event => onChange(event.target.value === 'default' ? undefined : event.target.value === 'true')}>
      <option value="default">Use default</option><option value="true">Yes</option><option value="false">No</option>
    </select> : type === 'strings' ? <textarea {...validation.attributes(field)} className="input" rows={2} readOnly={readOnly} value={strings(value).join('\n')} placeholder="One value per line" onChange={event => onChange(event.target.value ? event.target.value.split('\n') : [])} />
      : <input {...validation.attributes(field)} className="input" type={type === 'number' ? 'number' : 'text'} min={type === 'number' ? 0 : undefined} step={type === 'number' ? 1 : undefined}
        value={typeof value === 'string' || typeof value === 'number' ? value : ''} required={required} readOnly={readOnly} placeholder={value === undefined ? 'Use default' : undefined}
        onChange={event => onChange(type === 'number' ? event.target.value === '' ? undefined : Number(event.target.value) : event.target.value)} />}
  </label>{validation.message(field)}{hint && <p className="muted">{hint}</p>}
    {!readOnly && !required && value !== undefined && type !== 'boolean' && <button className="btn ghost" type="button" onClick={() => onChange(undefined)}>Use default for {label.toLowerCase()}</button>}
  </div>;
}

export function LabelFields({ labels, onChange, readOnly = false }: { labels?: Record<string, string>; onChange: (value?: Record<string, string>) => void; readOnly?: boolean }) {
  const [key, setKey] = useState('');
  const [value, setValue] = useState('');
  const duplicate = !!labels && Object.prototype.hasOwnProperty.call(labels, key);
  return <fieldset className="management-field"><legend>Labels</legend>
    {Object.entries(labels ?? {}).map(([name, text]) => <div className="row" style={{ gap: 8, flexWrap: 'wrap' }} key={name}>
      <label className="management-field" style={{ flex: 1 }}><span>Label {name}</span><input className="input" readOnly={readOnly} value={text} onChange={event => onChange({ ...labels, [name]: event.target.value })} /></label>
      {!readOnly && <button className="btn ghost" type="button" onClick={() => { const next = { ...labels }; delete next[name]; onChange(next); }}>Remove label {name}</button>}
    </div>)}
    {!Object.keys(labels ?? {}).length && <p className="muted">No labels configured.</p>}
    {!readOnly && <>
      <div className="grid-2"><label className="management-field"><span>New label key</span><input className="input" value={key} onChange={event => setKey(event.target.value)} /></label><label className="management-field"><span>New label value</span><input className="input" value={value} onChange={event => setValue(event.target.value)} /></label></div>
      {duplicate && <p role="status">That label already exists. Edit its value above.</p>}
      <button className="btn" type="button" disabled={!key.trim() || duplicate} onClick={() => { onChange({ ...labels, [key]: value }); setKey(''); setValue(''); }}>Add label</button>
      {labels !== undefined && <button className="btn ghost" type="button" onClick={() => { if (window.confirm('Remove all labels from this draft?')) onChange(undefined); }}>Remove all labels</button>}
    </>}
  </fieldset>;
}

export function MonitorFields({ spec: raw, onChange, readOnly = false }: { spec: Record<string, unknown>; onChange: (spec: Record<string, unknown>) => void; readOnly?: boolean }) {
  const spec = raw as MonitorSpec;
  const check = record(spec.check);
  const recovery = spec.recovery ? record(spec.recovery) : undefined;
  const rules = record(spec.notifications);
  const maintenance = Array.isArray(spec.maintenance) ? spec.maintenance : [];
  const update = (key: string, value: unknown) => onChange(setField(raw, key, value));
  const updateCheck = (key: string, value: unknown) => update('check', setField(check, key, value));
  const updateRecovery = (key: string, value: unknown) => update('recovery', setField(recovery ?? {}, key, value));
  return <>
    <ValueField field="spec.enabled" label="Enabled" type="boolean" value={spec.enabled} onChange={value => update('enabled', value)} readOnly={readOnly} hint="Disabled monitors retain their configuration, observations and history while new checks, notifications and recovery are paused. Other pause rules still apply when enabled." />
    <ValueField field="spec.tags" label="Tags" type="strings" value={spec.tags} onChange={value => update('tags', value)} readOnly={readOnly} />
    <fieldset className="management-field"><legend>Health check</legend>
      <DriverFields key={`check-${record(check.driver).type}`} category="check" field="spec.check.driver" spec={record(check.driver)} onChange={driver => updateCheck('driver', driver)} readOnly={readOnly} />
      <div className="grid-2">
        <ValueField field="spec.check.interval" label="Check interval" value={check.interval} required onChange={value => updateCheck('interval', value)} readOnly={readOnly} hint="Go duration, such as 60s or 1m30s." />
        <ValueField field="spec.check.timeout" label="Check timeout" value={check.timeout} required onChange={value => updateCheck('timeout', value)} readOnly={readOnly} hint="Go duration, such as 5s." />
        {([['unhealthyThreshold', 'Unhealthy threshold'], ['healthyThreshold', 'Healthy threshold'], ['retries', 'Check retries'], ['maxFailures', 'Check maximum failures']] as const).map(([key, label]) => <ValueField key={key} field={`spec.check.${key}`} label={label} type="number" value={check[key]} hint={{ unhealthyThreshold: 'Consecutive failed check results needed to open an incident.', healthyThreshold: 'Consecutive successful check results needed to confirm health recovery.', retries: 'Additional attempts within one check deadline; separate from consecutive-check thresholds.', maxFailures: 'Fallback unhealthy threshold when the explicit unhealthy threshold is zero. Prefer the threshold field above.' }[key]} onChange={value => updateCheck(key, value)} readOnly={readOnly} />)}
      </div>
      <ValueField field="spec.check.groups" label="Check groups" type="strings" value={check.groups} onChange={value => updateCheck('groups', value)} readOnly={readOnly} hint="Existing check grouping is separate from notification groups." />
    </fieldset>
    <fieldset className="management-field"><legend>Recovery</legend>
      {!readOnly && <label className="row" style={{ gap: 8 }}><input type="checkbox" checked={!!recovery} onChange={event => {
        if (!event.target.checked && !window.confirm('Remove recovery configuration from this draft?')) return;
        update('recovery', event.target.checked ? { driver: { type: '', config: {} } } : undefined);
      }} />Configure recovery</label>}
      {recovery ? <><DriverFields key={`recovery-${record(recovery.driver).type}`} category="recovery" field="spec.recovery.driver" spec={record(recovery.driver)} onChange={driver => updateRecovery('driver', driver)} readOnly={readOnly} />
        <div className="grid-2"><ValueField field="spec.recovery.cooldown" label="Recovery cooldown" value={recovery.cooldown} onChange={value => updateRecovery('cooldown', value)} readOnly={readOnly} hint="Go duration, such as 5m." />
          {([['maxAttempts', 'Recovery maximum attempts'], ['retries', 'Recovery retries'], ['maxFailures', 'Recovery maximum failures']] as const).map(([key, label]) => <ValueField key={key} field={`spec.recovery.${key}`} label={label} type="number" value={recovery[key]} hint={key === 'maxAttempts' ? 'Confirmed recovery attempts allowed per incident; defaults to one. An uncertain outcome still blocks conflicting recovery.' : 'This setting cannot authorize repeating an uncertain external effect. Use maximum attempts to bound incident recovery.'} onChange={value => updateRecovery(key, value)} readOnly={readOnly} />)}
        </div><p className="muted">Saving configuration does not immediately invoke recovery. The controller uses incident eligibility and the configured limits.</p></> : <p className="muted">Recovery is not configured.</p>}
    </fieldset>
    <fieldset className="management-field"><legend>Configured notifications</legend>
      <p className="muted">Each notification selects a driver and destinations. Blue reports an uncertain recovery for operator review, and closes after verified health recovery without releasing the action hold.</p>
      {recovery && (!rules.blue || record(rules.blue).dispatch === false) && <p className="warning">Uncertain recovery has no enabled blue notification destination. Its action will remain held for operator review.</p>}
      {Object.entries(rules).map(([color, rule]) => <CodeFields key={color} color={color} rule={record(rule) as AlertRule} readOnly={readOnly}
        onChange={next => update('notifications', setField(rules, color, next))} />)}
      {!Object.keys(rules).length && <p className="muted">No notifications configured.</p>}
      {!readOnly && <label className="management-field"><span>Add notification</span><select className="input" aria-label="Add notification" value="" onChange={event => update('notifications', setField(rules, event.target.value, { notifyType: '', recipientRefs: [] }))}>
        <option value="">Choose a notification color…</option>{Object.entries(CODE_LABELS).map(([color, label]) => <option key={color} value={color} disabled={Object.prototype.hasOwnProperty.call(rules, color)}>{color} — {label.toLowerCase()}</option>)}
      </select></label>}
      {spec.notificationGroupRefs !== undefined && <ReferencePicker type="groups" label="Monitor notification groups" values={strings(spec.notificationGroupRefs)} onChange={value => update('notificationGroupRefs', value)} readOnly={readOnly} />}
    </fieldset>
    <fieldset className="management-field"><legend>Maintenance windows</legend>
      {maintenance.map((window, index) => <fieldset className="management-field" key={index}><legend>Maintenance {index + 1}</legend>
        {(['start', 'end', 'cron', 'duration', 'timezone'] as const).map(key => <ValueField key={key} field={`spec.maintenance[${index}].${key}`} label={`Maintenance ${index + 1} ${key}`} value={window[key]} readOnly={readOnly}
          onChange={value => update('maintenance', maintenance.map((current, i) => i === index ? setField(current, key, value) : current))}
          hint={key === 'start' || key === 'end' ? 'RFC 3339 timestamp with an explicit offset, such as 2026-10-01T02:00:00Z.' : key === 'cron' ? 'Use either start/end or a recurring cron expression with duration and optional timezone.' : key === 'timezone' ? 'Optional IANA timezone, such as UTC or Africa/Cairo. Omission preserves the server default.' : 'Go duration, such as 30m.'} />)}
        {!readOnly && <button className="btn ghost" type="button" onClick={() => { if (globalThis.confirm('Remove this maintenance window from the draft?')) update('maintenance', maintenance.filter((_, i) => i !== index)); }}>Remove maintenance {index + 1}</button>}
      </fieldset>)}
      {!maintenance.length && <p className="muted">No maintenance windows configured.</p>}
      {!readOnly && <div className="row" style={{ gap: 8 }}><button className="btn" type="button" onClick={() => update('maintenance', [...maintenance, { start: '', end: '' }])}>Add scheduled maintenance</button><button className="btn" type="button" onClick={() => update('maintenance', [...maintenance, { cron: '', duration: '30m', timezone: 'UTC' }])}>Add recurring maintenance</button></div>}
    </fieldset>
    <fieldset className="management-field"><legend>Automatic Ignore</legend>
      <p className="muted">Count distinct outages that reach the unhealthy threshold within your rolling window. Ignore keeps checking health while suppressing ordinary notifications and interventions. Verified recovery clears Ignore and resets this count.</p>
      {!readOnly && <label className="row" style={{ gap: 8 }}><input type="checkbox" checked={spec.autoIgnore !== undefined} onChange={event => update('autoIgnore', event.target.checked ? {} : undefined)} />Configure automatic Ignore</label>}
      {spec.autoIgnore !== undefined ? <div className="grid-2">
        <ValueField field="spec.autoIgnore.incidents" label="Outages within window" type="number" required value={spec.autoIgnore.incidents} onChange={value => update('autoIgnore', setField(record(spec.autoIgnore), 'incidents', value))} readOnly={readOnly} hint="Required: 2 to 1,000 distinct outages. Repeated failed checks in one outage count once." />
        <ValueField field="spec.autoIgnore.window" label="Rolling window" required value={spec.autoIgnore.window} onChange={value => update('autoIgnore', setField(record(spec.autoIgnore), 'window', value))} readOnly={readOnly} hint="Required: a positive Go duration up to 720h, for example 1h. No default is selected." />
      </div> : <p className="muted">Automatic Ignore is not configured.</p>}
    </fieldset>
  </>;
}

function CodeFields({ color, rule, onChange, readOnly }: { color: string; rule: AlertRule; onChange: (rule?: AlertRule) => void; readOnly: boolean }) {
  const validation = useFieldValidation();
  const session = useDashboardSession();
  const capabilities = useQuery({ queryKey: ['management', 'capabilities'], queryFn: ({ signal }) => session.get<Capabilities>('/api/v2/discovery', { signal }), enabled: !readOnly });
  const drivers = capabilities.data?.data.drivers.notification ?? [];
  const typed = rule.notifyType !== undefined;
  const update = (key: string, value: unknown) => onChange(setField(rule, key, value));
  return <fieldset className="management-field"><legend>Notification {color}</legend>
    <ValueField field={`spec.notifications.${color}.dispatch`} label={`Notification ${color} dispatch`} type="boolean" value={rule.dispatch} onChange={value => update('dispatch', value)} readOnly={readOnly} hint="The default remains omitted. Selecting No explicitly disables dispatch for this notification without changing observed health." />
    {color === 'red' && <label className="management-field"><span>Notification red trigger</span>{readOnly ? <span>{rule.trigger ?? 'state_change'}</span> : <select {...validation.attributes(`spec.notifications.${color}.trigger`)} className="input" aria-label="Notification red trigger" value={rule.trigger ?? ''} onChange={event => update('trigger', event.target.value || undefined)}>
      <option value="">Health transition (default)</option><option value="state_change">Health transition</option><option value="recovery_failed">After a confirmed failed recovery attempt</option><option value="recovery_exhausted">After all recovery attempts fail</option>
    </select>}{!readOnly && validation.message(`spec.notifications.${color}.trigger`)}<span className="muted">Recovery triggers require a recovery action. Unknown outcomes stay held. A queued, active or unknown notification keeps its original failure context.</span></label>}
    {typed ? <>
      <label className="management-field"><span>Notification {color} driver</span>{readOnly ? <span>{rule.notifyType}</span> : <select {...validation.attributes(`spec.notifications.${color}.notifyType`)} className="input" required disabled={!capabilities.data} value={rule.notifyType} onChange={event => update('notifyType', event.target.value)}>
        <option value="">Choose a compiled notification driver…</option>{rule.notifyType && !drivers.includes(rule.notifyType) && <option value={rule.notifyType}>{rule.notifyType} — unavailable in this server</option>}
        {drivers.filter(value => fieldsFor(value)).sort().map(driver => <option key={driver} value={driver}>{driver}</option>)}
      </select>}</label>
      {!readOnly && validation.message(`spec.notifications.${color}.notifyType`)}
      <ReferencePicker field={`spec.notifications.${color}.recipientRefs`} type="recipients" label={`Notification ${color} recipients`} values={rule.recipientRefs ?? []} onChange={refs => update('recipientRefs', refs)} readOnly={readOnly} />
    </> : <><p className="muted">Direct destinations are preserved. An inline notification driver beside a group retains its original behavior; it is not treated as a recipient type filter.</p>
      {rule.driver && <DriverFields key={`notification-${rule.driver.type}`} category="notification" field={`spec.notifications.${color}.driver`} spec={rule.driver} onChange={driver => update('driver', driver)} readOnly={readOnly} />}
      {rule.endpointRefs !== undefined && <ReferencePicker field={`spec.notifications.${color}.endpointRefs`} type="endpoints" label={`Notification ${color} endpoints`} values={rule.endpointRefs} onChange={refs => update('endpointRefs', refs)} readOnly={readOnly} />}
      {!readOnly && <button className="btn" type="button" onClick={() => { if (window.confirm('Replace these direct destinations with a notification type and recipient/group selection? Existing inline settings and direct endpoint references will be removed from this draft.')) onChange({ ...(rule.dispatch === undefined ? {} : { dispatch: rule.dispatch }), ...(rule.trigger === undefined ? {} : { trigger: rule.trigger }), ...(rule.groupRef === undefined ? {} : { groupRef: rule.groupRef }), notifyType: '', recipientRefs: [] }); }}>Use typed recipients for Notification {color}</button>}
    </>}
    <ReferencePicker field={`spec.notifications.${color}.groupRef`} type="groups" label={`Notification ${color} group`} single values={rule.groupRef === undefined ? [] : [rule.groupRef]} onChange={refs => update('groupRef', refs.length ? refs[0] : undefined)} readOnly={readOnly} />
    {!readOnly && <button className="btn ghost" type="button" onClick={() => { if (window.confirm(`Remove notification ${color} from this draft?`)) onChange(undefined); }}>Remove notification {color}</button>}
  </fieldset>;
}

const duration = /^\+?(?:\d+(?:\.\d*)?|\.\d+)(?:ns|us|µs|μs|ms|s|m|h)(?:(?:\d+(?:\.\d*)?|\.\d+)(?:ns|us|µs|μs|ms|s|m|h))*$/;
function positiveDuration(value: unknown): boolean { return typeof value === 'string' && duration.test(value) && /[1-9]/.test(value); }

/** Local guidance complements the authoritative server validation; no provider I/O. */
export function validateMonitorDraft(spec: Record<string, unknown>, capabilities?: Capabilities): string | undefined {
	if (spec.autoIgnore !== undefined) {
		const policy = record(spec.autoIgnore);
		if (!Number.isInteger(policy.incidents) || Number(policy.incidents) < 2 || Number(policy.incidents) > 1000) return 'Automatic Ignore needs an explicit outage count from 2 to 1,000.';
		if (!positiveDuration(policy.window)) return 'Automatic Ignore needs an explicit positive rolling window, at most 720h.';
		const units: Record<string, number> = { ns: 1e-9, us: 1e-6, 'µs': 1e-6, 'μs': 1e-6, ms: 1e-3, s: 1, m: 60, h: 3600 };
		const seconds = [...String(policy.window).matchAll(/(\d+(?:\.\d*)?|\.\d+)(ns|us|µs|μs|ms|s|m|h)/g)].reduce((sum, match) => sum + Number(match[1]) * units[match[2]], 0);
		if (seconds > 720 * 3600) return 'Automatic Ignore window must not exceed 720h.';
	}
  const check = record(spec.check);
  if (!positiveDuration(check.interval) || !positiveDuration(check.timeout)) return 'Check interval and timeout must be positive Go duration strings, such as 60s and 5s.';
  const validateDriver = (category: DriverCategory, source: unknown): string | undefined => {
    const driver = record(source);
    if (typeof driver.type !== 'string' || !fieldsFor(driver.type, category) || !capabilities?.drivers[category]?.includes(driver.type)) return `Select a ${category} driver compiled into this server.`;
    if (category === 'check' && driver.type === 'http' && record(driver.config).url !== undefined && protectedHTTPURL(record(driver.config).url)) return 'HTTP URLs containing credentials, a query or a fragment require a URL secret reference.';
    if (Object.values(record(driver.credentialRefs)).some(value => typeof value !== 'string' || !value)) return 'Select a valid secret reference for each configured driver slot.';
    return undefined;
  };
  const checkError = validateDriver('check', check.driver);
  if (checkError) return checkError;
  if (spec.recovery) { const failure = validateDriver('recovery', record(spec.recovery).driver); if (failure) return failure; }
  for (const [color, value] of Object.entries(record(spec.notifications))) {
    const rule = record(value);
    if (rule.trigger !== undefined && !['state_change', 'recovery_failed', 'recovery_exhausted'].includes(String(rule.trigger))) return `Notification ${color} has an unsupported notification trigger.`;
    if (rule.trigger !== undefined && rule.trigger !== 'state_change' && (color !== 'red' || !spec.recovery)) return 'Recovery notification triggers require Notification red and a configured recovery action.';
    if (rule.notifyType !== undefined) {
      if (typeof rule.notifyType !== 'string' || !capabilities?.drivers.notification?.includes(rule.notifyType) || !fieldsFor(rule.notifyType)) return `Notification ${color} needs a notification type compiled into this server.`;
      if (!strings(rule.recipientRefs).length && !(typeof rule.groupRef === 'string' && rule.groupRef)) return `Notification ${color} needs at least one recipient or a group.`;
      if (rule.driver !== undefined || rule.endpointRefs !== undefined) return `Notification ${color} cannot combine typed recipients with inline settings or direct endpoint references.`;
    } else if (rule.driver) { const failure = validateDriver('notification', rule.driver); if (failure) return failure; }
  }
  for (const [index, value] of (Array.isArray(spec.maintenance) ? spec.maintenance : []).entries()) {
    const window = record(value);
    if (window.cron !== undefined) {
      if (typeof window.cron !== 'string' || !window.cron.trim() || !positiveDuration(window.duration)) return `Maintenance ${index + 1} needs a cron expression and positive duration.`;
      if (window.start !== undefined || window.end !== undefined) return `Maintenance ${index + 1} must use either start/end or a recurring schedule.`;
    } else {
      const timestamp = (time: unknown) => typeof time === 'string' && /^\d{4}-\d{2}-\d{2}T.+(?:Z|[+-]\d{2}:\d{2})$/.test(time) && Number.isFinite(Date.parse(time));
      if (!timestamp(window.start) || !timestamp(window.end) || Date.parse(window.end as string) <= Date.parse(window.start as string)) return `Maintenance ${index + 1} needs valid start and end timestamps with an end after its start.`;
      if (window.duration !== undefined || window.timezone !== undefined) return `Maintenance ${index + 1} uses absolute timestamps and cannot also configure a duration or timezone.`;
    }
  }
  return undefined;
}

export function MonitorStatusDetails({ monitor }: { monitor: Monitor }) {
  const status = monitor.status;
  const latency = status?.lastCheckLatencyMs;
  const lastChecked = formatDateTime(status?.lastCheckedAt ?? '');
  const snoozedUntil = formatDateTime(status?.snoozedUntil ?? '');
  return <section aria-label="Monitor application and observations"><h3>Application and observations</h3>
    <p>Configured generation: {monitor.metadata.generation ?? 'Unavailable'} · Observed generation: {status?.observedGeneration ?? 'Unavailable'}</p>
    <p>{monitor.metadata.generation !== undefined && monitor.metadata.generation > 0 && status?.observedGeneration === monitor.metadata.generation ? 'The controller reports this configuration generation applied.' : 'Controller application of this saved configuration has not been confirmed.'}</p>
    <p>Observed health: {status?.health || 'Unavailable'} · Last check: {lastChecked === '—' ? 'Unavailable' : lastChecked}</p>
    {status?.ignored && <p>Ignored by {status.ignoredBy || 'Unavailable'} since {formatDateTime(status.ignoredAt ?? '')}. {status.ignoreReason} Health observations continue.</p>}
    <FailureDetails diagnostic={status?.lastFailure} label="Last check failure" />
	<FailureDetails diagnostic={status?.configurationProblem} label="Check configuration problem" />
	{status?.configurationProblem && <p>Health shows the last target observation. Local preparation will be retried at the check interval; recovery actions are blocked meanwhile.</p>}
    {status?.warnings?.some(warning => warning.reason === 'unknownRecoveryDestination') && <p className="warning">Uncertain recovery has no enabled blue notification destination. Its action will remain held for operator review.</p>}
    <p>Last-check latency: {latency?.available === true && typeof latency.value === 'number' && Number.isFinite(latency.value) && latency.value >= 0 ? `${latency.value} ms` : 'Unavailable'}</p>
    <p>Control revision: {status?.controlRevision || 'Unavailable'} · Execution revision: {status?.executionRevision || 'Unavailable'}</p>
    <p>Snoozed until: {snoozedUntil === '—' ? 'Not reported' : snoozedUntil} · Current incident: {status?.incidentID || 'Not reported'} · Unknown provider outcomes: {status?.unknownActions ?? 'Unavailable'} · Actions needing review: {status?.actionsNeedingReview ?? 'Unavailable'}</p>
    <LabelFields labels={monitor.metadata.labels} onChange={noop} readOnly />
  </section>;
}

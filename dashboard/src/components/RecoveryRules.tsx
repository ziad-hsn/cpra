import { useLayoutEffect, useRef, useState } from 'react';
import { useQuery } from '@tanstack/react-query';
import type { Capabilities, RecoveryDecision, RecoveryAdmission } from '../api/generated';
import { object } from '../api/value';
import { useDashboardSession } from '../auth/SessionBoundary';
import { DriverFields } from './ResourceForms';
import { driverView } from '../api/resources';
import { useFieldValidation } from './FieldValidation';
import { formatDateTime } from '../lib/format';

const record = (value: unknown): Record<string, unknown> => object(value) ? value : {};
const list = (value: unknown): unknown[] => Array.isArray(value) ? value : [];
const optionalPredicates = ['phases', 'httpStatuses', 'grpcCodes', 'grpcStatuses'] as const;
const labels = { codes: 'Failure codes', phases: 'Phases', httpStatuses: 'HTTP statuses', grpcCodes: 'gRPC transport statuses', grpcStatuses: 'gRPC health statuses' };

function ruleSummary(rule: Record<string, unknown>): string {
  const match = record(rule.match);
  const codes = list(match.codes).filter(value => typeof value === 'string').join(' or ') || 'a failure code (required)';
  const predicates = optionalPredicates.filter(key => list(match[key]).length).map(key => `${labels[key].toLowerCase()} ${list(match[key]).join(' or ')}`);
  return `If ${[codes, ...predicates].join(' and ')}, then ${rule.none === true ? 'no intervention' : `select ${String(record(rule.driver).type || 'an intervention (required)')}`}.`;
}

// An editing hint, never validation or an execution decision. Prefixes may
// overlap syntactically even when a pinned vocabulary has no such code. The
// shared Go validator resolves that distinction against the exact contract.
function possibleOverlap(left: Record<string, unknown>, right: Record<string, unknown>): boolean {
  const a = record(left.match), b = record(right.match);
  const valuesOverlap = optionalPredicates.every(key => !list(a[key]).length || !list(b[key]).length || list(a[key]).some(value => list(b[key]).includes(value)));
  const codesOverlap = list(a.codes).some(x => typeof x === 'string' && list(b.codes).some(y => typeof y === 'string' && (x === y || x.endsWith('.*') && y.startsWith(x.slice(0, -1)) || y.endsWith('.*') && x.startsWith(y.slice(0, -1)))));
  return valuesOverlap && codesOverlap;
}

/** This form prepares a conditional resource change. It cannot grant execution. */
export function RecoveryRules({ recovery, checkDriver, onChange, readOnly = false }: { recovery: Record<string, unknown>; checkDriver: string; onChange: (value: Record<string, unknown>) => void; readOnly?: boolean }) {
  const session = useDashboardSession();
  const validation = useFieldValidation();
  const discovery = useQuery({ queryKey: ['management', 'capabilities'], queryFn: ({ signal }) => session.get<Capabilities>('/api/v2/discovery', { signal }), enabled: !readOnly });
  const rules = list(recovery.rules).map(record);
  const [keys, setKeys] = useState(() => rules.map((_, index) => index));
  const nextKey = useRef(rules.length);
  const focusRule = useRef<number | null>(null);
  const inputs = useRef<(HTMLInputElement | null)[]>([]);
  const [announcement, setAnnouncement] = useState('');
  useLayoutEffect(() => {
    if (focusRule.current !== null) {
      inputs.current[focusRule.current]?.focus();
      focusRule.current = null;
    }
  }, [recovery]);
  const definitions = discovery.data?.data.failureTaxonomies?.find(t => t.version === recovery.taxonomyVersion)?.codes.filter(c => c.drivers.includes(checkDriver)) ?? [];
  const changeRule = (index: number, rule: Record<string, unknown>) => onChange({ ...recovery, rules: rules.map((current, i) => i === index ? rule : current) });
  const move = (index: number, to: number) => {
    const next = [...rules], identities = [...keys];
    [next[index], next[to]] = [next[to], next[index]];
    [identities[index], identities[to]] = [identities[to], identities[index]];
    setKeys(identities);
    focusRule.current = to;
    setAnnouncement(`Rule ${index + 1} moved to position ${to + 1}.`);
    onChange({ ...recovery, rules: next });
  };
  return <section aria-label="Error-dependent intervention rules">
    <p>Rules run in order. The first match selects one intervention or explicitly selects none. No match means no intervention. A rule needs fresh consecutive matching checks; starting one intervention does not enable another rule as a fallback.</p>
    <span className="sr-only" role="status">{announcement}</span>
    <label className="management-field"><span>Failure taxonomy version</span><input {...validation.attributes('spec.recovery.taxonomyVersion')} className="input" value={typeof recovery.taxonomyVersion === 'string' ? recovery.taxonomyVersion : ''} readOnly={readOnly} required maxLength={16} onChange={e => onChange({ ...recovery, taxonomyVersion: e.target.value })} />
      <span className="muted">Pinned to this configuration. Changing it requires reviewing and applying the diff; a server upgrade does not expand prefixes.</span></label>{validation.message('spec.recovery.taxonomyVersion')}
    {rules.map((rule, index) => {
      const path = `spec.recovery.rules[${index}]`;
      const match = record(rule.match);
      const relevant = (key: typeof optionalPredicates[number]) => key === 'phases' || checkDriver === 'external' || key === 'httpStatuses' && checkDriver === 'http' || (key === 'grpcCodes' || key === 'grpcStatuses') && checkDriver === 'grpc';
      return <fieldset {...validation.attributes(path)} tabIndex={-1} key={keys[index]} className="management-field"><legend>Rule {index + 1}{typeof rule.id === 'string' && rule.id ? ` · ${rule.id}` : ''}</legend>
        <p className="muted" style={{ overflowWrap: 'anywhere' }}>{ruleSummary(rule)}</p>
        <label className="management-field"><span>Rule {index + 1} ID</span><input ref={element => { inputs.current[index] = element; }} {...validation.attributes(`${path}.id`)} className="input" value={typeof rule.id === 'string' ? rule.id : ''} maxLength={64} required readOnly={readOnly} onChange={e => changeRule(index, { ...rule, id: e.target.value })} /></label>{validation.message(`${path}.id`)}
        <p className="muted">Exact codes or a terminal prefix such as tls.cert.*. Conditions combine with AND; values within a condition combine with OR. Messages and regular expressions are not match inputs.</p>
        {!readOnly && rules.slice(0, index).some(previous => possibleOverlap(previous, rule)) && <p className="warning">Possible overlap with an earlier rule. Review the order before saving. The server validates actual coverage against the pinned failure vocabulary; the first matching rule takes precedence.</p>}
        <div {...validation.attributes(`${path}.match`)} tabIndex={-1} className="grid-2">{(['codes', ...optionalPredicates.filter(key => relevant(key) || match[key] !== undefined)] as const).map(key => <div key={key} className="management-field">
          <label className="management-field"><span>Rule {index + 1} {labels[key].toLowerCase()}</span><PredicateInput field={`${path}.match.${key}`} values={list(match[key])} required={key === 'codes'} readOnly={readOnly} placeholder={key === 'codes' ? 'network.connection_refused' : 'Optional; one value per line'} onChange={values => {
            const next = { ...match };
            if (key !== 'codes' && values.length === 0) delete next[key]; else next[key] = key === 'httpStatuses' ? values.map(value => /^\d+$/.test(value) ? Number(value) : value) : values;
            changeRule(index, { ...rule, match: next });
          }} /></label>{validation.message(`${path}.match.${key}`)}
          {key !== 'codes' && !relevant(key) && <span className="warning">This saved predicate is not normally used with {checkDriver}. It remains in the draft for explicit validation and review.</span>}
        </div>)}</div>
        {validation.message(`${path}.match`)}
        {definitions.length > 0 && <details><summary>Supported {checkDriver} failure codes</summary><ul>{definitions.map(code => <li key={code.code}><code>{code.code}</code> — {code.message}{!code.eligible && ' (cannot authorize intervention)'}</li>)}</ul></details>}
        <label className="management-field"><span>Rule {index + 1} decision</span><select {...validation.attributes(`${path}.none`)} className="input" value={rule.none === true ? 'none' : 'action'} disabled={readOnly} onChange={e => {
          if (e.target.value === 'none') { if (rule.driver && !window.confirm('Remove this rule’s intervention settings from the draft?')) return; const next = { ...rule, none: true }; delete (next as Record<string, unknown>).driver; changeRule(index, next); }
          else { const next = { ...rule, driver: { type: '', config: {} } }; delete (next as Record<string, unknown>).none; changeRule(index, next); }
        }}><option value="none">No intervention</option><option value="action">One intervention</option></select></label>{validation.message(`${path}.none`)}
        {rule.none !== true && <DriverFields key={String(record(rule.driver).type)} category="recovery" field={`${path}.driver`} spec={record(rule.driver)} onChange={driver => changeRule(index, { ...rule, driver })} readOnly={readOnly} />}
        {validation.message(path)}
        {!readOnly && <div className="row" style={{ gap: 8, flexWrap: 'wrap' }}><button type="button" className="btn" disabled={index === 0} onClick={() => move(index, index - 1)}>Move rule {index + 1} up</button><button type="button" className="btn" disabled={index === rules.length - 1} onClick={() => move(index, index + 1)}>Move rule {index + 1} down</button><button type="button" className="btn ghost" onClick={() => { if (window.confirm('Remove this rule from the draft?')) { setKeys(keys.filter((_, i) => i !== index)); onChange({ ...recovery, rules: rules.filter((_, i) => i !== index) }); } }}>Remove rule {index + 1}</button></div>}
      </fieldset>;
    })}
    {!readOnly && <button type="button" className="btn" disabled={rules.length >= 32} onClick={() => { setKeys([...keys, nextKey.current++]); onChange({ ...recovery, rules: [...rules, { id: '', match: { codes: [] }, none: true }] }); }}>Add rule ({rules.length}/32)</button>}
    <p className="muted">The server validates every branch and rejects unreachable rules. Collection previews and saved monitor observations report confirmed partial overlaps. An observed symptom does not establish its root cause.</p>
    {validation.message('spec.recovery.rules')}
  </section>;
}

// Keep the editing buffer while focused, so normalization cannot remove the
// newline the operator has just typed before the next value.
function PredicateInput({ field, values, required, readOnly, placeholder, onChange }: { field: string; values: unknown[]; required: boolean; readOnly: boolean; placeholder: string; onChange: (values: string[]) => void }) {
  const validation = useFieldValidation();
  const [editing, setEditing] = useState(false);
  const [text, setText] = useState('');
  return <textarea {...validation.attributes(field)} className="input" rows={2} required={required} readOnly={readOnly} placeholder={placeholder} value={editing ? text : values.join('\n')} onFocus={() => { setText(values.join('\n')); setEditing(true); }} onBlur={() => setEditing(false)} onChange={e => { const next = e.target.value; setText(next); onChange(next.split('\n').map(value => value.trim()).filter(Boolean)); }} />;
}

export function RecoveryDecisionDetails({ decision }: { decision?: RecoveryDecision }) {
  if (!decision) return null;
  return <section aria-label="Intervention selection"><h4>Intervention selection</h4>
    <p>Candidate: {decision.candidateRuleID || 'No matching rule'} · Qualification: {decision.qualifyingObservations}/{decision.requiredObservations} consecutive checks · {(decision.qualificationReason || decision.reason).replaceAll('_', ' ')}</p>
    <p>Selected for this incident: {decision.selected ? `${decision.selected.ruleID} (${decision.selected.driver})` : 'Not started'}. A different error cannot select another intervention after the first committed start.</p>
    <p className="mono" style={{ overflowWrap: 'anywhere' }}>Decision revision: {decision.decisionRevision || 'Unavailable'}<br />Configuration revision: {decision.configurationRevision || 'Unavailable'}<br />Observation: {decision.observationID || 'Unavailable'} · Taxonomy: {decision.taxonomyVersion}</p>
  </section>;
}

export function RecoveryAdmissionDetails({ admission }: { admission?: RecoveryAdmission }) {
  if (!admission) return null;
  return <section aria-label="Intervention admission"><h4>Intervention admission</h4>
    <p>Admission assessment: {admission.reason === 'eligible' ? 'No observed policy blocker' : admission.reason.replaceAll('_', ' ')}. Attempts used: {admission.attemptsUsed}/{admission.attemptLimit}.</p>
    <p>Execution deadline: {admission.deadline ? formatDateTime(admission.deadline) : 'Not established'}{admission.notBefore ? ` · Cooldown until ${formatDateTime(admission.notBefore)}` : ''}. Assessed at {formatDateTime(admission.observedAt)}.</p>
    <p className="muted">This observation grants no execution permission. Requests recheck controls, authority, configuration, capacity and action fences.</p>
  </section>;
}

/** Only the reviewed public driver projection may enter a rule diff. Stored
 * credentials and unsupported configuration fields are never rendered. */
export function RecoveryRuleChanges({ before, after }: { before?: unknown; after?: unknown }) {
  function policy(value: unknown) {
    const source = record(value);
    const action = (raw: unknown) => { const result = driverView(record(raw), 'recovery'); return result.unsupported ? { type: record(raw).type, details: 'Unsupported or concealed settings; inspect configuration through a compatible client.' } : result.value; };
    return { taxonomyVersion: source.taxonomyVersion, ...(source.driver ? { driver: action(source.driver) } : {}), rules: list(source.rules).map(raw => { const rule = record(raw), match = record(rule.match); return { id: rule.id, match: { codes: match.codes, ...Object.fromEntries(optionalPredicates.filter(key => match[key] !== undefined).map(key => [key, match[key]])) }, ...(rule.none === true ? { none: true } : { driver: action(rule.driver) }) }; }) };
  }
  const oldPolicy = policy(before), newPolicy = policy(after);
  const previous = JSON.stringify(oldPolicy, null, 2), next = JSON.stringify(newPolicy, null, 2);
  if (previous === next) return null;
  const changes: string[] = [];
  if (oldPolicy.taxonomyVersion !== newPolicy.taxonomyVersion) changes.push(`Taxonomy: ${String(oldPolicy.taxonomyVersion || 'None')} → ${String(newPolicy.taxonomyVersion || 'None')}. Review expanded code coverage.`);
  if (JSON.stringify(oldPolicy.driver) !== JSON.stringify(newPolicy.driver)) changes.push('Single-intervention configuration changed.');
  const oldRules = new Map(oldPolicy.rules.map((rule, index) => [rule.id, {rule, index}]));
  const newIDs = new Set(newPolicy.rules.map(rule => rule.id));
  for (const rule of oldPolicy.rules) if (!newIDs.has(rule.id)) changes.push(`Removed rule ${String(rule.id || '(unnamed)')}.`);
  for (const [index, rule] of newPolicy.rules.entries()) {
    const old = oldRules.get(rule.id), id = String(rule.id || '(unnamed)');
    if (!old) { changes.push(`Added rule ${id} at position ${index + 1}. ${ruleSummary(rule)}`); continue; }
    if (old.index !== index) changes.push(`Moved rule ${id}: position ${old.index + 1} → ${index + 1}.`);
    if (JSON.stringify(old.rule.match) !== JSON.stringify(rule.match)) changes.push(`Changed match criteria for rule ${id}. ${ruleSummary(rule)}`);
    if (('none' in old.rule) !== ('none' in rule)) changes.push(`Changed decision for rule ${id}: ${'none' in rule ? 'no intervention' : 'one intervention'}.`);
    if (JSON.stringify('driver' in old.rule ? old.rule.driver : undefined) !== JSON.stringify('driver' in rule ? rule.driver : undefined)) changes.push(`Changed intervention settings or secret references for rule ${id}.`);
  }
  return <section aria-label="Intervention rule changes"><h3>Review intervention rule changes</h3><p>Array order, taxonomy and selected action settings affect authorization. Saving uses the original configuration version; it does not invoke an intervention.</p><ul>{changes.map((change, index) => <li key={index} style={{ overflowWrap: 'anywhere' }}>{change}</li>)}</ul><details><summary>Show concealed configuration details</summary><div className="grid-2">{[['Saved configuration', previous], ['Proposed configuration', next]].map(([label, text]) => <div key={label}><strong>{label}</strong><pre style={{ whiteSpace: 'pre-wrap', overflowWrap: 'anywhere' }}>{text}</pre></div>)}</div></details></section>;
}

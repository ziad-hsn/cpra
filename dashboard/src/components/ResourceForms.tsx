import { useEffect, useState } from 'react';
import { useQuery } from '@tanstack/react-query';
import { useDashboardSession } from '../auth/SessionBoundary';
import { fieldsFor, listResources, object, resourceKinds, type DriverCategory, type ResourceType } from '../api/resources';
import type { Capabilities } from '../api/generated';

export function ReferencePicker({ type, values, onChange, label, single = false, readOnly = false }: {
  type: ResourceType; values: string[]; onChange: (values: string[]) => void; label: string; single?: boolean; readOnly?: boolean;
}) {
  const session = useDashboardSession();
  const [cursors, setCursors] = useState(['']);
  const cursor = cursors[cursors.length - 1];
  const [specificID, setSpecificID] = useState('');
  const allowed = session.can(resourceKinds[type].listOperation);
  const choices = useQuery({ queryKey: ['management', type, 'list', cursor], queryFn: ({ signal }) => listResources(session, type, cursor, signal), enabled: !readOnly && allowed, gcTime: 0 });
  const select = (id: string) => {
    if (!id || values.includes(id)) return;
    onChange(single ? [id] : [...values, id]);
    setSpecificID('');
  };
  return <fieldset className="management-field">
    <legend>{label}</legend>
    {values.length > 0 ? <ul className="reference-list">{values.map(id => <li key={id}>
      <span className="mono">{id}</span>
      {!readOnly && <button type="button" className="btn ghost" onClick={() => onChange(values.filter(value => value !== id))} aria-label={`Remove ${id} from ${label}`}>Remove</button>}
    </li>)}</ul> : <p className="muted">{single ? 'No override configured.' : 'No members selected.'}</p>}
    {!readOnly && <>
      {allowed && <label className="management-field"><span>Choose {label.toLowerCase()}</span>
        <select className="input" value="" onChange={event => select(event.target.value)} disabled={values.length >= 500}>
          <option value="">Select an existing {resourceKinds[type].singular.toLowerCase()}…</option>
          {choices.data?.items.map(({ resource }) => <option key={resource.metadata.id} value={resource.metadata.id} disabled={values.includes(resource.metadata.id)}>
            {resource.metadata.name || resource.metadata.id} ({resource.metadata.id}){type === 'endpoints' && 'type' in resource.spec ? ` · ${resource.spec.type}` : ''}
          </option>)}
        </select>
      </label>}
      {choices.isError && <p role="alert">Could not load available references. A known ID can still be entered; the server validates access and dependencies.</p>}
      {allowed && (cursor || choices.data?.nextCursor) && <div className="row" style={{ gap: 8 }}>
        <button type="button" className="btn ghost" disabled={cursors.length === 1} onClick={() => setCursors(cursors.slice(0, -1))}>Previous choices</button>
        <span>Page {cursors.length}</span>
        <button type="button" className="btn ghost" disabled={!choices.data?.nextCursor} onClick={() => setCursors([...cursors, choices.data!.nextCursor!])}>Next choices</button>
      </div>}
      <div className="row" style={{ gap: 8, flexWrap: 'wrap' }}>
        <label className="management-field" style={{ flex: 1 }}><span>Specific {label.toLowerCase()} ID</span><input className="input" value={specificID} onChange={event => setSpecificID(event.target.value)} /></label>
        <button type="button" className="btn" disabled={!specificID || values.includes(specificID) || values.length >= 500} onClick={() => select(specificID)}>Use ID</button>
      </div>
      <p className="muted">References retain their order. Only this page of choices is loaded; adding the same ID twice is prevented.</p>
    </>}
  </fieldset>;
}

export function DriverFields({ category, spec, onChange, readOnly = false }: { category: DriverCategory; spec: Record<string, unknown>; onChange: (spec: Record<string, unknown>) => void; readOnly?: boolean }) {
  const session = useDashboardSession();
  const capabilities = useQuery({ queryKey: ['management', 'capabilities'], queryFn: ({ signal }) => session.get<Capabilities>('/api/v2/discovery', { signal }), enabled: !readOnly });
  const driver = typeof spec.type === 'string' ? spec.type : '';
  const fields = fieldsFor(driver, category);
  const [secretFields, setSecretFields] = useState<string[]>([]);
  const [urlWarning, setURLWarning] = useState(false);
  const title = category[0].toUpperCase() + category.slice(1);
  const config = object(spec.config) ? spec.config : {};
  const refs = object(spec.credentialRefs) ? spec.credentialRefs : {};
  const drivers = capabilities.data?.data.drivers[category] ?? [];
  const updateConfig = (key: string, value: unknown, remove = false) => {
    if (!remove && category === 'check' && driver === 'http' && key === 'url' && typeof value === 'string' && /[?#@]/.test(value)) { setURLWarning(true); return; }
    setURLWarning(false);
    const next = { ...config };
    if (remove) delete next[key]; else next[key] = value;
    onChange({ ...spec, config: next });
  };
  const changeDriver = (value: string) => {
    if (driver && (Object.keys(config).length || Object.keys(refs).length) && !window.confirm('Changing this driver removes its current settings and secret references. Continue?')) return;
    setSecretFields([]); setURLWarning(false);
    onChange({ type: value, config: {} });
  };
  return <>
    <label className="management-field"><span>{title} driver</span>
      {readOnly ? <span>{driver}</span> : <select className="input" aria-label={`${title} driver`} value={driver} onChange={event => changeDriver(event.target.value)} required disabled={!capabilities.data}>
        <option value="">Choose a compiled driver…</option>
        {driver && !drivers.includes(driver) && <option value={driver}>{driver} — unavailable in this server</option>}
        {drivers.filter(value => fieldsFor(value, category)).sort().map(value => <option key={value} value={value}>{value}</option>)}
      </select>}
    </label>
    {!readOnly && capabilities.isError && <p role="alert">Compiled {category} drivers are unavailable. Restore server access before saving.</p>}
    {!readOnly && driver && capabilities.data && !drivers.includes(driver) && <p role="alert">This driver is not compiled into the current server. The server will reject activation.</p>}
    {fields?.map(field => {
      const useSecret = field.protected || typeof refs[field.key] === 'string' || secretFields.includes(field.key);
      const secretChoice = !field.protected && !readOnly ? <label className="row" style={{ gap: 8 }}><input type="checkbox" checked={useSecret}
        onChange={event => {
          setURLWarning(false);
          if (event.target.checked) {
            setSecretFields([...secretFields, field.key]);
            const next = { ...config }; delete next[field.key]; onChange({ ...spec, config: next });
          } else {
            if (typeof refs[field.key] === 'string' && !window.confirm('Remove this secret reference and use an ordinary field value instead?')) return;
            setSecretFields(secretFields.filter(key => key !== field.key));
            const next = { ...refs }; delete next[field.key]; onChange({ ...spec, credentialRefs: next });
          }
        }} />Use a secret for {field.label.toLowerCase()}</label> : null;
      if (useSecret) return <div key={field.key}>
        {secretChoice}
        <ReferencePicker type="credentials" label={`${field.label} secret`} single readOnly={readOnly}
          values={typeof refs[field.key] === 'string' ? [refs[field.key] as string] : []}
          onChange={values => {
            const next = { ...refs }; const nextConfig = { ...config }; delete nextConfig[field.key];
            if (values.length) next[field.key] = values[0]; else delete next[field.key];
            onChange({ ...spec, config: nextConfig, credentialRefs: next });
          }} />
        {!readOnly && <p className="muted">Create this value in Secrets, then select its ID. {field.type === 'string-map' ? 'The secret value must be a JSON object whose header values are strings.' : field.type === 'boolean' || field.type === 'number' || field.type.endsWith('-list') ? 'The secret contains the JSON value for this field’s type.' : 'The secret contains the field value as plain text.'} The value is never displayed here.</p>}
      </div>;
      const present = Object.prototype.hasOwnProperty.call(config, field.key);
      const value = config[field.key];
      if (readOnly) return <div key={field.key} className="management-field"><span>{field.label}</span><p className="mono">{!present ? 'Use default (not configured)' : field.type === 'boolean' ? value ? 'Yes' : 'No' : Array.isArray(value) ? value.join(', ') || 'Empty list' : String(value) || 'Empty value'}</p></div>;
      return <div key={field.key} className="management-field">
        {secretChoice}
        <label className="management-field"><span>{field.label}</span>
          {field.type === 'boolean' ? <select className="input" aria-label={field.label} value={!present ? 'default' : value ? 'true' : 'false'} disabled={readOnly}
            onChange={event => updateConfig(field.key, event.target.value === 'true', event.target.value === 'default')}>
            <option value="default">Use default</option><option value="true">Yes</option><option value="false">No</option>
          </select> : field.type === 'number-list' ? <NumberListInput value={value} readOnly={readOnly} onChange={next => updateConfig(field.key, next)} /> : field.type === 'string-list' ? <textarea className="input" rows={3} readOnly={readOnly}
            value={Array.isArray(value) ? value.join('\n') : ''} placeholder="One value per line"
            onChange={event => updateConfig(field.key, event.target.value ? event.target.value.split('\n') : [])} />
            : <input className="input" type={field.type === 'number' ? 'number' : 'text'} readOnly={readOnly}
              ref={element => { element?.setCustomValidity(category === 'check' && driver === 'http' && field.key === 'url' && urlWarning ? 'This URL requires a secret reference.' : ''); }}
              aria-invalid={category === 'check' && driver === 'http' && field.key === 'url' && urlWarning || undefined}
              value={typeof value === 'string' || typeof value === 'number' ? value : ''}
              placeholder={present ? undefined : field.type === 'duration' ? 'Use default (for example 30s)' : 'Use default'}
              onChange={event => updateConfig(field.key, field.type === 'number' ? Number(event.target.value) : event.target.value, field.type === 'number' && event.target.value === '')} />}
        </label>
        {!readOnly && present && field.type !== 'boolean' && <button className="btn ghost" type="button" onClick={() => updateConfig(field.key, undefined, true)}>Use default for {field.label.toLowerCase()}</button>}
      </div>;
    })}
    {category === 'check' && driver === 'http' && !readOnly && <p className="muted">HTTP URLs containing credentials, a query or a fragment must use a URL secret reference. Plain target URLs can be entered directly.</p>}
    {urlWarning && <p role="alert">That URL requires a secret reference. Create it in Secrets and select “Use a secret for url”; the protected value was not added to the draft.</p>}
    {!fields && driver && <p>This driver can be observed, but its configuration cannot be edited by this dashboard.</p>}
  </>;
}

export function EndpointFields(props: { spec: Record<string, unknown>; onChange: (spec: Record<string, unknown>) => void; readOnly?: boolean }) {
  return <DriverFields category="notification" {...props} />;
}

function NumberListInput({ value, onChange, readOnly }: { value: unknown; onChange: (value: number[]) => void; readOnly: boolean }) {
  const [text, setText] = useState(Array.isArray(value) ? value.join('\n') : '');
  useEffect(() => { setText(Array.isArray(value) ? value.join('\n') : ''); }, [value]);
  return <textarea className="input" rows={3} readOnly={readOnly} value={readOnly ? Array.isArray(value) ? value.join('\n') : '' : text} placeholder="One integer per line"
    onChange={event => {
      const next = event.target.value; setText(next);
      const values = next ? next.split('\n') : [];
      const valid = values.every(item => /^-?\d+$/.test(item.trim()) && Number.isSafeInteger(Number(item)));
      event.target.setCustomValidity(valid ? '' : 'Enter one whole number per line.');
      if (valid) onChange(values.map(Number));
    }} />;
}

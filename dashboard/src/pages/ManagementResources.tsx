import { useEffect, useRef, useState } from 'react';
import { Link, useNavigate, useParams, useSearchParams } from 'react-router-dom';
import { useQuery, useQueryClient } from '@tanstack/react-query';
import { useAccess, useDashboardSession } from '../auth/SessionBoundary';
import { useManagementMutation } from '../hooks/mutations';
import { getResource, listResources, mutationReceipt, object, operationFor, resourceKinds, type ManagedResource, type ResourceType, type ResourceView } from '../api/resources';
import { ManagementError } from '../api/session';
import { EndpointFields, ReferencePicker } from '../components/ResourceForms';
import { DirtyDraftGuard } from '../components/DirtyDraftGuard';
import { MutationReceipt, OperationLink } from '../components/OperationReceipt';
import { ConfirmDialog } from '../components/ConfirmDialog';
import { LabelFields, MonitorFields, MonitorStatusDetails, validateMonitorDraft } from '../components/MonitorFields';
import type { Capabilities, Metadata, Monitor } from '../api/generated';

interface Draft { id: string; name: string; namePresent: boolean; spec: Record<string, unknown>; baseline?: ManagedResource; secret: string; replaceSecret: boolean; labels?: Record<string, string> }
const references = (value: unknown): string[] => Array.isArray(value) ? value.filter(item => typeof item === 'string') : [];

function credentialPatch(metadata: Metadata, original: Metadata, spec: Record<string, unknown>) {
  const changes: Record<string, unknown> = {};
  if (metadata.name !== original.name) changes.name = metadata.name ?? null;
  const label = (labels: Metadata['labels'], key: string) => labels && Object.hasOwn(labels, key) ? labels[key] : undefined;
  const labels = Object.fromEntries([...new Set([...Object.keys(original.labels ?? {}), ...Object.keys(metadata.labels ?? {})])]
    .filter(key => label(original.labels, key) !== label(metadata.labels, key))
    .map(key => [key, label(metadata.labels, key) ?? null]));
  if (Object.keys(labels).length) changes.labels = labels;
  // Incarnation/version are frozen in the editor; If-Match binds the exact
  // observed object. Immutable fields cannot be merge-patched in the API.
  return { ...(Object.keys(changes).length ? { metadata: changes } : {}), spec };
}

function draftFor(type: ResourceType, resource?: ManagedResource): Draft {
  return { labels: resource?.metadata.labels ? { ...resource.metadata.labels } : undefined, id: resource?.metadata.id ?? '', name: resource?.metadata.name ?? '', namePresent: resource?.metadata.name !== undefined, baseline: resource,
    spec: resource ? structuredClone(resource.spec) as Record<string, unknown> : type === 'monitors' ? { check: { driver: { type: '', config: {} }, interval: '60s', timeout: '5s' } } : type === 'endpoints' ? { type: '', config: {} }
      : type === 'credentials' ? {} : type === 'recipients' ? { endpointRefs: [] } : { endpointRefs: [], recipientRefs: [] },
    secret: '', replaceSecret: !resource };
}

function ReadSpec({ type, spec }: { type: ResourceType; spec: Record<string, unknown> }) {
  if (type === 'monitors') return <MonitorFields spec={spec} onChange={() => undefined} readOnly />;
  if (type === 'credentials') return <p>{typeof spec.description === 'string' ? spec.description : 'No description.'} Secret values are never returned.</p>;
  if (type === 'endpoints') return <EndpointFields spec={spec} onChange={() => undefined} readOnly />;
  return <><ReferencePicker type="endpoints" label="Endpoint references" readOnly values={references(spec.endpointRefs)} onChange={() => undefined} />
    {type === 'groups' && <ReferencePicker type="recipients" label="Recipient references" readOnly values={references(spec.recipientRefs)} onChange={() => undefined} />}</>;
}

export default function ManagementResources({ type }: { type: ResourceType }) {
  const definition = resourceKinds[type];
  const session = useDashboardSession();
  const access = useAccess();
  const [searchParams] = useSearchParams();
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const { id } = useParams<{ id: string }>();
  const creating = !id && searchParams.get('create') === '1';
  const createRoute = `${definition.route}?create=1`;
  const operator = access.access?.role === 'operator';
  const [cursors, setCursors] = useState(['']);
  const [draft, setDraft] = useState<Draft | undefined>();
  const [changed, setChanged] = useState(false);
  const [formError, setFormError] = useState('');
  const [comparison, setComparison] = useState<ResourceView | undefined>();
  const [deleteTarget, setDeleteTarget] = useState<ManagedResource | undefined>();
  const [destination, setDestination] = useState<string>();
  const errorRef = useRef<HTMLDivElement>(null);
  const view = useRef({ active: true });
  const cursor = cursors[cursors.length - 1];
  const mutation = useManagementMutation<unknown>(mutationReceipt);
  const canCreate = operator && session.can(operationFor(type, 'Create'));
  const canEdit = operator && session.can(operationFor(type, type === 'credentials' ? 'Patch' : 'Replace'));
  const canDelete = operator && session.can(operationFor(type, 'Delete'));
  const canRead = id ? session.can(operationFor(type, 'Get')) : session.can(definition.listOperation);
  const list = useQuery({ queryKey: ['management', type, 'list', cursor], queryFn: ({ signal }) => listResources(session, type, cursor, signal), enabled: !id && !creating && canRead, refetchInterval: query => cursor || query.state.data?.nextCursor ? false : 5000, gcTime: 0 });
  const detail = useQuery({ queryKey: ['management', type, 'detail', id], queryFn: ({ signal }) => getResource(session, type, id!, signal), enabled: !!id && !creating && canRead, refetchInterval: 5000, gcTime: 0 });
  const capabilities = useQuery({ queryKey: ['management', 'capabilities'], queryFn: ({ signal }) => session.get<Capabilities>('/api/v2/discovery', { signal }), enabled: (type === 'endpoints' || type === 'monitors') && !!draft });

  useEffect(() => {
    const current = { active: true };
    view.current = current;
    setDraft(creating && canCreate ? draftFor(type) : undefined);
    setChanged(false); setComparison(undefined); setDeleteTarget(undefined); setFormError('');
    return () => { current.active = false; };
  }, [id, creating, canCreate, type]);
  useEffect(() => { if (formError || mutation.error) errorRef.current?.focus(); }, [formError, mutation.error]);
  useEffect(() => {
    // Navigate only after the clean draft state has reached the router blocker.
    // Calling navigate in the async save callback can observe the previous
    // render's dirty flag and trap a successfully saved change behind a modal.
    if (destination && !changed) { setDestination(undefined); navigate(destination); }
  }, [destination, changed, navigate]);

  const update = (next: Partial<Draft>) => { setDraft(current => current ? { ...current, ...next } : current); setChanged(true); };
  const startEdit = (resource: ManagedResource) => { setDraft(draftFor(type, resource)); setComparison(undefined); setChanged(false); setFormError(''); mutation.clearFeedback(); };
  const finishEditor = () => {
    if (changed && !window.confirm('Discard the unsaved draft?')) return;
    setDraft(undefined); setChanged(false); setComparison(undefined); setFormError('');
    if (creating) setDestination(definition.route);
  };

  const save = async () => {
    if (!draft || mutation.pending || mutation.error?.reason === 'unconfirmed') return;
    if (!draft.id.trim()) { setFormError('A stable ID is required.'); return; }
    if (type === 'monitors' && !/^[a-zA-Z0-9][a-zA-Z0-9._:-]{0,127}$/.test(draft.id)) { setFormError('A monitor ID must start with a letter or digit and contain 1–128 letters, digits, dots, underscores, colons or hyphens.'); return; }
    if (type === 'recipients' && !references(draft.spec.endpointRefs).length) { setFormError('Select at least one notification endpoint.'); return; }
    if (type === 'groups' && !references(draft.spec.endpointRefs).length && !references(draft.spec.recipientRefs).length) { setFormError('Select at least one endpoint or recipient.'); return; }
    if (type === 'endpoints' && (typeof draft.spec.type !== 'string' || !capabilities.data?.data.drivers.notification?.includes(draft.spec.type))) { setFormError('Select a notification driver compiled into this server.'); return; }
    if (type === 'monitors') { const error = validateMonitorDraft(draft.spec, capabilities.data?.data); if (error) { setFormError(error); return; } }
    if (type === 'credentials' && draft.replaceSecret && !draft.secret) { setFormError('Supply the new secret value, or keep the existing value.'); return; }
    const existing = draft.baseline;
    const owner = view.current;
    const epoch = session.getSnapshot().epoch;
    const stillCurrent = () => owner.active && owner === view.current && session.getSnapshot().epoch === epoch;
    if (existing && (!existing.metadata.resourceVersion || !existing.metadata.uid)) { setFormError('A current resource version and incarnation are required before editing.'); return; }
    const spec = { ...draft.spec, ...(type === 'credentials' && draft.replaceSecret ? { value: draft.secret } : {}) };
    const metadata = { ...existing?.metadata, id: draft.id, labels: draft.labels, ...(draft.namePresent ? { name: draft.name } : {}) };
    const document = { apiVersion: 'cpra.io/v2', kind: definition.kind, metadata, spec };
    if (new TextEncoder().encode(JSON.stringify(document)).byteLength > 1024 * 1024) { setFormError('This resource exceeds the 1 MiB limit. Shorten its values before submitting.'); return; }
    // Remove the plaintext field before a request can settle, fail, or be cancelled.
    update({ secret: '' });
    setFormError('');
    try {
      if (!existing) await mutation.execute(definition.path, { method: 'POST', operation: operationFor(type, 'Create'), body: document });
      else if (type === 'credentials') await mutation.execute(`${definition.path}/${encodeURIComponent(existing.metadata.id)}`, { method: 'PATCH', operation: operationFor(type, 'Patch'), resourceVersion: existing.metadata.resourceVersion!, body: credentialPatch(metadata, existing.metadata, spec) });
      else await mutation.execute(`${definition.path}/${encodeURIComponent(existing.metadata.id)}`, { method: 'PUT', operation: operationFor(type, 'Replace'), resourceVersion: existing.metadata.resourceVersion!, body: document });
      if (!stillCurrent()) return;
      setChanged(false); setDraft(undefined); setComparison(undefined);
      await queryClient.invalidateQueries({ queryKey: ['management', type] });
      if (stillCurrent()) setDestination(`${definition.route}/${encodeURIComponent(metadata.id)}`);
    } catch { /* The typed mutation state displays a bounded, non-secret failure. */ }
  };

  const remove = async () => {
    const target = deleteTarget;
    if (!target || !target.metadata.resourceVersion || !target.metadata.uid || mutation.pending) return;
    const owner = view.current;
    const epoch = session.getSnapshot().epoch;
    const stillCurrent = () => owner.active && owner === view.current && session.getSnapshot().epoch === epoch;
    try {
      await mutation.execute(`${definition.path}/${encodeURIComponent(target.metadata.id)}`, { method: 'DELETE', operation: operationFor(type, 'Delete'), resourceVersion: target.metadata.resourceVersion });
      if (!stillCurrent()) return;
      setDeleteTarget(undefined); setChanged(false); setDraft(undefined);
      await queryClient.invalidateQueries({ queryKey: ['management', type] });
      if (stillCurrent()) setDestination(definition.route);
    } catch { setDeleteTarget(undefined); }
  };

  const compare = async () => {
    if (!draft?.baseline && !creating) return;
    const owner = view.current;
    const epoch = session.getSnapshot().epoch;
    try {
      const latest = await getResource(session, type, draft?.id || id!);
      if (owner.active && owner === view.current && session.getSnapshot().epoch === epoch) setComparison(latest);
    }
    catch (error) { setFormError(error instanceof ManagementError && error.status === 404 ? 'The original resource no longer exists. Close this editor before choosing another resource.' : 'The latest resource could not be read. Your draft has not changed.'); }
  };
  const value = detail.data?.resource;
  const failure = formError || (mutation.error?.reason === 'unconfirmed' ? 'The outcome is unconfirmed. Do not submit again; inspect the original resource or operation. Your secret input has been cleared.'
    : mutation.error?.status === 409 || mutation.error?.status === 412 ? 'The resource changed or a reference prevents this operation. Your draft has not been overwritten. Compare the latest resource before deciding.'
      : mutation.error ? `The request did not complete${mutation.error.status ? ` (HTTP ${mutation.error.status})` : ''}. Review the highlighted fields and server availability.` : '');

  return <div className="page management-page">
    <DirtyDraftGuard dirty={changed} />
    <div className="page-head"><div><h1>{definition.title}</h1><p className="lead">{definition.description}</p></div>
      {!id && !creating && canCreate && <Link className="btn primary" to={createRoute}>Create {definition.singular.toLowerCase()}</Link>}
      {(id || creating) && <Link className="btn ghost" to={definition.route}>Back to {definition.title.toLowerCase()}</Link>}
    </div>
    {failure && <div className="card" role="alert" tabIndex={-1} ref={errorRef}><p>{failure}</p>
      {mutation.error?.problem?.errors?.length ? <ul>{mutation.error.problem.errors.map((field, index) => <li key={index}>{/^[\w.[\]-]{1,160}$/.test(field.field) ? field.field : 'Resource field'}: validation failed.</li>)}</ul> : null}
      {(draft?.baseline || (creating && mutation.error?.reason === 'unconfirmed')) && <button className="btn" onClick={() => void compare()}>Compare latest resource</button>}
      <OperationLink id={mutation.error?.response?.operationID || mutation.error?.problem?.operationID} label="Reconcile original operation" />
    </div>}
    {mutation.response && !draft && <div className="card" role="status">
      <MutationReceipt response={mutation.response} />
    </div>}
    {!id && !creating ? <>
      {!canRead ? <p role="status">Your identity cannot list these resources.</p> : list.isError ? <p role="alert">Resource list unavailable. <button className="btn" onClick={() => void list.refetch()}>Retry</button></p>
        : !list.data ? <p>Loading resources…</p> : <div className="card"><table className="data-table"><thead><tr><th>Name</th><th>Stable ID</th><th>Version</th></tr></thead>
          <tbody>{list.data.items.map(({ resource }) => <tr key={resource.metadata.id}><td><Link to={`${definition.route}/${encodeURIComponent(resource.metadata.id)}`}>{resource.metadata.name || resource.metadata.id}</Link></td><td className="mono">{resource.metadata.id}</td><td className="mono">{resource.metadata.resourceVersion || 'Unavailable'}</td></tr>)}</tbody>
        </table>{!list.data.items.length && <p>No resources on this page.</p>}</div>}
      {list.data && <div className="pager"><button className="btn" disabled={cursors.length === 1} onClick={() => setCursors(cursors.slice(0, -1))}>Previous page</button><span>Page {cursors.length} · Up to 100 resources</span><button className="btn" disabled={!list.data.nextCursor} onClick={() => setCursors([...cursors, list.data!.nextCursor!])}>Next page</button><button className="btn" onClick={() => { setCursors(['']); void list.refetch(); }}>Refresh resources</button></div>}
      {list.data && (cursor || list.data.nextCursor) && <p className="muted">This paginated view stays fixed while you browse. Refresh resources to start from current observations.</p>}
    </> : !creating && !draft ? <>
      {!canRead ? <p>Your identity cannot read this resource.</p> : detail.isError ? <p role="alert">Resource unavailable. <button className="btn" onClick={() => void detail.refetch()}>Retry</button></p> : !value ? <p>Loading resource…</p> : <div className="card">
        <div className="card-head"><h2>{value.metadata.name || value.metadata.id}</h2><div className="row" style={{ gap: 8 }}>
          {type === 'monitors' && <Link className="btn" to={`/monitors/by-id/${encodeURIComponent(value.metadata.id)}`}>View monitoring and controls</Link>}
          {canEdit && !detail.data?.unsupported && <button className="btn" onClick={() => startEdit(value)}>Edit {definition.singular.toLowerCase()}</button>}
          {canDelete && <button className="btn" disabled={!value.metadata.uid || !value.metadata.resourceVersion} onClick={() => setDeleteTarget(structuredClone(value))}>Delete {definition.singular.toLowerCase()}</button>}
        </div></div>
        <p>Stable ID: <span className="mono">{value.metadata.id}</span> · Version: <span className="mono">{value.metadata.resourceVersion || 'Unavailable'}</span></p>
        <p>Incarnation: <span className="mono">{value.metadata.uid || 'Unavailable'}</span></p>
        {type === 'credentials' && <p>Value availability: {object(value.status) && typeof value.status.available === 'boolean' ? value.status.available ? 'Available' : 'Unavailable' : 'Not reported'}</p>}
        {detail.data?.unsupported && <p role="status">{detail.data.unsupported}</p>}
        {type === 'monitors' && <MonitorStatusDetails monitor={value as Monitor} />}
        <ReadSpec type={type} spec={value.spec as Record<string, unknown>} />
      </div>}
    </> : null}
    {creating && !canCreate && <p>Your identity cannot create these resources.</p>}
    {draft && <form className="card management-form" aria-label={`${creating ? 'Create' : 'Edit'} ${definition.singular.toLowerCase()}`} onSubmit={event => { event.preventDefault(); if (event.currentTarget.reportValidity()) void save(); }}>
      <h2>{draft.baseline ? 'Edit' : 'Create'} {definition.singular.toLowerCase()}</h2>
      <label className="management-field"><span>Stable ID</span><input className="input" required value={draft.id} readOnly={!!draft.baseline} onChange={event => update({ id: event.target.value })} /></label>
      {type === 'monitors' && !draft.baseline && <p className="muted">Start the stable ID with a letter or digit. Use 1–128 letters, digits, dots, underscores, colons or hyphens. Change the display name later to rename a monitor while keeping its identity.</p>}
      <label className="management-field"><span>Name</span><input className="input" value={draft.name} onChange={event => update({ name: event.target.value, namePresent: true })} /></label>
      {draft.baseline && <p className="muted">Editing version <span className="mono">{draft.baseline.metadata.resourceVersion}</span>. Background updates do not replace this draft.</p>}
      <LabelFields labels={draft.labels} onChange={labels => update({ labels })} />
      {type === 'monitors' ? <MonitorFields spec={draft.spec} onChange={spec => update({ spec })} /> : type === 'credentials' ? <>
        <label className="management-field"><span>Description</span><input className="input" value={typeof draft.spec.description === 'string' ? draft.spec.description : ''} onChange={event => update({ spec: { ...draft.spec, description: event.target.value } })} /></label>
        {draft.baseline && <label className="row" style={{ gap: 8 }}><input type="checkbox" checked={draft.replaceSecret} onChange={event => update({ replaceSecret: event.target.checked, secret: '' })} />Replace the secret value</label>}
        {draft.replaceSecret && <label className="management-field"><span>New secret value</span><input className="input" type="password" autoComplete="off" spellCheck={false} value={draft.secret} onChange={event => update({ secret: event.target.value })} /></label>}
        <p className="muted">The value is write-only and cleared when submitted or when this editor closes. Existing values are never filled into this form. Reference this secret by its ID from an endpoint.</p>
      </> : type === 'endpoints' ? <EndpointFields spec={draft.spec} onChange={spec => update({ spec })} /> : <>
        <ReferencePicker type="endpoints" label="Endpoint references" values={references(draft.spec.endpointRefs)} onChange={endpointRefs => update({ spec: { ...draft.spec, endpointRefs } })} />
        {type === 'groups' && <ReferencePicker type="recipients" label="Recipient references" values={references(draft.spec.recipientRefs)} onChange={recipientRefs => update({ spec: { ...draft.spec, recipientRefs } })} />}
      </>}
      {comparison && <section className="card" aria-label="Latest resource comparison"><h3>Latest resource</h3>
        <p>Your draft: {draft.name || draft.id}, version {draft.baseline?.metadata.resourceVersion || 'new'}. Latest: {comparison.resource.metadata.name || comparison.resource.metadata.id}, version {comparison.resource.metadata.resourceVersion}.</p>
        {draft.baseline?.metadata.uid && draft.baseline.metadata.uid !== comparison.resource.metadata.uid && <p role="alert">The original resource was deleted and recreated. This is a different incarnation.</p>}
        <ReadSpec type={type} spec={comparison.resource.spec as Record<string, unknown>} />
        {comparison.unsupported ? <p>{comparison.unsupported}</p> : <button type="button" className="btn" onClick={() => { if (window.confirm('Discard this draft and start a new edit from the displayed latest resource?')) startEdit(comparison.resource); }}>Discard draft and use latest</button>}
      </section>}
      <div className="row" style={{ gap: 12 }}><button className="btn primary" type="submit" disabled={mutation.pending || mutation.error?.reason === 'unconfirmed'}>{mutation.pending ? 'Saving…' : 'Save'}</button><button type="button" className="btn ghost" onClick={finishEditor}>Cancel editing</button></div>
    </form>}
    {deleteTarget && <ConfirmDialog title={`Delete ${deleteTarget.metadata.name || deleteTarget.metadata.id}?`} pending={mutation.pending} onCancel={() => setDeleteTarget(undefined)} onConfirm={() => void remove()}><p>The server blocks deletion while other resources reference this object. Recorded history and unknown actions are preserved.</p></ConfirmDialog>}
  </div>;
}

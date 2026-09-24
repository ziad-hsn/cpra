import { useState } from 'react';
import { useNavigate, useParams } from 'react-router-dom';
import { useDashboardSession } from '../auth/SessionBoundary';
import { useOperation } from '../hooks/operations';
import { operationID, operationMessage, supportedCollectionIdentity } from '../api/operations';
import { ManagementError } from '../api/session';
import { formatNumber } from '../lib/format';
import { CollectionValidation } from '../components/CollectionValidation';
import { OperationsList } from '../components/OperationsList';
import { CollectionExecution } from '../components/CollectionExecution';
import { CollectionReselection } from '../components/CollectionReselection';
import { fileNormalizationProfile } from '../api/collectionInventory';
import { CollectionOperationControls } from '../components/CollectionOperationControls';

const outcomes: Record<string, string> = { reserved: 'Reserved; no resource change committed', activation_rejected: 'Rejected before resource/action commit', reservation_expired: 'Reservation expired; no resource/action commit', committed: 'Committed; awaiting controller', applied: 'Applied by controller', projection_failed: 'Controller application failed', superseded: 'Superseded; application was not confirmed', unrecognized: 'Unrecognized outcome' };
const stateLabels: Record<string, string> = { reserved: 'Reserved', pending: 'Pending', staging: 'Staging', uploading: 'Uploading', validating: 'Validating', validated: 'Validated', rejected: 'Rejected', applying: 'Applying', committed: 'Committed', completed: 'Completed', failed: 'Failed', partial: 'Partial or superseded', interrupted: 'Interrupted', invalidated: 'Invalidated', canceled: 'Canceled', cancelled: 'Canceled', expired: 'Expired' };
const count = (value?: number) => value === undefined ? 'Not reported' : formatNumber(value);

export default function OperationDetail() {
  const { id } = useParams<{ id: string }>();
  const session = useDashboardSession();
  const navigate = useNavigate();
  const [lookup, setLookup] = useState('');
  const allowed = session.can('GetOperation');
  const query = useOperation(id);
  const error = query.error;
  const inaccessible = error instanceof ManagementError && [401, 403, 404, 410].includes(error.status ?? 0);
  const operation = inaccessible ? undefined : query.data?.data;
  const identity = operation && supportedCollectionIdentity(operation);
  const missing = error instanceof ManagementError && (error.status === 404 || error.status === 410);
  const message = error instanceof ManagementError && error.status === 410 ? 'This operation receipt has expired. Its absence does not undo a committed change.'
    : error instanceof ManagementError && error.status === 404 ? 'This operation receipt is unavailable. This alone does not prove that the change was rejected.'
      : error instanceof ManagementError && error.status === 403 ? 'Your identity cannot read this operation.'
        : 'Operation progress is unavailable. Any last received status below may be out of date.';
  return <div className="page management-page">
    <div className="page-head"><div><h1>Operation progress</h1><p className="lead">Follow one original operation to distinguish durable admission from controller application.</p></div></div>
    {!id && session.can('ListOperations') && <OperationsList />}
    {!allowed ? <p role="status">Your identity cannot read operation receipts.</p> : !id ? <form className="card management-form" aria-label="Find operation" onSubmit={event => { event.preventDefault(); if (operationID(lookup)) navigate(`/operations/${encodeURIComponent(lookup)}`); }}>
      <label className="management-field"><span>Operation ID</span><input className="input" required value={lookup} onChange={event => setLookup(event.target.value)} /></label>
      <button className="btn primary" type="submit" disabled={!operationID(lookup)}>Find operation</button>
      <p className="muted">Use an ID from a saved change or an unconfirmed response. Reading its progress never repeats the mutation.</p>
    </form> : !operationID(id) ? <p role="alert">The operation ID in this address is invalid.</p> : <>
      <p>Operation ID: <span className="mono">{id}</span></p>
      {query.isError && <div className="card" role="alert"><p>{message}</p><button className="btn" disabled={query.isFetching} onClick={() => void query.refetch()}>Read receipt again</button>
        {missing && <p>Inspect the original resource and any recorded operation identity before deciding on further changes. The dashboard does not resubmit the original mutation.</p>}
      </div>}
      {!operation && !query.isError && <p>Reading operation receipt…</p>}
      {operation && <section className="card" aria-label="Operation receipt"><h2>{stateLabels[operation.state] ?? 'Unrecognized state'}</h2>
        <p role="status">{operationMessage(operation)}</p>
        <p>Uploaded: {count(operation.uploaded)} · Committed: {count(operation.committed)} · Applied: {count(operation.applied)} · Validation: {operation.validated === undefined ? 'Not reported' : operation.validated ? 'Passed' : 'Rejected'}</p>
        {operation.identityFormat !== undefined && <p>Collection: {count(operation.itemCount)} resources · Identity format: <span className="mono">{operation.identityFormat}</span></p>}
        <p className="muted">Receipt digest: <span className="mono">{operation.contentDigest}</span></p>
        {operation.items?.length ? <div className="table-scroll" role="region" aria-label="Operation item results" tabIndex={0}><table className="data-table"><thead><tr><th>Resource ID</th><th>Outcome</th><th>Committed</th><th>Applied</th><th>Previous version</th><th>New version</th></tr></thead>
          <tbody>{operation.items.map((item, index) => <tr key={`${item.id}:${index}`}><td className="mono">{item.id}</td><td>{outcomes[item.outcome] ?? 'Unrecognized outcome'}</td><td>{item.committed === undefined ? 'Not reported' : item.committed ? 'Yes' : 'No'}</td><td>{item.applied === undefined ? 'Not reported' : item.applied ? 'Yes' : 'No'}</td><td className="mono">{item.oldVersion || 'Not reported'}</td><td className="mono">{item.newVersion || 'Not reported'}</td></tr>)}</tbody>
        </table></div> : <p>{operation.executionResult ? 'Read the per-resource application results below.' : operation.identityFormat !== undefined ? 'Collection validation results are read separately below. This receipt does not report per-resource application outcomes.' : 'Item-level outcomes are not reported.'}</p>}
        {operation.nextCursor && <p role="status">The server indicates additional item results. This receipt view shows only the returned application-outcome page. Retained validation results have separate pagination below.</p>}
        {['committed', 'reserved', 'pending', 'validating', 'applying'].includes(operation.state) && <p className="muted">Progress is reread every five seconds or the longer interval requested by the server. Leaving this page stops waiting; it does not cancel the operation.</p>}
        <p className="muted">These receipts describe durable admission and controller application. They do not establish notification delivery or completed recovery, and rollback cannot reverse external actions.</p>
      </section>}
      {operation?.executionResult && identity && <CollectionExecution key={`execution:${operation.id}:${identity.identityFormat}:${identity.contentDigest}:${identity.itemCount}`} id={operation.id} identity={identity} observation={operation.executionResult} />}
      {operation?.state === 'uploading' && identity?.normalizationProfile === fileNormalizationProfile && operation.uploaded !== undefined && operation.uploaded < identity.itemCount && <CollectionReselection key={`reselection:${operation.id}`} id={operation.id} onProgress={() => { void query.refetch(); }} />}
      {operation && identity && <CollectionOperationControls key={`controls:${operation.id}:${identity.identityFormat}:${identity.contentDigest}:${identity.itemCount}`} id={operation.id} identity={identity} operation={operation} onProgress={() => { void query.refetch(); }} />}
      {operation?.identityFormat !== undefined && (identity
        ? <CollectionValidation key={`${operation.id}:${identity.identityFormat}:${identity.contentDigest}:${identity.itemCount}`} id={operation.id} identity={identity} />
        : <p role="status">This collection uses an identity format that the dashboard cannot use to read validation results. Its receipt remains available as a read-only observation.</p>)}
    </>}
  </div>;
}

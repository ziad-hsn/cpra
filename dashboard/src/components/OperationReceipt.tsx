import { Link } from 'react-router-dom';
import { useDashboardSession } from '../auth/SessionBoundary';
import { useOperation } from '../hooks/operations';
import { operationID, operationMessage, operationView, type OperationObservation } from '../api/operations';
import { object } from '../api/value';
import type { APIResponse } from '../api/session';

export function OperationLink({ id, label = 'View operation progress' }: { id?: string; label?: string }) {
  const session = useDashboardSession();
  if (!operationID(id)) return null;
  return session.can('GetOperation') ? <Link className="btn" to={`/operations/${encodeURIComponent(id)}`}>{label}</Link> : <p>Operation ID: <span className="mono">{id}</span>. Your identity cannot read this receipt.</p>;
}

export function MutationReceipt({ response }: { response: APIResponse<unknown> }) {
  const query = useOperation(response.operationID);
  let received: OperationObservation | undefined;
  if (object(response.data) && response.data.operation) {
    try { received = operationView(response.data.operation, response.operationID); } catch { /* Already sanitized feedback can only provide supplemental evidence. */ }
  }
  const receipt = query.data?.data ?? received;
  return <>
    <p>{receipt ? operationMessage(receipt) : 'Saved durably. Controller application has not yet been confirmed.'}</p>
    {query.isError && <p role="alert">Operation progress could not be refreshed. This does not reverse the saved change.</p>}
    <OperationLink id={response.operationID} />
    {!operationID(response.operationID) && <p className="muted">No usable operation ID was returned. Inspect the saved resource; this response cannot establish controller application.</p>}
  </>;
}

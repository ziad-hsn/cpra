import type { ManagementError } from '../api/session';
import { correlationID } from '../api/correlation';
import { safeValidationField, validationMessage } from '../api/validation';
import { useFieldValidation } from './FieldValidation';

const codes = new Set([
  'internalError', 'unauthorized', 'forbidden', 'admissionBusy', 'admissionUnavailable', 'outcomeUnconfirmed', 'operationAllocationUnconfirmed', 'requestInterrupted', 'unavailable',
  'collectionQuota', 'collectionConflict', 'invalidCollection', 'collectionUnavailable', 'operationExpired', 'operationConflict', 'operationNotFound', 'historyUnavailable', 'cursorExpired', 'notFound', 'versionConflict',
  'evidenceConflict', 'recoveryIneligible', 'recoveryRateLimited', 'executorUnfenced', 'invalidControl', 'resourceReferenced', 'dependencyConflict', 'validationGraphLimit', 'invalidResource', 'invalidGraph',
  'validationPending', 'validationInterrupted', 'validationCanceled', 'validationNotRequested', 'featureUnavailable', 'preconditionRequired', 'invalidPrecondition', 'invalidJSON', 'bodyTooLarge',
]);
const phases = new Set(['upload', 'validation', 'activation', 'cancellation', 'observation']);

/** Correlation and reviewed codes are useful; raw provider detail and submitted
 * values are deliberately excluded from the rendered error, including secrets. */
export function ProblemDetails({ error, phase }: { error?: ManagementError; phase?: string }) {
  const validation = useFieldValidation();
  if (!error) return null;
  const request = error.response?.requestID || error.problem?.requestID;
  const operation = error.response?.operationID || error.problem?.operationID;
  const code = error.problem?.code;
  const fields = error.problem?.errors ?? [];
  return <div className="problem-details">
    <dl className="kv">
      {phase && phases.has(phase) && <><dt>Phase</dt><dd>{phase}</dd></>}
      {error.status !== undefined && Number.isInteger(error.status) && error.status >= 100 && error.status <= 599 && <><dt>HTTP status</dt><dd>{error.status}</dd></>}
      {code && codes.has(code) && <><dt>Problem code</dt><dd className="mono">{code}</dd></>}
      {correlationID(request) && <><dt>Request ID</dt><dd className="mono">{request}</dd></>}
      {correlationID(operation) && <><dt>Operation ID</dt><dd className="mono">{operation}</dd></>}
    </dl>
    {!!fields.length && <ul aria-label="Field errors">{fields.slice(0, 10).map((field, index) => <li key={index}>
      {safeValidationField(field.field) || 'Resource field'}: {validationMessage(field.reason) || 'validation failed'}{field.reason && codes.has(field.reason) ? ` (${field.reason})` : ''}.
      {field.field && validation.target(field.field) && <button type="button" className="btn ghost" onClick={() => {
        const target = validation.target(field.field!); if (target) document.getElementById(target)?.focus();
      }}>Go to {safeValidationField(field.field)}</button>}
    </li>)}</ul>}
    {fields.length > 10 && <p>Additional field errors omitted.</p>}
  </div>;
}

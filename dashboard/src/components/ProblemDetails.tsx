import type { ManagementError } from '../api/session';

const codes = new Set([
  'internalError', 'unauthorized', 'forbidden', 'admissionBusy', 'admissionUnavailable', 'outcomeUnconfirmed', 'operationAllocationUnconfirmed', 'requestInterrupted', 'unavailable',
  'collectionQuota', 'collectionConflict', 'invalidCollection', 'collectionUnavailable', 'operationExpired', 'operationConflict', 'operationNotFound', 'historyUnavailable', 'cursorExpired', 'notFound', 'versionConflict',
  'evidenceConflict', 'recoveryIneligible', 'recoveryRateLimited', 'executorUnfenced', 'invalidControl', 'resourceReferenced', 'dependencyConflict', 'validationGraphLimit', 'invalidResource', 'invalidGraph',
  'validationPending', 'validationInterrupted', 'validationCanceled', 'validationNotRequested', 'featureUnavailable', 'preconditionRequired', 'invalidPrecondition', 'invalidJSON', 'bodyTooLarge',
]);
const fieldName = /^(metadata|spec|items|sources|revision|reason|note|duration|incidentID|resolution|evidenceRefs)(\.[A-Za-z][A-Za-z0-9_]*|\[[0-9]+\])*$/;
const phases = new Set(['upload', 'validation', 'activation', 'cancellation', 'observation']);
const uuid = '[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}';
const correlation = new RegExp(`^(?:${uuid}|op\\.${uuid}\\.[0-9]{20})$`);
const safeIdentity = (value?: string) => !!value && correlation.test(value) && !value.includes('00000000-0000-0000-0000-000000000000');

/** Correlation and reviewed codes are useful; raw provider detail and submitted
 * values are deliberately excluded from the rendered error, including secrets. */
export function ProblemDetails({ error, phase }: { error?: ManagementError; phase?: string }) {
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
      {safeIdentity(request) && <><dt>Request ID</dt><dd className="mono">{request}</dd></>}
      {safeIdentity(operation) && <><dt>Operation ID</dt><dd className="mono">{operation}</dd></>}
    </dl>
    {!!fields.length && <ul aria-label="Field errors">{fields.slice(0, 10).map((field, index) => <li key={index}>
      {field.field.length <= 160 && fieldName.test(field.field) ? field.field : 'Resource field'}: validation failed{field.reason && codes.has(field.reason) ? ` (${field.reason})` : ''}.
    </li>)}</ul>}
    {fields.length > 10 && <p>Additional field errors omitted.</p>}
  </div>;
}

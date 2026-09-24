import type { ApplyResult, Operation, OperationList } from './generated';
import { collectionIdentityFormat, fileNormalizationProfile } from './collectionInventory';
import type { CollectionIdentity } from './collectionOperations';
import { object } from './value';
import { executionObservation } from './executionResults';
import { DashboardSession, ManagementError, type APIResponse } from './session';

// Observation types retain bounded future identity formats without pretending that
// this dashboard can construct or use their mutation contracts.
export type OperationObservation = Omit<Operation, 'identityFormat'> & { identityFormat?: string };
export type OperationListObservation = Omit<OperationList, 'items'> & { items: OperationObservation[] };

export function supportedCollectionIdentity(operation: OperationObservation): CollectionIdentity | undefined {
  return operation.identityFormat === collectionIdentityFormat && operation.itemCount !== undefined &&
    (operation.normalizationProfile === undefined || operation.normalizationProfile === fileNormalizationProfile)
    ? { identityFormat: collectionIdentityFormat, contentDigest: operation.contentDigest, itemCount: operation.itemCount,
      ...(operation.normalizationProfile ? { normalizationProfile: operation.normalizationProfile } : {}) } : undefined;
}

const states = new Set(['reserved', 'pending', 'staging', 'uploading', 'validating', 'validated', 'rejected', 'applying', 'committed', 'completed', 'failed', 'partial', 'interrupted', 'invalidated', 'canceled', 'cancelled', 'expired']);
const outcomes = new Set(['reserved', 'activation_rejected', 'reservation_expired', 'committed', 'applied', 'projection_failed', 'superseded']);
const count = (value: unknown): value is number => typeof value === 'number' && Number.isSafeInteger(value) && value >= 0;
export const operationID = (value: unknown): value is string => typeof value === 'string' && value.length > 0 && value.length <= 256 && value !== '.' && value !== '..' && !/[\\/?#%\x00-\x1f]/.test(value);

/** Operation receipts contain identities and outcomes, never resource payloads. */
export function operationView(value: unknown, expectedID?: string): OperationObservation {
  if (!object(value) || !operationID(value.id) || (expectedID !== undefined && value.id !== expectedID) || typeof value.state !== 'string' || typeof value.contentDigest !== 'string' || !/^(?:sha256:)?[a-f\d]{64}$/i.test(value.contentDigest)) {
    throw new ManagementError('invalid', 'The server returned an unsupported operation receipt.');
  }
  const result: OperationObservation = { id: value.id, state: states.has(value.state) ? value.state : 'unrecognized', contentDigest: value.contentDigest };
  for (const key of ['committed', 'applied', 'uploaded', 'retryAfterSeconds'] as const) {
    if (value[key] !== undefined) {
      if (!count(value[key])) throw new ManagementError('invalid', 'The operation receipt contains invalid progress counts.');
      result[key] = value[key];
    }
  }
  if (value.validated !== undefined) {
    if (typeof value.validated !== 'boolean') throw new ManagementError('invalid', 'The operation receipt contains an invalid validation observation.');
    result.validated = value.validated;
  }
  if (value.normalizationProfile !== undefined) {
    if (typeof value.normalizationProfile !== 'string' || !/^[a-zA-Z0-9][a-zA-Z0-9._:-]{0,95}$/.test(value.normalizationProfile) || value.identityFormat === undefined) {
      throw new ManagementError('invalid', 'The operation receipt contains an invalid normalization profile.');
    }
    result.normalizationProfile = value.normalizationProfile;
  }
  if (value.identityFormat !== undefined || value.itemCount !== undefined) {
    if (typeof value.identityFormat !== 'string' || !/^[a-zA-Z0-9][a-zA-Z0-9._:-]{0,127}$/.test(value.identityFormat) || !count(value.itemCount) || value.itemCount > 10_000_000 ||
      (value.identityFormat === collectionIdentityFormat && !/^[a-f0-9]{64}$/.test(value.contentDigest))) {
      throw new ManagementError('invalid', 'The operation receipt contains an unsupported collection identity.');
    }
    result.identityFormat = value.identityFormat;
    result.itemCount = value.itemCount;
    const total = value.itemCount;
    if (['uploaded', 'committed', 'applied'].some(key => typeof value[key] === 'number' && value[key] > total) ||
      (result.applied !== undefined && result.committed !== undefined && result.applied > result.committed)) {
      throw new ManagementError('invalid', 'The collection progress exceeds its declared inventory.');
    }
  }
  if (value.nextCursor !== undefined) {
    if (typeof value.nextCursor !== 'string' || value.nextCursor.length > 8192) throw new ManagementError('invalid', 'The operation receipt contains an unsupported cursor.');
    result.nextCursor = value.nextCursor;
  }
  if (value.executionResult !== undefined) {
    Object.assign(result, executionObservation(value.executionResult, result.itemCount, value.items, result.nextCursor));
  } else if (value.items !== undefined) {
    if (!Array.isArray(value.items) || value.items.length > 500) throw new ManagementError('too-large', 'The operation receipt exceeds the 500-item page limit.');
    result.items = value.items.map(item => {
      if (!object(item) || !operationID(item.id) || typeof item.outcome !== 'string') throw new ManagementError('invalid', 'The operation receipt contains an unsupported item.');
      const result: ApplyResult = { id: item.id, outcome: outcomes.has(item.outcome) ? item.outcome : 'unrecognized' };
      for (const key of ['oldVersion', 'newVersion'] as const) if (typeof item[key] === 'string' && item[key].length <= 256) result[key] = item[key];
      for (const key of ['committed', 'applied'] as const) if (typeof item[key] === 'boolean') result[key] = item[key];
      // Do not retain arbitrary provider messages or extra request/config fields,
      // even if an incompatible server accidentally echoes them in a receipt.
      return result;
    });
  }
  return result;
}

export async function getOperation(session: DashboardSession, id: string, signal?: AbortSignal): Promise<APIResponse<OperationObservation>> {
  if (!operationID(id)) throw new ManagementError('invalid', 'Enter a valid operation ID.');
  const response = await session.get<unknown>(`/api/v2/operations/${encodeURIComponent(id)}`, { signal });
  return { ...response, data: operationView(response.data, id) };
}

/** A page contains only receipt identities and outcomes, never submitted bodies. */
export async function listOperations(session: DashboardSession, cursor = '', monitorID = '', signal?: AbortSignal): Promise<OperationListObservation> {
  if (cursor.length > 8192 || (monitorID !== '' && !/^[a-zA-Z0-9][a-zA-Z0-9._:-]{0,127}$/.test(monitorID))) {
    throw new ManagementError('invalid', 'Use an exact monitor ID and the original operation cursor.');
  }
  const query = new URLSearchParams({ limit: '100' });
  if (cursor) query.set('cursor', cursor);
  if (monitorID) query.set('monitorID', monitorID);
  const response = await session.get<unknown>(`/api/v2/operations?${query}`, { signal });
  const value = response.data;
  if (!object(value) || !Array.isArray(value.items) || value.items.length > 100) {
    throw new ManagementError('invalid', 'The server returned an unsupported operation page.');
  }
  const items = value.items.map(item => operationView(item));
  if (new Set(items.map(item => item.id)).size !== items.length) throw new ManagementError('invalid', 'The operation page contains duplicate identities.');
  const result: OperationListObservation = { items };
  for (const key of ['nextCursor', 'snapshot'] as const) {
    if (value[key] !== undefined) {
      if (typeof value[key] !== 'string' || value[key].length > (key === 'snapshot' ? 256 : 8192)) throw new ManagementError('invalid', 'The operation page contains an invalid continuation.');
      result[key] = value[key];
    }
  }
  if (value.generatedAt !== undefined) {
    if (typeof value.generatedAt !== 'string' || value.generatedAt.startsWith('0001-01-01') || !Number.isFinite(Date.parse(value.generatedAt))) throw new ManagementError('invalid', 'The operation page has an invalid observation time.');
    result.generatedAt = value.generatedAt;
  }
  if (result.nextCursor && (!result.snapshot || !result.generatedAt)) throw new ManagementError('invalid', 'The operation page cannot establish a stable continuation.');
  if (cursor && result.nextCursor === cursor) throw new ManagementError('invalid', 'The operation page did not advance its continuation. Refresh the operation list.');
  return result;
}

export function operationApplied(operation: OperationObservation): boolean {
  return operation.identityFormat === undefined && operation.state === 'completed' && operation.committed !== undefined && operation.committed > 0 && operation.applied === operation.committed &&
    (operation.items ?? []).every(item => item.applied === true && item.committed === true && item.outcome === 'applied');
}

export function operationMessage(operation: OperationObservation): string {
  if (operationApplied(operation)) return 'Saved durably and applied by the controller.';
  if (operation.identityFormat !== undefined && operation.state === 'completed') {
    const results = operation.executionResult;
    const counts = results?.counts ?? results?.summary;
    if (results?.state === 'ready' && counts && counts.accepted + counts.unchanged === operation.itemCount && counts.childApplied === counts.accepted) {
      return counts.accepted === 0 ? 'Every resource was already up to date. No new changes were committed.' : 'Every resource was applied by the controller or was already up to date.';
    }
  }
  if (operation.state === 'validating') return 'The original collection is being validated. No configuration has been activated.';
  if (operation.state === 'validated') return 'The original collection has a retained successful validation result. Activation has not been requested by validation.';
  if (operation.state === 'rejected') return 'The original collection has a retained rejection. Read its validation result; changed intent requires a new operation.';
  if (operation.state === 'interrupted') return 'The original validation attempt was interrupted and will not be recompiled. Read its retained evidence; changed intent requires a new operation.';
  if (operation.state === 'invalidated' || operation.state === 'expired') return 'This original operation is no longer eligible to continue. No new operation was started.';
  if (operation.state === 'canceled' || operation.state === 'cancelled') return 'The original operation was canceled. Existing committed changes and external actions are not reversed.';
  if (operation.state === 'uploading' || operation.state === 'staging') return 'The collection is staged but inactive. Upload and validation progress do not confirm activation.';
  if (operation.state === 'pending') return 'The original operation is pending. Application has not been confirmed.';
  if (operation.state === 'applying') return 'Conditional application is in progress. Read the original item outcomes; partial completion is possible.';
  if (operation.state === 'reserved') return 'An operation handle is reserved. No resource change or action has been committed by this operation.';
  if (operation.state === 'committed') return 'Saved durably. Controller application has not yet been confirmed.';
  if (operation.state === 'failed' && operation.committed === 0 && operation.items?.some(item => item.outcome === 'reservation_expired')) return 'The unused operation reservation expired. No resource change or action was committed by this operation.';
  if (operation.state === 'failed' && operation.committed === 0) return 'The operation was rejected before committing a resource change or action. Inspect its result before deciding on a new request.';
  if (operation.state === 'failed' && operation.committed === undefined) return 'The operation failed. The receipt does not report whether any resource change or action was committed. Inspect its original resource before deciding on another request.';
  if (operation.state === 'failed') return 'Saved durably, but controller application failed. Inspect the affected resource before deciding what to change.';
  if (operation.state === 'partial' && operation.items?.some(item => item.outcome === 'superseded')) return operation.committed === 0
    ? 'This operation was superseded before committing a resource change or action. Inspect the current resource before deciding on another request.'
    : 'This operation was superseded before application was confirmed. Inspect the current resource and retained history before deciding what to change.';
  if (operation.state === 'partial') return 'The operation finished with partial outcomes. Inspect each result; the collection was not rolled back.';
  if (operation.state === 'completed') return 'The operation is completed, but the receipt does not confirm that every committed item was applied.';
  return 'This server reports an operation state the dashboard cannot interpret. Application has not been confirmed.';
}

export function operationPollInterval(response?: APIResponse<OperationObservation>, error?: unknown): number | false {
  if (error instanceof ManagementError && ([401, 403, 404, 410].includes(error.status ?? 0) || ['invalid', 'too-large', 'forbidden', 'session-changed'].includes(error.reason))) return false;
  if (!error && response && !['committed', 'reserved', 'pending', 'validating', 'applying'].includes(response.data.state)) return false;
  const retry = error instanceof ManagementError ? error.response?.retryAfterMs ?? 0 : 0;
  return Math.min(2_147_483_647, Math.max(5000, Number.isFinite(retry) ? retry : 2_147_483_647,
    response?.retryAfterMs ?? 0, (response?.data.retryAfterSeconds ?? 0) * 1000));
}

import { collectionIdentityFormat, type WorkerCollectionSummary } from './collectionInventory';
import { operationID } from './operations';
import { object } from './value';
import { executionObservation } from './executionResults';
import { DashboardSession, ManagementError, type APIResponse, type CollectionRequestOperation } from './session';
import type { ApplyResult, CollectionAdmission, Operation, Preflight, ValidationResultPage } from './generated';
import type { PrivateCollectionBody } from '../import/collection';

export type CollectionIdentity = Pick<WorkerCollectionSummary, 'identityFormat' | 'contentDigest' | 'itemCount' | 'normalizationProfile'>;
export const collectionTicketMaxBytes = 128 << 10;
export const collectionStates = new Set(['pending', 'staging', 'uploading', 'validating', 'validated', 'rejected', 'interrupted', 'applying', 'committed', 'completed', 'partial', 'failed', 'canceled', 'cancelled', 'expired', 'invalidated']);
const outcomes = new Set(['create', 'update', 'unchanged', 'invalid', 'skipped', 'conflict', 'dependency_failed', 'reserved', 'committed', 'applied', 'failed', 'canceled', 'cancelled', 'projection_failed', 'superseded', 'activation_rejected']);
const count = (value: unknown): value is number => typeof value === 'number' && Number.isSafeInteger(value) && value >= 0;
const invalid = () => new ManagementError('invalid', 'The server response does not match this frozen collection. No further step was submitted.');

/** Only server-registered operations also granted by /self are enabled. Older
 * discovery without operation metadata never implies write support. */
export function collectionPermissions(session: DashboardSession, discovery: unknown): ReadonlySet<string> {
  if (!object(discovery) || !object(discovery.resourceOperations)) return new Set();
  const result = new Set<string>();
  for (const kind of ['Collection', 'Operation']) {
    const operations = discovery.resourceOperations[kind];
    if (Array.isArray(operations) && operations.length <= 100) for (const operation of operations) {
      if (typeof operation === 'string' && session.can(operation)) result.add(operation);
    }
  }
  return result;
}
function identity(value: Record<string, unknown>, expected: CollectionIdentity): void {
  if (value.identityFormat !== collectionIdentityFormat || value.identityFormat !== expected.identityFormat || value.contentDigest !== expected.contentDigest || value.itemCount !== expected.itemCount) throw invalid();
}
function itemResults(value: unknown, maximum: number): ApplyResult[] | undefined {
  if (value === undefined) return undefined;
  if (!Array.isArray(value) || value.length > maximum) throw invalid();
  const ids = new Set<string>();
  return value.map(item => {
    if (!object(item) || typeof item.id !== 'string' || item.id.length > 321 || !/^[A-Za-z][A-Za-z0-9]{0,63}\//.test(item.id) || !operationID(item.id.slice(item.id.indexOf('/') + 1)) || typeof item.outcome !== 'string' || ids.has(item.id)) throw invalid();
    ids.add(item.id);
    const result: ApplyResult = { id: item.id, outcome: outcomes.has(item.outcome) ? item.outcome : 'unrecognized' };
    for (const key of ['oldVersion', 'newVersion'] as const) if (typeof item[key] === 'string' && item[key].length <= 256) result[key] = item[key];
    for (const key of ['committed', 'applied'] as const) if (typeof item[key] === 'boolean') result[key] = item[key];
    // Never retain messages, echoed resource bodies, source paths or secrets.
    return result;
  });
}
export function collectionPreflight(value: unknown, expected: CollectionIdentity): Preflight {
  if (!object(value) || typeof value.valid !== 'boolean') throw invalid();
  identity(value, expected);
  return { valid: value.valid, ...expected, items: itemResults(value.items, expected.itemCount) };
}
export function collectionOperation(value: unknown, expected: CollectionIdentity, id?: string): Operation {
  if (!object(value) || !operationID(value.id) || (id !== undefined && value.id !== id) || typeof value.state !== 'string') throw invalid();
  identity(value, expected);
  if (value.normalizationProfile !== expected.normalizationProfile) throw invalid();
  const result: Operation = { ...expected, id: value.id, state: collectionStates.has(value.state) ? value.state : 'unrecognized', items: value.executionResult === undefined ? itemResults(value.items, 500) : undefined };
  for (const field of ['uploaded', 'committed', 'applied'] as const) {
    if (value[field] !== undefined) {
      if (!count(value[field]) || value[field] > expected.itemCount) throw invalid();
      result[field] = value[field];
    }
  }
  if (value.validated !== undefined) { if (typeof value.validated !== 'boolean') throw invalid(); result.validated = value.validated; }
  if (value.retryAfterSeconds !== undefined) { if (!count(value.retryAfterSeconds) || value.retryAfterSeconds > 86400) throw invalid(); result.retryAfterSeconds = value.retryAfterSeconds; }
  if (value.nextCursor !== undefined) { if (typeof value.nextCursor !== 'string' || value.nextCursor.length > 8192) throw invalid(); result.nextCursor = value.nextCursor; }
  if (value.executionResult !== undefined) Object.assign(result, executionObservation(value.executionResult, expected.itemCount, value.items, result.nextCursor));
  return result;
}

/** Consumes a private body on success and failure. It never logs or caches it. */
export async function collectionCall(session: DashboardSession, allowed: ReadonlySet<string>, operation: CollectionRequestOperation, expected: CollectionIdentity, body?: PrivateCollectionBody, id?: string, signal?: AbortSignal, ticket?: string): Promise<APIResponse<Operation | Preflight>> {
  let bytes: Uint8Array<ArrayBuffer> | undefined;
  try {
    if (!allowed.has(operation)) throw new ManagementError('forbidden', 'The server has not enabled this collection operation for your identity.');
    bytes = body?.take();
    if (operation === 'CreateOperation') {
      if (!bytes || bytes[bytes.length - 1] !== 125 || typeof ticket !== 'string' || !ticket || ticket.length > collectionTicketMaxBytes) throw invalid();
      const suffix = new TextEncoder().encode(`,"admissionTicket":${JSON.stringify(ticket)}}`);
      const attached = new Uint8Array(bytes.length - 1 + suffix.length);
      if (attached.length > 4 << 20) throw invalid();
      attached.set(bytes.subarray(0, -1)); attached.set(suffix, bytes.length - 1); bytes.fill(0); bytes = attached;
    }
    const response = await session.collectionRequest<unknown>(operation, bytes, id, signal);
    try {
      if (operation === 'ValidateOperation' && response.status !== 202) throw invalid();
      if (operation === 'ActivateOperation' && (response.status !== 200 || response.operationID !== id)) throw invalid();
      if (operation === 'PreflightCollection') return { ...response, data: collectionPreflight(response.data, expected) };
      const data = collectionOperation(response.data, expected, id);
      // A matching handle alone does not acknowledge activation. An applying
      // receipt or validated execution availability proves admission, including
      // an original result returned after cancellation or retention expiry.
      if (operation === 'ActivateOperation' && data.state !== 'applying' &&
        (!data.executionResult || !['pending', 'ready', 'expired'].includes(data.executionResult.state) || data.items?.length || data.nextCursor)) throw invalid();
      return { ...response, data };
    } catch {
      if (operation === 'PreflightCollection') throw invalid();
      const metadata = { status: response.status, requestID: response.requestID, operationID: operation === 'ActivateOperation' ? id! : response.operationID, resourceVersion: response.resourceVersion, retryAfterMs: response.retryAfterMs };
      throw new ManagementError('unconfirmed', 'The response could not confirm the original collection operation. Read its progress before continuing.', undefined, undefined, undefined, metadata);
    }
  } finally { bytes?.fill(0); body?.close(); }
}

/** Ticket stays in the page's private lifecycle ref, never in React/query state. */
export async function prepareCollection(session: DashboardSession, allowed: ReadonlySet<string>, body: PrivateCollectionBody, signal?: AbortSignal): Promise<CollectionAdmission> {
  try {
    if (!allowed.has('PrepareCollection')) throw new ManagementError('forbidden', 'The server has not enabled collection admission preparation.');
    const response = await session.collectionRequest<unknown>('PrepareCollection', body.take(), undefined, signal);
    const value = response.data;
    if (!object(value) || typeof value.ticket !== 'string' || !value.ticket || value.ticket.length > collectionTicketMaxBytes || typeof value.expiresAt !== 'string' || !/^\d{4}-\d\d-\d\dT/.test(value.expiresAt) || !Number.isFinite(Date.parse(value.expiresAt))) throw invalid();
    return { ticket: value.ticket, expiresAt: value.expiresAt };
  } finally { body.close(); }
}
export async function readCollection(session: DashboardSession, expected: CollectionIdentity, id: string, signal?: AbortSignal, cursor = ''): Promise<APIResponse<Operation>> {
  if (!operationID(id) || cursor.length > 8192 || !session.can('GetOperation')) throw invalid();
  const params = new URLSearchParams({ limit: '100' });
  if (cursor) params.set('cursor', cursor);
  const response = await session.get<unknown>(`/api/v2/operations/${encodeURIComponent(id)}?${params}`, { signal, maxResponseBytes: 4 << 20 });
  return { ...response, data: collectionOperation(response.data, expected, id) };
}
export const collectionMutable = (operation?: Operation) => !!operation && ['pending', 'staging', 'uploading', 'validating', 'validated', 'rejected'].includes(operation.state);
export const collectionCancelable = (operation?: Operation) => collectionMutable(operation) || operation?.state === 'applying';
export const collectionTerminal = (operation?: Operation) => !!operation && ['completed', 'partial', 'failed', 'rejected', 'interrupted', 'canceled', 'cancelled', 'expired', 'invalidated'].includes(operation.state);
export function collectionDelay(response: APIResponse<Operation>): number { return Math.max(5000, Math.min(86400000, response.retryAfterMs || 0), (response.data.retryAfterSeconds ?? 0) * 1000); }

/** A retained observation is not an activation grant. Unknown vocabulary is
 * displayed as unsupported and cannot enable the following mutation. */
export type CollectionValidationPage = ValidationResultPage & CollectionIdentity & { supported: boolean };
const validationIssues = new Set(['invalidSource', 'invalidResource', 'duplicateIdentity', 'invalidGraph', 'validationLimit', 'conflict', 'missingReference', 'unsafePrefix', 'notEvaluated', 'validationInterrupted']);
const changes = new Set(['create', 'update', 'unchanged']);
const hash = (value: unknown): value is string => typeof value === 'string' && /^[a-f0-9]{64}$/.test(value);
const uuid = (value: unknown): value is string => typeof value === 'string' && /^[a-f0-9]{8}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{12}$/.test(value);
const observationTime = (value: unknown): value is string => typeof value === 'string' && value.length <= 64 && !value.startsWith('0001-') && /^\d{4}-\d\d-\d\dT/.test(value) && Number.isFinite(Date.parse(value));
export function collectionValidationPage(value: unknown, expected: CollectionIdentity & { sourceCount?: number }, id: string, cursor = '', previous?: CollectionValidationPage): CollectionValidationPage {
  if (!object(value) || value.operationID !== id || !operationID(id) || !object(value.summary) || !Array.isArray(value.items) || value.items.length > 100) throw invalid();
  identity(value, expected);
  const raw = value.summary;
  if (!uuid(raw.resultID) || typeof raw.valid !== 'boolean' || typeof raw.summaryOnly !== 'boolean' || !count(raw.count) || raw.count > 10_000 || !hash(raw.digest) || !hash(raw.capabilitiesDigest) || !observationTime(raw.finalizedAt) || !observationTime(raw.expiresAt) || Date.parse(raw.expiresAt) <= Date.parse(raw.finalizedAt)) throw invalid();
  const summary: ValidationResultPage['summary'] = { resultID: raw.resultID, valid: raw.valid, summaryOnly: raw.summaryOnly, count: raw.count, digest: raw.digest, capabilitiesDigest: raw.capabilitiesDigest, finalizedAt: raw.finalizedAt, expiresAt: raw.expiresAt };
  let supported = previous?.supported ?? true;
  if (raw.issue !== undefined) {
    if (typeof raw.issue !== 'string' || !raw.issue || raw.issue.length > 64) throw invalid();
    summary.issue = validationIssues.has(raw.issue) ? raw.issue : 'unrecognized'; supported &&= validationIssues.has(raw.issue);
  }
  if (raw.planID !== undefined || raw.planDigest !== undefined) {
    if (!uuid(raw.planID) || !hash(raw.planDigest)) throw invalid();
    summary.planID = raw.planID; summary.planDigest = raw.planDigest;
  }
  if (summary.valid && (summary.summaryOnly || summary.issue !== undefined || !summary.planID) || !summary.valid && summary.planID !== undefined || summary.summaryOnly && (summary.count !== 0 || !summary.issue) || !summary.summaryOnly && summary.count !== expected.itemCount) throw invalid();
  if (previous && (previous.operationID !== id || JSON.stringify(previous.summary) !== JSON.stringify(summary))) throw invalid();
  if (cursor && (!previous || previous.nextCursor !== cursor)) throw invalid();
  const start = cursor ? (previous!.items.at(-1)?.ordinal ?? 0) + 1 : 1;
  const ids = new Set<string>();
  const items = value.items.map((item, index): ValidationResultPage['items'][number] => {
    if (!object(item) || item.ordinal !== start + index || !count(item.ordinal) || item.ordinal > summary.count || typeof item.kind !== 'string' || !['Credential', 'NotificationEndpoint', 'Recipient', 'NotificationGroup', 'Monitor'].includes(item.kind) || !operationID(item.id) || ids.has(`${item.kind}/${item.id}`) || typeof item.source !== 'string' || !/^source\.\d{20}$/.test(item.source) || !count(Number(item.source.slice(7))) || Number(item.source.slice(7)) < 1 || Number(item.source.slice(7)) > (expected.sourceCount ?? 1_000_000) || !count(item.sourceDocument) || item.sourceDocument < 1 || item.sourceDocument > 10_000_000 || !count(item.sourceItem) || item.sourceItem < 1 || item.sourceItem > 10_000_000) throw invalid();
    ids.add(`${item.kind}/${item.id}`);
    const result: ValidationResultPage['items'][number] = { ordinal: item.ordinal, kind: item.kind, id: item.id, source: item.source, sourceDocument: item.sourceDocument, sourceItem: item.sourceItem };
    if (item.change !== undefined) { if (typeof item.change !== 'string' || !item.change || item.change.length > 32) throw invalid(); result.change = changes.has(item.change) ? item.change : 'unrecognized'; supported &&= changes.has(item.change); }
    if (item.issue !== undefined) { if (typeof item.issue !== 'string' || !item.issue || item.issue.length > 64) throw invalid(); result.issue = validationIssues.has(item.issue) ? item.issue : 'unrecognized'; supported &&= validationIssues.has(item.issue); }
    if (!result.change && !result.issue || summary.valid && (!result.change || result.issue)) throw invalid();
    if (item.uid !== undefined || item.resourceVersion !== undefined) {
      if (!operationID(item.uid) || !operationID(item.resourceVersion) || result.change === 'create') throw invalid();
      result.uid = item.uid; result.resourceVersion = item.resourceVersion;
    }
    return result;
  });
  const result: CollectionValidationPage = { identityFormat: expected.identityFormat, contentDigest: expected.contentDigest, itemCount: expected.itemCount,
    ...(expected.normalizationProfile ? { normalizationProfile: expected.normalizationProfile } : {}), operationID: id, summary, items, supported };
  if (value.nextCursor !== undefined) { if (typeof value.nextCursor !== 'string' || !value.nextCursor || value.nextCursor.length > 8192 || value.nextCursor === cursor) throw invalid(); result.nextCursor = value.nextCursor; }
  const last = items.at(-1)?.ordinal ?? 0;
  if (summary.count > 0 && items.length === 0 || !!result.nextCursor !== (last < summary.count)) throw invalid();
  return result;
}
export async function readCollectionValidation(session: DashboardSession, expected: CollectionIdentity & { sourceCount?: number }, id: string, signal?: AbortSignal, cursor = '', previous?: CollectionValidationPage): Promise<APIResponse<CollectionValidationPage>> {
  if (!session.can('GetOperationValidation')) throw new ManagementError('forbidden', 'Your identity cannot read collection validation results.');
  if (!operationID(id) || cursor.length > 8192) throw invalid();
  const params = new URLSearchParams({ limit: '100' }); if (cursor) params.set('cursor', cursor);
  const response = await session.get<unknown>(`/api/v2/operations/${encodeURIComponent(id)}/validation?${params}`, { signal, maxResponseBytes: 4 << 20 });
  if (response.status !== 200) throw invalid();
  return { ...response, data: collectionValidationPage(response.data, expected, id, cursor, previous) };
}
/** Retry only the read for an explicit pending response tied to this handle. */
export function collectionValidationPendingDelay(error: unknown, id: string): number | undefined {
  if (!(error instanceof ManagementError) || error.status !== 409 || error.problem?.code !== 'validationPending' || error.responseIssue || error.response?.operationID !== id) return undefined;
  const delay = error.response.retryAfterMs;
  if (!Number.isSafeInteger(delay) || delay <= 0 || delay > 86_400_000) return undefined;
  return Math.max(5000, delay);
}
export function collectionValidationMessage(error: unknown): string {
  if (error instanceof ManagementError) {
    if (error.status === 410) return 'The original validation evidence has expired. It cannot be resumed; changed intent requires a new operation.';
    if (error.status === 409 && error.problem?.code === 'validationInterrupted') return 'The original validation was interrupted. No verdict is available and it will not be recompiled; changed intent requires a new operation.';
    if (error.status === 409 && error.problem?.code === 'validationCanceled') return 'The original operation was canceled before a verdict. No validation or activation was retried.';
    if (error.status === 409 && error.problem?.code === 'validationNotRequested') return 'No validation request has been committed for this original operation. Reading its result does not submit one.';
    if (error.status === 503 && error.problem?.code === 'historyUnavailable') return 'The original validation history is unavailable. This is not a rejection or a passing verdict; no validation was resubmitted.';
    if (error.status === 403 || error.reason === 'forbidden') return 'Your identity cannot read this validation result. Waiting stopped without canceling the operation.';
  }
  return 'The original validation result could not be confirmed. Waiting stopped without canceling, revalidating or activating the operation.';
}

/** Pins a sealed application result independently of the parent's stop state. */
export function collectionExecutionPage(value: unknown, expected: CollectionIdentity, id: string, cursor = '', previous?: Operation): Operation {
  const result = collectionOperation(value, expected, id);
  if (!result.executionResult || result.executionResult.state !== 'ready') return result;
  if (!result.items?.length || result.items.length > 100 || !result.executionResult.summary) throw invalid();
  if (cursor && (!previous || previous.nextCursor !== cursor) || !cursor && result.items[0].inputOrdinal !== 1 || result.nextCursor === cursor && cursor !== '') throw invalid();
  if (previous && (previous.id !== id || previous.identityFormat !== result.identityFormat || previous.normalizationProfile !== result.normalizationProfile || previous.contentDigest !== result.contentDigest || previous.itemCount !== result.itemCount || JSON.stringify(previous.executionResult?.summary) !== JSON.stringify(result.executionResult.summary))) throw invalid();
  if (cursor && result.items[0].inputOrdinal !== (previous!.items?.at(-1)?.inputOrdinal ?? 0) + 1) throw invalid();
  return result;
}

/** Reads one original result page; never activates, cancels or replaces work. */
export async function readCollectionExecution(session: DashboardSession, expected: CollectionIdentity, id: string, signal?: AbortSignal, cursor = '', previous?: Operation): Promise<APIResponse<Operation>> {
  if (!session.can('GetOperation')) throw new ManagementError('forbidden', 'Your identity cannot read execution results.');
  if (!operationID(id) || cursor.length > 4096) throw invalid();
  const params = new URLSearchParams({ limit: '100' }); if (cursor) params.set('cursor', cursor);
  const response = await session.get<unknown>(`/api/v2/operations/${encodeURIComponent(id)}?${params}`, { signal, maxResponseBytes: 4 << 20 });
  if (response.status !== 200) throw invalid();
  return { ...response, data: collectionExecutionPage(response.data, expected, id, cursor, previous) };
}

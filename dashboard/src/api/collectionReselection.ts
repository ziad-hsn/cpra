import type { CollectionReselectionAttempt } from './generated';
import { fileNormalizationProfile } from './collectionInventory';
import { object } from './value';
import { DashboardSession, ManagementError, type APIResponse, type ReselectionRequestOperation, type ReselectionRequestOptions } from './session';

export type ReselectionStatus = CollectionReselectionAttempt;
export const reselectionOperations: readonly ReselectionRequestOperation[] = [
  'CreateCollectionReselection', 'GetCollectionReselection', 'UploadCollectionReselectionSource',
  'VerifyCollectionReselection', 'ResumeCollectionReselection', 'DiscardCollectionReselection',
];
export interface ReselectionSourcePart {
  source: number;
  offset: number;
  end: boolean;
  /** Ownership transfers to this request; the buffer is cleared on every exit. */
  data: Uint8Array<ArrayBuffer>;
}

const phases = new Set(['uploading', 'verifying', 'verified', 'transferring', 'completed', 'failed']);
const failures = new Set(['input_mismatch', 'authorization_changed', 'operation_changed', 'expired', 'storage_unavailable', 'quota_exceeded', 'verification_failed', 'transfer_failed']);
const attemptID = (value: unknown): value is string => typeof value === 'string' && /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/.test(value);
const count = (value: unknown, maximum: number): value is number => typeof value === 'number' && Number.isSafeInteger(value) && value >= 0 && value <= maximum;
const invalid = () => new ManagementError('invalid', 'The server returned an unsupported reselection observation. Inspect the original operation before continuing.');

/** Copies only the bounded public status vocabulary. Input, filenames, keys,
 * diagnostics and unknown fields never enter component or query state. */
export function reselectionStatus(value: unknown, id: string, attempt?: string): ReselectionStatus {
  if (!object(value) || !attemptID(value.id) || value.operationID !== id || attempt !== undefined && value.id !== attempt || value.normalizationProfile !== fileNormalizationProfile ||
    typeof value.phase !== 'string' || !phases.has(value.phase) || !count(value.sourceCount, 1000) || value.sourceCount < 1 || !count(value.sourcesCompleted, value.sourceCount) ||
    !count(value.rawBytes, 64 << 20) || !count(value.nextOffset, value.rawBytes) || !count(value.nextSource, 1000) || !count(value.operationUploaded, 10_000) ||
    typeof value.expiresAt !== 'string' || value.expiresAt.length > 40 || !/^\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d(?:\.\d{1,9})?(?:Z|[+-]\d\d:\d\d)$/.test(value.expiresAt) || !Number.isFinite(Date.parse(value.expiresAt))) throw invalid();
  const complete = value.sourcesCompleted === value.sourceCount;
  if (complete ? value.nextSource !== 0 || value.nextOffset !== 0 : value.nextSource !== value.sourcesCompleted + 1) throw invalid();
  if (value.phase !== 'uploading' && value.phase !== 'failed' && !complete) throw invalid();
  if (value.phase === 'failed' ? typeof value.errorCode !== 'string' || !failures.has(value.errorCode) : value.errorCode !== undefined) throw invalid();
  return {
    id: value.id, operationID: id, normalizationProfile: fileNormalizationProfile, phase: value.phase as ReselectionStatus['phase'],
    sourceCount: value.sourceCount, sourcesCompleted: value.sourcesCompleted, rawBytes: value.rawBytes, nextSource: value.nextSource, nextOffset: value.nextOffset,
    expiresAt: value.expiresAt, operationUploaded: value.operationUploaded,
    ...(value.errorCode === undefined ? {} : { errorCode: value.errorCode as ReselectionStatus['errorCode'] }),
  };
}

function metadata(response: Omit<APIResponse<unknown>, 'data'>, id: string): Omit<APIResponse<unknown>, 'data'> {
  return { status: response.status, operationID: id, requestID: '', resourceVersion: '',
    retryAfterMs: Number.isFinite(response.retryAfterMs) && response.retryAfterMs >= 0 ? Math.min(response.retryAfterMs, 86_400_000) : 0 };
}

// A server error may echo private input. Preserve error category and safe
// progress identity, without returning the raw problem or response headers.
function safeError(error: unknown, id: string): ManagementError {
  if (!(error instanceof ManagementError)) return new ManagementError('unavailable', 'The reselection request could not be completed. Inspect the original operation before continuing.');
  const messages: Record<ManagementError['reason'], string> = {
    http: 'The server rejected this reselection request. Inspect the original operation before continuing.',
    'not-admitted': 'The request was not admitted. Inspect the original operation before continuing.',
    unconfirmed: 'The reselection outcome is unconfirmed. Inspect the attempt and original operation; no automatic retry was made.',
    cancelled: 'The reselection request was cancelled or timed out.',
    'session-changed': 'The session changed during the reselection request.',
    unavailable: 'The reselection service is unavailable.',
    invalid: 'The reselection request or observation is unsupported.',
    'too-large': 'The reselection response exceeds the supported size.',
    forbidden: 'This reselection operation is not permitted for your identity.',
  };
  return new ManagementError(error.reason, messages[error.reason], error.status, undefined, error.responseIssue,
    error.response ? metadata(error.response, id) : undefined);
}

async function call(session: DashboardSession, allowed: ReadonlySet<string>, operation: ReselectionRequestOperation, options: ReselectionRequestOptions, expectedStatus: number,
  validate?: (status: ReselectionStatus) => boolean): Promise<APIResponse<ReselectionStatus>> {
  try {
    if (!allowed.has(operation)) throw new ManagementError('forbidden', 'This reselection operation is not enabled for your identity.');
    const response = await session.reselectionRequest<unknown>(operation, options);
    try {
      if (response.status !== expectedStatus || response.operationID !== '' && response.operationID !== options.id) throw invalid();
      const status = reselectionStatus(response.data, options.id, options.attempt);
      if (validate && !validate(status)) throw invalid();
      return { ...metadata(response, options.id), data: status };
    } catch {
      throw new ManagementError(operation === 'GetCollectionReselection' ? 'invalid' : 'unconfirmed', 'The reselection response could not be confirmed.', undefined, undefined, undefined, metadata(response, options.id));
    }
  } catch (error) { throw safeError(error, options.id); }
  finally { options.body?.fill(0); }
}

/** Starts only an attempt for the original operation. It neither activates
 * resources nor uploads sources, and the profile cannot be caller-substituted. */
export function createReselection(session: DashboardSession, allowed: ReadonlySet<string>, id: string, sourceCount: number, signal?: AbortSignal): Promise<APIResponse<ReselectionStatus>> {
  if (!count(sourceCount, 1000) || sourceCount < 1) return Promise.reject(new ManagementError('invalid', 'Select between one and one thousand original sources.'));
  const body = new TextEncoder().encode(JSON.stringify({ sourceCount, normalizationProfile: fileNormalizationProfile }));
  return call(session, allowed, 'CreateCollectionReselection', { id, body, signal }, 201,
    status => status.sourceCount === sourceCount && status.phase === 'uploading' && status.sourcesCompleted === 0 && status.rawBytes === 0 && status.nextSource === 1 && status.nextOffset === 0);
}

export function getReselection(session: DashboardSession, allowed: ReadonlySet<string>, id: string, attempt: string, signal?: AbortSignal): Promise<APIResponse<ReselectionStatus>> {
  return call(session, allowed, 'GetCollectionReselection', { id, attempt, signal }, 200);
}

export function uploadReselectionSource(session: DashboardSession, allowed: ReadonlySet<string>, id: string, attempt: string, part: ReselectionSourcePart, signal?: AbortSignal): Promise<APIResponse<ReselectionStatus>> {
  const { source, offset, end, data } = part;
  const through = offset + data.byteLength;
  return call(session, allowed, 'UploadCollectionReselectionSource', { id, attempt, source, offset, end, body: data, signal }, 200,
    status => status.sourceCount >= source && status.rawBytes >= through &&
      (status.sourcesCompleted >= source || !end && status.nextSource === source && status.nextOffset >= through));
}

export function verifyReselection(session: DashboardSession, allowed: ReadonlySet<string>, id: string, attempt: string, signal?: AbortSignal): Promise<APIResponse<ReselectionStatus>> {
  return call(session, allowed, 'VerifyCollectionReselection', { id, attempt, signal }, 202, status => status.phase !== 'uploading');
}

export function resumeReselection(session: DashboardSession, allowed: ReadonlySet<string>, id: string, attempt: string, signal?: AbortSignal): Promise<APIResponse<ReselectionStatus>> {
  return call(session, allowed, 'ResumeCollectionReselection', { id, attempt, signal }, 202,
    status => status.phase === 'transferring' || status.phase === 'completed' || status.phase === 'failed');
}

/** Discard is explicit and affects only the disposable attempt. */
export async function discardReselection(session: DashboardSession, allowed: ReadonlySet<string>, id: string, attempt: string, signal?: AbortSignal): Promise<void> {
  try {
    if (!allowed.has('DiscardCollectionReselection')) throw new ManagementError('forbidden', 'Discard is not enabled for your identity.');
    const response = await session.reselectionRequest<unknown>('DiscardCollectionReselection', { id, attempt, signal });
    if (response.status !== 204 || response.data !== undefined || response.operationID !== '' && response.operationID !== id) throw new ManagementError('unconfirmed', 'The discard outcome is unconfirmed.');
  } catch (error) { throw safeError(error, id); }
}
